//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWithTasksNamesATaskThatIsNotInstalled(t *testing.T) {
	called := false
	err := withTasks([]string{"CIA Local AI v2 Test Task That Does Not Exist"}, func(string, *comObject) error {
		called = true
		return nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "não está instalada") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

// Reading the installed tasks' state changes nothing, but it needs them, so
// it runs only on a machine that has them.
func TestWithTasksReadsTheInstalledCanaryTasks(t *testing.T) {
	if os.Getenv("CIA_TRAY_LIVE_TASKS") == "" {
		t.Skip("set CIA_TRAY_LIVE_TASKS=1 to read the installed canary tasks")
	}
	control, err := newServerControl("canary", `C:\IA\local-ai-v2`)
	if err != nil {
		t.Fatal(err)
	}
	server := control.(*windowsServerControl)
	err = withTasks(server.tasks, func(name string, task *comObject) error {
		state, err := taskState(task)
		t.Logf("%s: state %d", name, state)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Start and Stop run against a disposable task that only sleeps in wscript,
// which has no window; the test registers it and deletes it again.
func TestStartAndStopADisposableTask(t *testing.T) {
	if os.Getenv("CIA_TRAY_LIVE_TASKS") == "" {
		t.Skip("set CIA_TRAY_LIVE_TASKS=1 to register, run and delete a disposable task")
	}
	script := filepath.Join(t.TempDir(), "sleep.vbs")
	if err := os.WriteFile(script, []byte("WScript.Sleep 120000\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	schtasks := filepath.Join(system, "schtasks.exe")
	name := fmt.Sprintf("CIA Tray Test %d", time.Now().UnixNano())
	action := fmt.Sprintf(`"%s" "%s"`, filepath.Join(system, "wscript.exe"), script)
	if output, err := exec.Command(schtasks, "/Create", "/TN", name, "/TR", action, "/SC", "ONCE", "/ST", "23:59", "/F").CombinedOutput(); err != nil {
		t.Fatalf("create task: %v %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(schtasks, "/Delete", "/TN", name, "/F").Run() })

	state := func() int32 {
		var value int32
		if err := withTasks([]string{name}, func(_ string, task *comObject) error {
			var err error
			value, err = taskState(task)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return value
	}
	waitFor := func(want int32) {
		deadline := time.Now().Add(10 * time.Second)
		for state() != want {
			if time.Now().After(deadline) {
				t.Fatalf("task state %d, want %d", state(), want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	control := &windowsServerControl{tasks: []string{name}}
	ctx := context.Background()
	for range 2 { // the second call finds the task running and leaves it
		if err := control.Start(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(taskStateRunning)
	}
	for range 2 { // the second call finds nothing to stop
		if err := control.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(3) // TASK_STATE_READY
	}
}

func TestNewServerControlRejectsAnUnknownEnvironment(t *testing.T) {
	if _, err := newServerControl("staging", `C:\IA\local-ai-v2`); err == nil {
		t.Fatal("unknown environment accepted")
	}
	control, err := newServerControl("final", `C:\IA\local-ai-v2`)
	if err != nil {
		t.Fatal(err)
	}
	server := control.(*windowsServerControl)
	if server.tasks[0] != "CIA Local AI v2 Final Router" || server.tasks[1] != "CIA Local AI v2 Final Edge" {
		t.Fatalf("tasks=%v", server.tasks)
	}
	if server.monitor != `C:\IA\local-ai-v2\bin\cia-monitor.exe` {
		t.Fatalf("monitor=%s", server.monitor)
	}
}
