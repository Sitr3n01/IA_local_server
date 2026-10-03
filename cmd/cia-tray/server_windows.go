//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The server is the router and the edge, each run by its own scheduled task
// through cia-supervisor (ADR 0004). The tasks have no trigger of their own:
// the tray starts them when it opens and ends them on "Encerrar", so the one
// startup entry Task Manager shows decides whether IA Local runs at logon.

var (
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	oleaut32 = windows.NewLazySystemDLL("oleaut32.dll")

	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysAllocString   = oleaut32.NewProc("SysAllocString")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")

	clsidTaskScheduler = windows.GUID{Data1: 0x0F87369F, Data2: 0xA4E5, Data3: 0x4CFC, Data4: [8]byte{0xBD, 0x3E, 0x73, 0xE6, 0x15, 0x45, 0x72, 0xDD}}
	iidTaskService     = windows.GUID{Data1: 0x2FABA4C7, Data2: 0x4DA9, Data3: 0x4013, Data4: [8]byte{0x96, 0x97, 0x20, 0xCC, 0x3F, 0xD4, 0x0F, 0x85}}
)

const (
	clsctxInprocServer = 1
	sFalse             = syscall.Errno(1)

	// Vtable slots after IUnknown (0-2) and IDispatch (3-6), from taskschd.h.
	serviceGetFolder = 7
	serviceConnect   = 10
	folderGetTask    = 13
	taskGetState     = 9
	taskRun          = 12
	taskStop         = 23

	taskStateQueued  = 2
	taskStateRunning = 4

	// monitorSettle is how long a freshly started monitor is watched: one
	// that is still running after it serves the page; one that exits with an
	// error failed to start.
	monitorSettle = 3 * time.Second
)

// comObject is a COM interface pointer; its first word is the vtable.
type comObject struct {
	vtbl *[32]uintptr
}

func (o *comObject) call(slot int, args ...uintptr) error {
	result, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	if int32(result) < 0 {
		return syscall.Errno(uint32(result))
	}
	return nil
}

func (o *comObject) release() {
	if o != nil {
		_, _, _ = syscall.SyscallN(o.vtbl[2], uintptr(unsafe.Pointer(o)))
	}
}

// variant is an empty VARIANT; the ABI passes it by reference.
type variant struct {
	vt       uint16
	reserved [3]uint16
	value    [2]uintptr
}

func allocBSTR(value string) (uintptr, error) {
	encoded, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return 0, err
	}
	bstr, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(encoded)))
	if bstr == 0 {
		return 0, errors.New("allocate BSTR")
	}
	return bstr, nil
}

type windowsServerControl struct {
	environment string
	tasks       []string // start order: router, then edge
	monitor     string

	mu  sync.Mutex
	job windows.Handle
}

func newServerControl(environment, installRoot string) (serverControl, error) {
	var title string
	switch environment {
	case "canary":
		title = "Canary"
	case "final":
		title = "Final"
	default:
		return nil, fmt.Errorf("ambiente desconhecido: %q", environment)
	}
	prefix := "CIA Local AI v2 " + title + " "
	return &windowsServerControl{
		environment: environment,
		tasks:       []string{prefix + "Router", prefix + "Edge"},
		monitor:     filepath.Join(installRoot, "bin", "cia-monitor.exe"),
	}, nil
}

// Start runs each task that is not already running or queued.
func (s *windowsServerControl) Start(context.Context) error {
	return withTasks(s.tasks, func(name string, task *comObject) error {
		state, err := taskState(task)
		if err != nil {
			return fmt.Errorf("ler o estado da tarefa %q: %w", name, err)
		}
		if state == taskStateRunning || state == taskStateQueued {
			return nil
		}
		var params variant
		var running *comObject
		if err := task.call(taskRun, uintptr(unsafe.Pointer(&params)), uintptr(unsafe.Pointer(&running))); err != nil {
			return fmt.Errorf("iniciar a tarefa %q: %w", name, err)
		}
		running.release()
		return nil
	})
}

// Stop ends the monitor this tray started, then the edge and the router.
// cia-supervisor holds each component in a kill-on-close job, so ending a
// task ends its whole process tree.
func (s *windowsServerControl) Stop(context.Context) error {
	s.stopMonitor()
	reversed := make([]string, 0, len(s.tasks))
	for index := len(s.tasks) - 1; index >= 0; index-- {
		reversed = append(reversed, s.tasks[index])
	}
	return withTasks(reversed, func(name string, task *comObject) error {
		state, err := taskState(task)
		if err != nil {
			return fmt.Errorf("ler o estado da tarefa %q: %w", name, err)
		}
		if state != taskStateRunning && state != taskStateQueued {
			return nil
		}
		if err := task.call(taskStop, 0); err != nil {
			return fmt.Errorf("encerrar a tarefa %q: %w", name, err)
		}
		return nil
	})
}

func taskState(task *comObject) (int32, error) {
	var state int32
	err := task.call(taskGetState, uintptr(unsafe.Pointer(&state)))
	return state, err
}

// withTasks connects to the Task Scheduler on a COM-initialised thread and
// hands each named task in the root folder to use, in order.
func withTasks(names []string, use func(string, *comObject) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, sFalse) {
		return fmt.Errorf("inicializar COM: %w", err)
	}
	defer windows.CoUninitialize()

	var service *comObject
	if result, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskScheduler)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidTaskService)), uintptr(unsafe.Pointer(&service))); int32(result) < 0 || service == nil {
		return fmt.Errorf("abrir o Agendador de Tarefas: %w", syscall.Errno(uint32(result)))
	}
	defer service.release()
	var server, user, domain, password variant
	if err := service.call(serviceConnect, uintptr(unsafe.Pointer(&server)), uintptr(unsafe.Pointer(&user)),
		uintptr(unsafe.Pointer(&domain)), uintptr(unsafe.Pointer(&password))); err != nil {
		return fmt.Errorf("conectar ao Agendador de Tarefas: %w", err)
	}
	rootPath, err := allocBSTR(`\`)
	if err != nil {
		return err
	}
	defer procSysFreeString.Call(rootPath)
	var folder *comObject
	if err := service.call(serviceGetFolder, rootPath, uintptr(unsafe.Pointer(&folder))); err != nil {
		return fmt.Errorf("abrir a pasta de tarefas: %w", err)
	}
	defer folder.release()

	for _, name := range names {
		taskName, err := allocBSTR(name)
		if err != nil {
			return err
		}
		var task *comObject
		getErr := folder.call(folderGetTask, taskName, uintptr(unsafe.Pointer(&task)))
		_, _, _ = procSysFreeString.Call(taskName)
		if getErr != nil {
			// HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND)
			if errno, ok := getErr.(syscall.Errno); ok && uint32(errno) == 0x80070002 {
				return fmt.Errorf("a tarefa %q não está instalada; registre-a com Install-V2ScheduledTasks.ps1", name)
			}
			return fmt.Errorf("abrir a tarefa %q: %w", name, getErr)
		}
		useErr := use(name, task)
		task.release()
		if useErr != nil {
			return useErr
		}
	}
	return nil
}

// OpenPanel starts cia-monitor with --open. A monitor that already serves the
// page opens it and exits; a new one keeps serving it until the tray ends.
// The monitor runs in a job that closes with the tray; silent breakaway keeps
// the browser the monitor opens out of that job.
func (s *windowsServerControl) OpenPanel(ctx context.Context) error {
	info, err := os.Stat(s.monitor)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("o painel (cia-monitor.exe) não está instalado em %s", filepath.Dir(s.monitor))
	}
	job, err := s.monitorJob()
	if err != nil {
		return err
	}
	command := exec.Command(s.monitor, "--environment", s.environment, "--open")
	command.Dir = filepath.Dir(s.monitor)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		return fmt.Errorf("iniciar o painel: %w", err)
	}
	if process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid)); err == nil {
		_ = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			return fmt.Errorf("o painel não pôde iniciar (a porta pode estar em uso): %w", err)
		}
		return nil
	case <-time.After(monitorSettle):
		return nil
	case <-ctx.Done():
		return nil
	}
}

func (s *windowsServerControl) monitorJob() (windows.Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != 0 {
		return s.job, nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("criar o job do painel: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configurar o job do painel: %w", err)
	}
	s.job = job
	return job, nil
}

func (s *windowsServerControl) stopMonitor() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != 0 {
		_ = windows.CloseHandle(s.job)
		s.job = 0
	}
}
