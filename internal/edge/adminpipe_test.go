package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Sitr3n01/local-ai-provider/internal/adminpipe"
)

// pipeExchange runs one administrative request through the edge's real handler
// using the pipe framing, without needing the Windows transport. It is the same
// code path the named pipe serves.
type pipeExchange struct {
	in  *strings.Reader
	out bytes.Buffer
}

func (p *pipeExchange) Read(b []byte) (int, error)  { return p.in.Read(b) }
func (p *pipeExchange) Write(b []byte) (int, error) { return p.out.Write(b) }

func adminExchange(t *testing.T, server *Server, request adminpipe.Request) adminpipe.Response {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	exchange := &pipeExchange{in: strings.NewReader(string(encoded) + "\n")}
	if err := adminpipe.ServeConn(context.Background(), exchange, server.AdminHandler()); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	var response adminpipe.Response
	if err := json.Unmarshal(bytes.TrimSpace(exchange.out.Bytes()), &response); err != nil {
		t.Fatalf("decode administrative response %q: %v", exchange.out.String(), err)
	}
	return response
}

func TestAdminPipeHandlerSharesTheHTTPAdmissionPolicy(t *testing.T) {
	hold := make(chan struct{})
	server, _ := newTestServer(t, maintenanceBackend(hold))
	server.memoryStatus = fixedMemory(32, 24)

	// An unknown model is refused with the same code the HTTP plane returns.
	absent := adminExchange(t, server, adminpipe.Request{Operation: adminpipe.OperationLoad, ModelID: "not-in-the-manifest"})
	if absent.OK || absent.Error == nil || absent.Error.Code != "model_not_found" {
		t.Fatalf("unknown model over the pipe produced %+v", absent)
	}

	// While a request is active, model control is refused rather than allowed to
	// interrupt it — the same 409 policy as the HTTP transport.
	body := []byte(`{"model":"local-coding","messages":[{"role":"user","content":"hi"}]}`)
	done := make(chan int, 1)
	go func() {
		done <- dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", body).Code
	}()
	deadline := time.Now().Add(3 * time.Second)
	for server.gate.snapshot().Active != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request never became active")
		}
		time.Sleep(2 * time.Millisecond)
	}
	busy := adminExchange(t, server, adminpipe.Request{Operation: adminpipe.OperationUnload, ModelID: "local-coding"})
	if busy.OK || busy.Error == nil || busy.Error.Code != "inference_busy" || busy.Error.HTTPStatus != http.StatusConflict {
		t.Fatalf("unload during an active request produced %+v", busy)
	}

	close(hold)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("active request finished with %d", code)
	}

	unloaded := adminExchange(t, server, adminpipe.Request{Operation: adminpipe.OperationUnload, ModelID: "local-coding"})
	if !unloaded.OK || unloaded.Result == nil || unloaded.Result.Status != "completed" {
		t.Fatalf("unload after the request finished produced %+v", unloaded)
	}
}

func TestAdminPipeDrainAndResumeDriveTheSameGate(t *testing.T) {
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)

	drained := adminExchange(t, server, adminpipe.Request{Operation: adminpipe.OperationDrain})
	if !drained.OK || drained.Result == nil {
		t.Fatalf("drain over the pipe produced %+v", drained)
	}
	var state maintenanceSnapshot
	if err := json.Unmarshal(drained.Result.Maintenance, &state); err != nil {
		t.Fatalf("decode maintenance payload: %v", err)
	}
	if state.State != maintenanceDrained {
		t.Fatalf("drain over the pipe reported %+v", state)
	}
	if refused := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", []byte(`{"model":"local-coding","messages":[]}`)); refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("inference after a pipe drain status = %d, want 503", refused.Code)
	}

	resumed := adminExchange(t, server, adminpipe.Request{Operation: adminpipe.OperationResume})
	if !resumed.OK || resumed.Result == nil {
		t.Fatalf("resume over the pipe produced %+v", resumed)
	}
	if server.gate.maintenance(time.Now()).Draining {
		t.Fatal("resume over the pipe did not clear the drain")
	}
}

func TestAdminPipeResponsesCarryNoCredential(t *testing.T) {
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)

	for _, request := range []adminpipe.Request{
		{Operation: adminpipe.OperationDrain},
		{Operation: adminpipe.OperationResume},
		{Operation: adminpipe.OperationLoad, ModelID: "local-coding"},
		{Operation: adminpipe.OperationLoad, ModelID: "absent"},
	} {
		exchange := &pipeExchange{in: strings.NewReader(mustJSON(t, request) + "\n")}
		if err := adminpipe.ServeConn(context.Background(), exchange, server.AdminHandler()); err != nil {
			t.Fatalf("ServeConn: %v", err)
		}
		body := exchange.out.String()
		for _, forbidden := range []string{testAdminToken, testInferenceToken, testRouterToken, "Bearer", "router-api-key"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("administrative response leaked %q: %s", forbidden, body)
			}
		}
	}
}

func TestHTTPAdminMutationsAreMarkedDeprecated(t *testing.T) {
	server, _ := newTestServer(t, maintenanceBackend(nil))
	server.memoryStatus = fixedMemory(32, 24)
	control := server.ControlHandler()

	recorder := controlRequest(t, control, http.MethodPost, "/api/v1/maintenance:drain", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("drain status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("X-CIA-Admin-Transport"); got != "http-deprecated" {
		t.Fatalf("HTTP administrative mutation transport header = %q", got)
	}
	if server.metrics.httpAdminMutations.Load() != 1 {
		t.Fatalf("HTTP administrative mutation was not counted: %d", server.metrics.httpAdminMutations.Load())
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
