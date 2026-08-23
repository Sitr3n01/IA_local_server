//go:build windows

package adminpipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// testPipeName keeps every test on its own endpoint. Pipe names are
// machine-global, so a fixed name would make parallel or repeated runs collide.
func testPipeName(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\cia-test-admin-%d-%s`, os.Getpid(), suffix)
}

func startListener(t *testing.T, name string, handler Handler) *Listener {
	t.Helper()
	listener, err := Listen(name)
	if err != nil {
		t.Fatalf("Listen(%s): %v", name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = listener.Serve(ctx, handler, nil)
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("administrative pipe listener did not stop")
		}
	})
	return listener
}

func TestPipeRoundTripAuthenticatesByDaclWithoutACredential(t *testing.T) {
	name := testPipeName(t, "roundtrip")
	handler := &recordingHandler{}
	listener := startListener(t, name, handler)

	// The DACL must be protected and must name this process's own account. The
	// listener reports the descriptor it applied so an installation check can
	// assert the same property without reimplementing it.
	sddl := listener.SecurityDescriptor()
	if !strings.HasPrefix(sddl, "D:P(") {
		t.Fatalf("pipe DACL is not protected: %s", sddl)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"(A;;FA;;;SY)", "(A;;FA;;;BA)", user.User.Sid.String()} {
		if !strings.Contains(sddl, required) {
			t.Fatalf("pipe DACL is missing %q: %s", required, sddl)
		}
	}

	// This process is the pipe server, so the expected executable is this test
	// binary. A client that pins the wrong one must be refused.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(DialOptions{Name: name, Timeout: 5 * time.Second, ExpectedServerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(context.Background(), Request{Operation: OperationLoad, ModelID: "local-coding"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Operation != OperationLoad || result.Model != "local-coding" || result.Status != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}

	maintenance, err := client.Execute(context.Background(), Request{Operation: OperationDrain})
	if err != nil {
		t.Fatalf("drain over the pipe: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(maintenance.Maintenance, &state); err != nil {
		t.Fatalf("decode maintenance payload: %v", err)
	}
	if state["state"] != "maintenance" {
		t.Fatalf("unexpected maintenance payload: %v", state)
	}
}

func TestPipeRefusesAnUnexpectedServerExecutable(t *testing.T) {
	name := testPipeName(t, "impostor")
	startListener(t, name, &recordingHandler{})

	client, err := NewClient(DialOptions{
		Name:               name,
		Timeout:            5 * time.Second,
		ExpectedServerPath: `C:\Windows\System32\notepad.exe`,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Execute(context.Background(), Request{Operation: OperationUnload, ModelID: "local-coding"})
	if err == nil {
		t.Fatal("a pipe served by an unexpected executable was accepted")
	}
	if !strings.Contains(err.Error(), "unexpected executable") {
		t.Fatalf("unexpected refusal reason: %v", err)
	}
	// A refused server must never be treated as "no pipe", because that is the
	// one condition under which a client is allowed to fall back to HTTP.
	if errors.Is(err, ErrNotListening) {
		t.Fatal("an impostor endpoint was reported as an absent pipe")
	}
}

func TestPipeNameCannotBeSquattedAfterTheListenerExists(t *testing.T) {
	name := testPipeName(t, "squat")
	startListener(t, name, &recordingHandler{})

	// FILE_FLAG_FIRST_PIPE_INSTANCE makes a second owner impossible while the
	// first listener holds the name.
	if second, err := Listen(name); err == nil {
		_ = second.Close()
		t.Fatal("a second listener claimed an endpoint that was already owned")
	}
}

func TestPipeDialReportsAnAbsentEndpointDistinctly(t *testing.T) {
	client, err := NewClient(DialOptions{
		Name:               testPipeName(t, "absent"),
		Timeout:            time.Second,
		ExpectedServerPath: `C:\IA\local-ai-v2\bin\cia-edge.exe`,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Execute(context.Background(), Request{Operation: OperationResume})
	if !errors.Is(err, ErrNotListening) {
		t.Fatalf("dial error = %v, want ErrNotListening", err)
	}
}

func TestPipeRejectsOversizedAndUnknownMessagesOverTheRealTransport(t *testing.T) {
	name := testPipeName(t, "bounds")
	handler := &recordingHandler{}
	startListener(t, name, handler)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// Raw dial, so the test can send bytes the typed client would refuse to
	// build: an oversized frame and an unknown verb.
	for _, probe := range []struct {
		name    string
		payload string
		code    string
	}{
		{"oversized", strings.Repeat("a", MaxMessageBytes+128), "message_too_large"},
		{"unknown", `{"operation":"shutdown"}` + "\n", "unknown_operation"},
		{"malformed", "not json\n", "invalid_request"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			conn, err := Dial(context.Background(), DialOptions{Name: name, Timeout: 5 * time.Second, ExpectedServerPath: executable})
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte(probe.payload)); err != nil {
				t.Fatalf("write probe: %v", err)
			}
			raw, err := readBoundedLine(conn)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			var response Response
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatalf("decode response %q: %v", raw, err)
			}
			if response.OK || response.Error == nil || response.Error.Code != probe.code {
				t.Fatalf("%s produced %+v, want code %q", probe.name, response, probe.code)
			}
		})
	}

	if len(handler.seen) != 0 {
		t.Fatalf("a refused message reached the handler: %+v", handler.seen)
	}

	// The endpoint stays usable after every refusal.
	client, err := NewClient(DialOptions{Name: name, Timeout: 5 * time.Second, ExpectedServerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Execute(context.Background(), Request{Operation: OperationSwitch, ModelID: "local-coding"}); err != nil {
		t.Fatalf("endpoint became unusable after refusals: %v", err)
	}
}

// The edge starts its listener on DefaultName. A default the listener's own
// validation refuses would stop the process from starting, and no test that
// builds a pipe name by hand can catch that - so this one uses the default.
func TestDefaultNameSatisfiesTheListener(t *testing.T) {
	for _, environment := range []string{"canary", "final"} {
		name := DefaultName(environment)
		if name == "" {
			t.Fatalf("DefaultName(%q) resolved nothing on Windows", environment)
		}
		if err := validatePipeName(name); err != nil {
			t.Fatalf("DefaultName(%q) = %q, which the listener refuses: %v", environment, name, err)
		}
		if !strings.HasPrefix(name, pipePrefix) {
			t.Fatalf("DefaultName(%q) = %q, which is not a local pipe path", environment, name)
		}
	}
	if DefaultName("canary") == DefaultName("final") {
		t.Fatal("both deployments resolved to the same default pipe")
	}
	for _, environment := range []string{"staging", "", "Canary"} {
		if name := DefaultName(environment); name != "" {
			t.Fatalf("DefaultName(%q) resolved %q; only the two pinned deployments have a pipe", environment, name)
		}
	}
}
