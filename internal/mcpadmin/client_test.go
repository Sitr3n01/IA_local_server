package mcpadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientRejectsNonLoopback(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://127.0.0.1:8091",
		"http://localhost:8091",
		"http://192.168.1.10:8091",
		"http://example.com:8091",
		"http://user:pass@127.0.0.1:8091",
		"http://127.0.0.1:8091/control",
		"http://127.0.0.1:8091?token=secret",
	} {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(Config{
				ControlURL: rawURL,
				TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
					return "token", nil
				}),
			}, "test")
			if err == nil {
				t.Fatalf("NewClient(%q) succeeded, want error", rawURL)
			}
		})
	}
}

func TestClientOperationsAreAuthenticatedEmptyAndEscaped(t *testing.T) {
	t.Parallel()

	const (
		token   = "administrative-test-token"
		modelID = "family/model + q4"
	)
	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected Authorization header")
		}
		if r.ContentLength != 0 {
			t.Errorf("Content-Length = %d, want 0", r.ContentLength)
		}
		body := make([]byte, 1)
		if n, _ := r.Body.Read(body); n != 0 {
			t.Error("administrative request body is not empty")
		}
		if strings.Contains(r.RequestURI, "%252F") || !strings.Contains(strings.ToUpper(r.RequestURI), "%2F") {
			t.Errorf("model ID was not path-escaped exactly once: %q", r.RequestURI)
		}

		escapedPath := r.URL.EscapedPath()
		const prefix = "/api/v1/models/"
		if !strings.HasPrefix(escapedPath, prefix) {
			http.NotFound(w, r)
			return
		}
		remainder := strings.TrimPrefix(escapedPath, prefix)
		colon := strings.LastIndex(remainder, ":")
		if colon < 0 {
			http.NotFound(w, r)
			return
		}
		gotModel, err := url.PathUnescape(remainder[:colon])
		if err != nil || gotModel != modelID {
			t.Errorf("decoded model ID = %q, err=%v", gotModel, err)
		}
		operation := remainder[colon+1:]
		seen[operation]++
		active := modelID
		if operation == "unload" {
			active = ""
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"operation":%q,"model":%q,"status":"completed","active_model":%q}`, operation, modelID, active)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ControlURL: server.URL,
		Timeout:    time.Second,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return token, nil
		}),
	}, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	operations := []struct {
		name string
		call func(context.Context, string) (OperationOutput, error)
	}{
		{name: "load", call: client.Load},
		{name: "unload", call: client.Unload},
		{name: "switch", call: client.Switch},
	}
	for _, operation := range operations {
		output, err := operation.call(context.Background(), modelID)
		if err != nil {
			t.Fatalf("%s: %v", operation.name, err)
		}
		if output.Operation != operation.name || output.Model != modelID || output.Status != "completed" {
			t.Fatalf("%s: unexpected output: %+v", operation.name, output)
		}
	}
	for _, operation := range operations {
		if seen[operation.name] != 1 {
			t.Errorf("%s request count = %d", operation.name, seen[operation.name])
		}
	}
}

func TestClientDoesNotFollowRedirectOrExposeBody(t *testing.T) {
	t.Parallel()

	const sensitiveMessage = "must-never-be-returned"
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":load") {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusTemporaryRedirect)
			_, _ = fmt.Fprintf(w, `{"error":{"code":"inference_busy","message":%q}}`, sensitiveMessage)
			return
		}
		redirected.Add(1)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ControlURL: server.URL,
		Timeout:    time.Second,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return "secret-token", nil
		}),
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Load(context.Background(), "local-coding")
	if err == nil {
		t.Fatal("Load succeeded through redirect")
	}
	if redirected.Load() != 0 {
		t.Fatal("administrative client followed redirect")
	}
	if strings.Contains(err.Error(), sensitiveMessage) || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error exposed sensitive data: %v", err)
	}
}

func TestClientReturnsOnlySafeErrorCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "request-safe-123")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"error":{"code":"inference_busy","message":"private details"}}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ControlURL: server.URL,
		Timeout:    time.Second,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return "token", nil
		}),
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Switch(context.Background(), "local-coding")
	if err == nil || !strings.Contains(err.Error(), "inference_busy") {
		t.Fatalf("error = %v, want safe code", err)
	}
	if strings.Contains(err.Error(), "private details") {
		t.Fatalf("error exposed response message: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Code != "inference_busy" || apiErr.RequestID != "request-safe-123" {
		t.Fatalf("typed error = %#v, want safe status/code/request ID", apiErr)
	}
}

func TestOperationErrorDropsUnsafeMetadata(t *testing.T) {
	t.Parallel()
	err := operationError(http.StatusServiceUnavailable, "503 Service Unavailable", "token\r\nleak", []byte(`{"error":{"code":"bad code!","message":"private"}}`))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "" || apiErr.RequestID != "" || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe metadata retained: %#v / %v", apiErr, err)
	}
}

func TestNewClientAllowsColdLoadTimeout(t *testing.T) {
	t.Parallel()
	_, err := NewClient(Config{
		ControlURL: "http://127.0.0.1:18091",
		Timeout:    2 * time.Minute,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return "token", nil
		}),
	}, "test")
	if err != nil {
		t.Fatalf("cold-load timeout rejected: %v", err)
	}
}

func TestClientRejectsInvalidModelIDsBeforeRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, err := NewClient(Config{
		ControlURL: server.URL,
		Timeout:    time.Second,
		TokenProvider: TokenProviderFunc(func(context.Context) (string, error) {
			return "token", nil
		}),
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	for _, modelID := range []string{"", "   ", "model\nname", strings.Repeat("x", maxModelIDBytes+1)} {
		if _, err := client.Load(context.Background(), modelID); err == nil {
			t.Errorf("Load(%q) succeeded, want error", modelID)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid inputs caused %d requests", requests.Load())
	}
}
