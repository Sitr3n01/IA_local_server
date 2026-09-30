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

type windowsRestarter struct{}

func NewWindowsRestarter() Restarter { return windowsRestarter{} }

const swRestore = 9

var (
	claudeUser32                     = windows.NewLazySystemDLL("user32.dll")
	claudeShell32                    = windows.NewLazySystemDLL("shell32.dll")
	procClaudeEnumWindows            = claudeUser32.NewProc("EnumWindows")
	procClaudeGetWindowThreadProcess = claudeUser32.NewProc("GetWindowThreadProcessId")
	procClaudeIsWindowVisible        = claudeUser32.NewProc("IsWindowVisible")
	procClaudePostMessage            = claudeUser32.NewProc("PostMessageW")
	procClaudeSetForegroundWindow    = claudeUser32.NewProc("SetForegroundWindow")
	procClaudeShowWindow             = claudeUser32.NewProc("ShowWindow")
	procClaudeShellExecute           = claudeShell32.NewProc("ShellExecuteW")
)

const swShowNormal = 1

// LaunchWindows opens the installed Claude Desktop MSIX without terminating an
// existing instance or changing either the 1P or isolated 3P configuration. It
// also restores and foregrounds an existing Claude window so tray activation is
// observable instead of silently succeeding in the background.
func LaunchWindows(ctx context.Context, identity Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := activateWindowsApplication(identity.AppUserModelID); err != nil {
		return fmt.Errorf("activate Claude Desktop MSIX: %w", err)
	}
	return foregroundClaudeWindow(ctx, identity)
}

// activateWindowsApplication asks the interactive Windows shell to activate the
// AUMID directly. Starting explorer.exe as a child process can return before the
// shell namespace activation is handed off and has left this FullTrustApplication
// suspended after package updates.
func activateWindowsApplication(appUserModelID string) error {
	operation, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString("shell:AppsFolder\\" + appUserModelID)
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

func foregroundClaudeWindow(ctx context.Context, identity Identity) error {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resumeAttempted := false
	for {
		pids, err := claudeProcessIDs(identity)
		if err == nil && len(pids) > 0 {
			if window := findClaudeTopLevelWindow(pids); window != 0 {
				_, _, _ = procClaudeShowWindow.Call(uintptr(window), swRestore)
				_, _, _ = procClaudeSetForegroundWindow.Call(uintptr(window))
				return nil
			}
			// The Store build can occasionally leave its FullTrustApplication
			// main thread at the initial suspend count after AUMID activation.
			// Resume each thread at most once and only for processes whose exact
			// executable belongs to the discovered Claude package.
			if !resumeAttempted {
				resumeAttempted = true
				_ = resumeClaudeThreads(pids)
			}
		}
		select {
		case <-waitCtx.Done():
			return errors.New("the Claude Desktop app did not create a visible window within 10 seconds")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func resumeClaudeThreads(pids map[uint32]struct{}) error {
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
		if _, ok := pids[entry.OwnerProcessID]; ok {
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

// claudeProcessIDs returns only processes whose executable path belongs to the
// exact MSIX package discovered for Claude Desktop. Matching merely by image
// name would also catch Claude Code and unrelated applications.
func claudeProcessIDs(identity Identity) (map[uint32]struct{}, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	expected := claudeExecutablePath(identity)
	pids := make(map[uint32]struct{})
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return pids, nil
		}
		return nil, err
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "claude.exe") {
			if path, pathErr := processExecutablePath(entry.ProcessID); pathErr == nil && strings.EqualFold(filepath.Clean(path), expected) {
				pids[entry.ProcessID] = struct{}{}
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}
	return pids, nil
}

func processExecutablePath(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func findClaudeTopLevelWindow(pids map[uint32]struct{}) windows.Handle {
	var found windows.Handle
	callback := windows.NewCallback(func(window uintptr, _ uintptr) uintptr {
		visible, _, _ := procClaudeIsWindowVisible.Call(window)
		if visible == 0 {
			return 1
		}
		var pid uint32
		_, _, _ = procClaudeGetWindowThreadProcess.Call(window, uintptr(unsafe.Pointer(&pid)))
		if _, ok := pids[pid]; !ok {
			return 1
		}
		found = windows.Handle(window)
		return 0
	})
	_, _, _ = procClaudeEnumWindows.Call(callback, 0)
	return found
}

func (windowsRestarter) Restart(ctx context.Context, identity Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := stopClaudeDesktop(ctx, identity); err != nil {
		return err
	}
	return LaunchWindows(ctx, identity)
}

const wmClose = 0x0010

func stopClaudeDesktop(ctx context.Context, identity Identity) error {
	pids, err := claudeProcessIDs(identity)
	if err != nil {
		return fmt.Errorf("enumerate Claude Desktop processes: %w", err)
	}
	if len(pids) == 0 {
		return nil
	}
	for _, window := range findClaudeTopLevelWindows(pids) {
		_, _, _ = procClaudePostMessage.Call(uintptr(window), wmClose, 0, 0)
	}
	if waitForClaudeExit(ctx, identity, 3*time.Second) {
		return nil
	}

	// Some Desktop configurations keep Electron resident after WM_CLOSE. The
	// fallback is still narrowly scoped to executable paths from this exact
	// package, never to every process named claude.exe.
	pids, err = claudeProcessIDs(identity)
	if err != nil {
		return fmt.Errorf("re-enumerate Claude Desktop processes: %w", err)
	}
	for pid := range pids {
		process, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if openErr != nil {
			continue
		}
		_ = windows.TerminateProcess(process, 0)
		_ = windows.CloseHandle(process)
	}
	if !waitForClaudeExit(ctx, identity, 5*time.Second) {
		return errors.New("the Claude Desktop package processes did not exit")
	}
	return nil
}

func waitForClaudeExit(ctx context.Context, identity Identity, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		pids, err := claudeProcessIDs(identity)
		if err == nil && len(pids) == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func findClaudeTopLevelWindows(pids map[uint32]struct{}) []windows.Handle {
	var found []windows.Handle
	callback := windows.NewCallback(func(window uintptr, _ uintptr) uintptr {
		visible, _, _ := procClaudeIsWindowVisible.Call(window)
		if visible == 0 {
			return 1
		}
		var pid uint32
		_, _, _ = procClaudeGetWindowThreadProcess.Call(window, uintptr(unsafe.Pointer(&pid)))
		if _, ok := pids[pid]; ok {
			found = append(found, windows.Handle(window))
		}
		return 1
	})
	_, _, _ = procClaudeEnumWindows.Call(callback, 0)
	return found
}

// GatewayVerifier confirms the selected local profile is byte-safe and that
// the actual CIA edge accepts its separate gateway credential. It sends no
// prompt, model request, or data beyond GET /v1/models.
type GatewayVerifier struct {
	paths    Paths
	identity Identity
}

func NewGatewayVerifier(paths Paths, identity Identity) (*GatewayVerifier, error) {
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return &GatewayVerifier{paths: paths, identity: identity}, nil
}

func (v *GatewayVerifier) Verify(ctx context.Context, mode Mode, gateway Gateway) error {
	modeDocument, err := readJSONObject(v.paths.modePath())
	if err != nil {
		return err
	}
	expected := "1p"
	if mode == ModeLocal {
		expected = "3p"
	}
	if current, _ := modeDocument["deploymentMode"].(string); current != expected {
		return fmt.Errorf("isolated Claude Desktop deployment mode is %q, want %q", current, expected)
	}
	if err := waitForRuntimeMode(ctx, v.identity, expected); err != nil {
		return err
	}
	if mode != ModeLocal {
		return nil
	}
	profile, err := readJSONObject(v.paths.profilePath())
	if err != nil {
		return err
	}
	for key, wanted := range map[string]string{
		"inferenceProvider":          "gateway",
		"inferenceCredentialKind":    "static",
		"inferenceGatewayBaseUrl":    gateway.BaseURL,
		"inferenceGatewayApiKey":     gateway.APIKey,
		"inferenceGatewayAuthScheme": "bearer",
	} {
		if actual, _ := profile[key].(string); actual != wanted {
			return fmt.Errorf("isolated Claude Desktop profile has unexpected %s", key)
		}
	}
	meta, err := readJSONObject(v.paths.metaPath())
	if err != nil {
		return err
	}
	if applied, _ := meta["appliedId"].(string); applied != profileID {
		return errors.New("CIA Claude Desktop profile is not selected")
	}

	return ProbeGateway(ctx, gateway)
}

func waitForRuntimeMode(ctx context.Context, identity Identity, expected string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if runtimeHasDeploymentMode(waitCtx, identity, expected) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("the Claude Desktop runtime did not report deployment mode %q", expected)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func runtimeHasDeploymentMode(ctx context.Context, identity Identity, expected string) bool {
	const command = `$ErrorActionPreference='Stop'; $hit=Get-CimInstance Win32_Process -Filter "Name='claude.exe'" | Where-Object { $_.ExecutablePath -eq $env:CIA_CLAUDE_EXPECTED_EXE -and $_.CommandLine -like '*--type=renderer*' -and $_.CommandLine.Contains(('\"deploymentMode\":\"'+$env:CIA_CLAUDE_EXPECTED_MODE+'\"')) } | Select-Object -First 1; if($null -eq $hit){exit 44}`
	check := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command)
	check.Env = append(os.Environ(),
		"CIA_CLAUDE_EXPECTED_EXE="+claudeExecutablePath(identity),
		"CIA_CLAUDE_EXPECTED_MODE="+expected,
	)
	return check.Run() == nil
}
