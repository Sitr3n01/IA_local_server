//go:build windows

package claudedesktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// DiscoverWindows resolves the installed MSIX and app identity without
// embedding a versioned package path. It is read-only and does not launch
// Claude Desktop.
func DiscoverWindows(ctx context.Context, backupDir string) (Identity, Paths, error) {
	if strings.TrimSpace(backupDir) == "" || !filepath.IsAbs(backupDir) {
		return Identity{}, Paths{}, errors.New("CIA Claude backup directory must be absolute")
	}
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	if localAppData == "" || appData == "" {
		return Identity{}, Paths{}, errors.New("LOCALAPPDATA and APPDATA are required to discover Claude Desktop")
	}
	const command = "$p=Get-AppxPackage -Name Claude | Sort-Object Version -Descending | Select-Object -First 1; if($null -eq $p){exit 42}; $a=Get-StartApps | Where-Object {$_.AppID -like ($p.PackageFamilyName+'!*')} | Select-Object -First 1; if($null -eq $a){exit 43}; [pscustomobject]@{package_family_name=$p.PackageFamilyName;app_user_model_id=$a.AppID;version=$p.Version.ToString();install_location=$p.InstallLocation} | ConvertTo-Json -Compress"
	result := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command)
	output, err := result.Output()
	if err != nil {
		return Identity{}, Paths{}, fmt.Errorf("locate Claude Desktop MSIX: %w", err)
	}
	var identity Identity
	if err := json.Unmarshal(bytes.TrimSpace(output), &identity); err != nil {
		return Identity{}, Paths{}, fmt.Errorf("decode Claude Desktop MSIX identity: %w", err)
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, Paths{}, err
	}
	paths := Paths{
		ThirdPartyStateDir: filepath.Join(localAppData, "Claude-3p"),
		FirstPartyConfig:   filepath.Join(appData, "Claude", "claude_desktop_config.json"),
		BackupDir:          backupDir,
	}
	return identity, paths, paths.Validate()
}

type windowsPolicyReader struct{}

func NewWindowsPolicyReader() PolicyReader { return windowsPolicyReader{} }

// ManagedInference reports only a policy that configures inference itself.
// Other Claude policies (for example update cadence) do not block CIA's local
// profile. HKLM wins over HKCU in the Desktop, but either makes a local config
// library mutation ambiguous, so both fail closed.
func (windowsPolicyReader) ManagedInference(context.Context) (bool, error) {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		key, err := registry.OpenKey(root, `SOFTWARE\Policies\Claude`, registry.QUERY_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, name := range []string{"inferenceProvider", "inferenceGatewayBaseUrl", "inferenceGatewayApiKey", "inferenceCredentialHelper"} {
			_, _, err := key.GetValue(name, nil)
			if err == nil {
				_ = key.Close()
				return true, nil
			}
			if !errors.Is(err, registry.ErrNotExist) {
				_ = key.Close()
				return false, err
			}
		}
		_ = key.Close()
	}
	return false, nil
}

const (
	swShowNormal = 1
	swRestore    = 9
)

var (
	claudeUser32                     = windows.NewLazySystemDLL("user32.dll")
	claudeShell32                    = windows.NewLazySystemDLL("shell32.dll")
	procClaudeEnumWindows            = claudeUser32.NewProc("EnumWindows")
	procClaudeGetWindowThreadProcess = claudeUser32.NewProc("GetWindowThreadProcessId")
	procClaudeIsWindowVisible        = claudeUser32.NewProc("IsWindowVisible")
	procClaudeSetForegroundWindow    = claudeUser32.NewProc("SetForegroundWindow")
	procClaudeShowWindow             = claudeUser32.NewProc("ShowWindow")
	procClaudeShellExecute           = claudeShell32.NewProc("ShellExecuteW")
)

type windowsDesktop struct{ identity Identity }

// NewWindowsDesktop drives the discovered package through the Windows shell.
// It only starts the app and foregrounds windows; it has no way to stop a
// process, so neither instance can be closed by CIA.
func NewWindowsDesktop(identity Identity) (Desktop, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return windowsDesktop{identity: identity}, nil
}

func (d windowsDesktop) Show(_ context.Context, dataDir string) (bool, error) {
	processes, err := packageProcesses(d.identity)
	if err != nil {
		return false, fmt.Errorf("enumerate Claude Desktop processes: %w", err)
	}
	main := instanceMain(processes, dataDir)
	if main == 0 {
		return false, nil
	}
	window := findClaudeTopLevelWindow(main)
	if window == 0 {
		return false, nil
	}
	_, _, _ = procClaudeShowWindow.Call(uintptr(window), swRestore)
	_, _, _ = procClaudeSetForegroundWindow.Call(uintptr(window))
	return true, nil
}

// Activate asks the interactive Windows shell to activate the AUMID directly.
// Starting explorer.exe as a child process can return before the shell
// namespace activation is handed off and has left this FullTrustApplication
// suspended after package updates.
func (d windowsDesktop) Activate(context.Context) error {
	operation, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString("shell:AppsFolder\\" + d.identity.AppUserModelID)
	if err != nil {
		return fmt.Errorf("encode Claude AUMID: %w", err)
	}
	result, _, callErr := procClaudeShellExecute.Call(
		0,
		uintptr(unsafe.Pointer(operation)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		swShowNormal,
	)
	if result <= 32 {
		if callErr != nil {
			return fmt.Errorf("ShellExecuteW failed with code %d: %w", result, callErr)
		}
		return fmt.Errorf("ShellExecuteW failed with code %d", result)
	}
	return nil
}

// WaitShown polls until the instance shows a window. The Store build can leave
// a freshly activated FullTrustApplication at its initial suspend count; once,
// after a short grace period, it resumes the threads of main processes no
// helper belongs to yet. A running instance always has helpers, so neither the
// signed-in instance nor a running local one is ever touched.
func (d windowsDesktop) WaitShown(ctx context.Context, dataDir string) error {
	started := time.Now()
	resumed := false
	for {
		shown, err := d.Show(ctx, dataDir)
		if err != nil {
			return err
		}
		if shown {
			return nil
		}
		if !resumed && time.Since(started) > 3*time.Second {
			resumed = true
			if processes, err := packageProcesses(d.identity); err == nil {
				_ = resumeProcessThreads(unclaimedMains(processes))
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("the Claude Desktop instance did not show a window in time")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func resumeProcessThreads(pids []uint32) error {
	if len(pids) == 0 {
		return nil
	}
	targets := make(map[uint32]bool, len(pids))
	for _, pid := range pids {
		targets[pid] = true
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		return err
	}
	for {
		if targets[entry.OwnerProcessID] {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr == nil {
				_, _ = windows.ResumeThread(thread)
				_ = windows.CloseHandle(thread)
			}
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return nil
			}
			return err
		}
	}
}

func claudeExecutablePath(identity Identity) string {
	return filepath.Clean(filepath.Join(identity.InstallLocation, "app", "Claude.exe"))
}

// packageProcesses lists only processes whose executable path belongs to the
// exact MSIX package discovered for Claude Desktop. Matching merely by image
// name would also catch Claude Code and unrelated applications. A process
// that exits or denies access while it is read is left out.
func packageProcesses(identity Identity) ([]packageProcess, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	expected := claudeExecutablePath(identity)
	var processes []packageProcess
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return processes, nil
		}
		return nil, err
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "claude.exe") {
			if process, ok := readPackageProcess(entry.ProcessID, entry.ParentProcessID, expected); ok {
				processes = append(processes, process)
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}
	return processes, nil
}

func readPackageProcess(pid, parent uint32, expectedExecutable string) (packageProcess, bool) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return packageProcess{}, false
	}
	defer windows.CloseHandle(process)

	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return packageProcess{}, false
	}
	if !strings.EqualFold(filepath.Clean(windows.UTF16ToString(buffer[:size])), expectedExecutable) {
		return packageProcess{}, false
	}
	commandLine, err := processCommandLine(process)
	if err != nil {
		return packageProcess{}, false
	}
	args, err := windows.DecomposeCommandLine(commandLine)
	if err != nil {
		return packageProcess{}, false
	}
	helper, dataDir := classifyArguments(args)
	return packageProcess{pid: pid, parent: parent, helper: helper, dataDir: dataDir}, true
}

// processCommandLine reads another process's command line through
// ProcessCommandLineInformation, which needs only
// PROCESS_QUERY_LIMITED_INFORMATION. Renderer command lines carry Desktop's
// managed configuration, so the buffer grows on demand up to 1 MiB.
func processCommandLine(process windows.Handle) (string, error) {
	buffer := make([]byte, 64<<10)
	for {
		var needed uint32
		err := windows.NtQueryInformationProcess(process, windows.ProcessCommandLineInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &needed)
		if err == nil {
			commandLine := (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0])).String()
			runtime.KeepAlive(buffer)
			return commandLine, nil
		}
		grow := errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || errors.Is(err, windows.STATUS_BUFFER_TOO_SMALL) || errors.Is(err, windows.STATUS_BUFFER_OVERFLOW)
		if !grow || int(needed) <= len(buffer) || needed > 1<<20 {
			return "", err
		}
		buffer = make([]byte, needed)
	}
}

// findClaudeTopLevelWindow returns the first visible top-level window of one
// process in z-order, which is the one the user last brought forward.
func findClaudeTopLevelWindow(pid uint32) windows.Handle {
	var found windows.Handle
	callback := windows.NewCallback(func(window uintptr, _ uintptr) uintptr {
		visible, _, _ := procClaudeIsWindowVisible.Call(window)
		if visible == 0 {
			return 1
		}
		var owner uint32
		_, _, _ = procClaudeGetWindowThreadProcess.Call(window, uintptr(unsafe.Pointer(&owner)))
		if owner != pid {
			return 1
		}
		found = windows.Handle(window)
		return 0
	})
	_, _, _ = procClaudeEnumWindows.Call(callback, 0)
	return found
}
