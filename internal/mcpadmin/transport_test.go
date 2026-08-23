package mcpadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sitr3n/local-ai-provider/internal/adminpipe"
)

// The resolution rules below hold on every platform. Whether a *default* pipe
// name exists at all is platform-specific and is asserted separately, because
// the transport is deliberately absent where its DACL does not exist.
func TestAdminTransportFromEnvAppliesTheConfiguredPrecedence(t *testing.T) {
	t.Setenv("CIA_ADMIN_PIPE", "")
	t.Setenv("CIA_ADMIN_PIPE_SERVER", "")
	t.Setenv("CIA_ENVIRONMENT", "")

	_, server := AdminTransportFromEnv("http://127.0.0.1:18091")
	if !strings.HasSuffix(server, "cia-edge.exe") {
		t.Fatalf("default pipe server = %q, want the installed edge executable", server)
	}

	// An unrecognised control port must not be guessed into a deployment.
	if unknown, _ := AdminTransportFromEnv("http://127.0.0.1:9999"); unknown != "" {
		t.Fatalf("unknown control port resolved to pipe %q", unknown)
	}

	// Explicit configuration wins over any derivation, on any platform.
	t.Setenv("CIA_ADMIN_PIPE", `\\.\pipe\explicit-choice`)
	if explicit, _ := AdminTransportFromEnv("http://127.0.0.1:8091"); explicit != `\\.\pipe\explicit-choice` {
		t.Fatalf("explicit pipe was overridden: %q", explicit)
	}

	t.Setenv("CIA_ADMIN_PIPE", "off")
	if disabled, _ := AdminTransportFromEnv("http://127.0.0.1:8091"); disabled != "" {
		t.Fatalf("CIA_ADMIN_PIPE=off still resolved pipe %q", disabled)
	}

	t.Setenv("CIA_ADMIN_PIPE_SERVER", `D:\elsewhere\cia-edge.exe`)
	if _, overridden := AdminTransportFromEnv("http://127.0.0.1:8091"); overridden != `D:\elsewhere\cia-edge.exe` {
		t.Fatalf("explicit pipe server was overridden: %q", overridden)
	}
}

// A client configured for the pipe must fall back to the deprecated HTTP plane
// only when nothing is serving the pipe at all. That is the compatibility path
// for an installation that has not been redeployed yet.
func TestClientFallsBackToHTTPOnlyWhenNoPipeIsListening(t *testing.T) {
	var tokenReads atomic.Int64
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation := r.URL.Path[strings.LastIndex(r.URL.Path, ":")+1:]
		_, _ = fmt.Fprintf(w, `{"operation":%q,"model":"local-coding","status":"completed","active_model":"local-coding"}`, operation)
	}))
	defer control.Close()

	client, err := NewClient(Config{
		ControlURL:      control.URL,
		Timeout:         2 * time.Second,
		AdminPipe:       `\\.\pipe\cia-test-admin-absent-endpoint`,
		AdminPipeServer: `C:\IA\local-ai-v2\bin\cia-edge.exe`,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			tokenReads.Add(1)
			return "test-token", nil
		}),
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport() != "named-pipe" {
		t.Fatalf("configured client transport = %q, want named-pipe", client.Transport())
	}

	output, err := client.Load(context.Background(), "local-coding")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if output.Status != "completed" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if tokenReads.Load() != 1 {
		t.Fatalf("HTTP fallback read the credential %d times, want 1", tokenReads.Load())
	}
}

func TestFallbackIsRefusedForEveryFailureOtherThanAnAbsentPipe(t *testing.T) {
	if !fallbackPermitted(fmt.Errorf("wrapped: %w", adminpipe.ErrNotListening)) {
		t.Fatal("an absent pipe must permit the deprecated HTTP transport")
	}
	if !fallbackPermitted(adminpipe.ErrUnsupported) {
		t.Fatal("a platform without the transport must permit the deprecated HTTP transport")
	}
	for _, refusal := range []error{
		errors.New("the administrative pipe is served by an unexpected executable"),
		&adminpipe.Error{Code: "inference_busy"},
		errors.New("connect to the administrative pipe: Access is denied."),
	} {
		if fallbackPermitted(refusal) {
			t.Fatalf("failure %v must not permit an HTTP fallback that sends the bearer token", refusal)
		}
	}
}

func TestClientWithoutAPipeStaysOnTheDeprecatedTransport(t *testing.T) {
	client, err := NewClient(Config{
		ControlURL:    "http://127.0.0.1:8091",
		Timeout:       time.Second,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) { return "test-token", nil }),
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport() != "http-deprecated" {
		t.Fatalf("unconfigured client transport = %q", client.Transport())
	}
}

func TestClientRejectsAPipeWithoutAServerIdentity(t *testing.T) {
	_, err := NewClient(Config{
		ControlURL:    "http://127.0.0.1:8091",
		Timeout:       time.Second,
		AdminPipe:     `\\.\pipe\cia-local-ai-admin-final`,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) { return "test-token", nil }),
	}, "test")
	if err == nil {
		t.Fatal("a pipe client without an expected server executable was accepted")
	}
}
