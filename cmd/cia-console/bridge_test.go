package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sitr3n/local-ai-provider/internal/mcpadmin"
)

// recordedCall is one call the fake AdminClient observed. Tests assert
// against this slice directly - not just against Result - because the point
// of control 1 is that a denied mutation never reaches the pipe at all, and
// a return value alone cannot distinguish "refused before dialing" from "the
// fake happened to return an error."
type recordedCall struct {
	method  string
	modelID string
}

type fakeAdminClient struct {
	calls []recordedCall

	loadOut  mcpadmin.OperationOutput
	maintOut mcpadmin.MaintenanceOutput
	err      error
}

func (f *fakeAdminClient) Load(_ context.Context, modelID string) (mcpadmin.OperationOutput, error) {
	f.calls = append(f.calls, recordedCall{"load", modelID})
	return f.loadOut, f.err
}

func (f *fakeAdminClient) Unload(_ context.Context, modelID string) (mcpadmin.OperationOutput, error) {
	f.calls = append(f.calls, recordedCall{"unload", modelID})
	return f.loadOut, f.err
}

func (f *fakeAdminClient) Switch(_ context.Context, modelID string) (mcpadmin.OperationOutput, error) {
	f.calls = append(f.calls, recordedCall{"switch", modelID})
	return f.loadOut, f.err
}

func (f *fakeAdminClient) Drain(context.Context) (mcpadmin.MaintenanceOutput, error) {
	f.calls = append(f.calls, recordedCall{"drain", ""})
	return f.maintOut, f.err
}

func (f *fakeAdminClient) Resume(context.Context) (mcpadmin.MaintenanceOutput, error) {
	f.calls = append(f.calls, recordedCall{"resume", ""})
	return f.maintOut, f.err
}

// countingApprover records how many times Approve was called, so tests can
// assert a read never consults it at all - not merely that it returned true.
type countingApprover struct {
	approve bool
	calls   int
	lastOp  Operation
}

func (c *countingApprover) Approve(op Operation) bool {
	c.calls++
	c.lastOp = op
	return c.approve
}

type fakeStatusClient struct {
	called bool
	status json.RawMessage
	err    error
}

func (f *fakeStatusClient) StatusRaw(context.Context) (json.RawMessage, error) {
	f.called = true
	return f.status, f.err
}

// 1. A denied mutation returns a refusal AND the fake records zero calls.
func TestHandleDeniedMutationNeverReachesAdminClient(t *testing.T) {
	admin := &fakeAdminClient{}
	approver := &countingApprover{approve: false}
	status := &fakeStatusClient{}
	b := NewBridge(approver, admin, status)

	result := b.Handle(context.Background(), Operation{Kind: KindLoad, ModelID: "qwen3.6-35b"})

	if result.OK {
		t.Fatalf("expected a refusal, got an OK result: %+v", result)
	}
	if result.Error == nil || result.Error.Code != "approval_denied" {
		t.Fatalf("expected error code %q, got %+v", "approval_denied", result.Error)
	}
	if got := len(admin.calls); got != 0 {
		t.Fatalf("expected zero AdminClient calls for a denied mutation, got %d: %+v", got, admin.calls)
	}
	if approver.calls != 1 {
		t.Fatalf("expected the Approver to be consulted exactly once, got %d calls", approver.calls)
	}
}

// Denial must hold for every mutation kind, not only load.
func TestHandleDeniedMutationNeverReachesAdminClientForEveryKind(t *testing.T) {
	cases := []Operation{
		{Kind: KindLoad, ModelID: "m"},
		{Kind: KindUnload, ModelID: "m"},
		{Kind: KindSwitch, ModelID: "m"},
		{Kind: KindDrain},
		{Kind: KindResume},
	}
	for _, op := range cases {
		t.Run(op.Kind, func(t *testing.T) {
			admin := &fakeAdminClient{}
			approver := &countingApprover{approve: false}
			b := NewBridge(approver, admin, &fakeStatusClient{})

			result := b.Handle(context.Background(), op)

			if result.OK {
				t.Fatalf("expected a refusal, got an OK result: %+v", result)
			}
			if result.Error == nil || result.Error.Code != "approval_denied" {
				t.Fatalf("expected error code %q, got %+v", "approval_denied", result.Error)
			}
			if got := len(admin.calls); got != 0 {
				t.Fatalf("expected zero AdminClient calls, got %d: %+v", got, admin.calls)
			}
		})
	}
}

// 2. An approved mutation reaches the fake exactly once with the right
// arguments.
func TestHandleApprovedMutationReachesAdminClientOnce(t *testing.T) {
	admin := &fakeAdminClient{
		loadOut: mcpadmin.OperationOutput{
			Operation:   "load",
			Model:       "qwen3.6-35b",
			Status:      "completed",
			ActiveModel: "qwen3.6-35b",
		},
	}
	approver := &countingApprover{approve: true}
	b := NewBridge(approver, admin, &fakeStatusClient{})

	result := b.Handle(context.Background(), Operation{Kind: KindLoad, ModelID: "qwen3.6-35b"})

	if !result.OK {
		t.Fatalf("expected success, got %+v", result)
	}
	if result.Load == nil || *result.Load != admin.loadOut {
		t.Fatalf("expected the fake's output to be returned verbatim, got %+v", result.Load)
	}
	if len(admin.calls) != 1 {
		t.Fatalf("expected exactly one AdminClient call, got %d: %+v", len(admin.calls), admin.calls)
	}
	want := recordedCall{"load", "qwen3.6-35b"}
	if admin.calls[0] != want {
		t.Fatalf("expected call %+v, got %+v", want, admin.calls[0])
	}
	if approver.calls != 1 {
		t.Fatalf("expected the Approver to be consulted exactly once, got %d", approver.calls)
	}
	if approver.lastOp.Kind != KindLoad || approver.lastOp.ModelID != "qwen3.6-35b" {
		t.Fatalf("approver saw the wrong operation: %+v", approver.lastOp)
	}
}

// An approved drain/resume must reach the fake exactly once with no model id.
func TestHandleApprovedMaintenanceReachesAdminClientOnce(t *testing.T) {
	for _, kind := range []string{KindDrain, KindResume} {
		t.Run(kind, func(t *testing.T) {
			admin := &fakeAdminClient{maintOut: mcpadmin.MaintenanceOutput{State: "draining", Draining: true}}
			approver := &countingApprover{approve: true}
			b := NewBridge(approver, admin, &fakeStatusClient{})

			result := b.Handle(context.Background(), Operation{Kind: kind})

			if !result.OK {
				t.Fatalf("expected success, got %+v", result)
			}
			if len(admin.calls) != 1 {
				t.Fatalf("expected exactly one AdminClient call, got %d: %+v", len(admin.calls), admin.calls)
			}
			if admin.calls[0] != (recordedCall{kind, ""}) {
				t.Fatalf("expected call %+v, got %+v", recordedCall{kind, ""}, admin.calls[0])
			}
		})
	}
}

// 3. A read requires no approval and never consults the Approver.
func TestHandleReadNeverConsultsApprover(t *testing.T) {
	approver := &countingApprover{approve: false} // even a "would deny" approver must not be asked
	status := &fakeStatusClient{status: json.RawMessage(`{"service":"cia-edge","version":"test","ready":true}`)}
	admin := &fakeAdminClient{}
	b := NewBridge(approver, admin, status)

	result := b.Handle(context.Background(), Operation{Kind: KindStatus})

	if !result.OK {
		t.Fatalf("expected success, got %+v", result)
	}
	if !bytes.Contains(result.Status, []byte(`"service":"cia-edge"`)) {
		t.Fatalf("expected the fake's status bytes to be returned, got %s", result.Status)
	}
	if approver.calls != 0 {
		t.Fatalf("expected zero Approver calls for a read, got %d", approver.calls)
	}
	if !status.called {
		t.Fatalf("expected the StatusClient to be called")
	}
	if len(admin.calls) != 0 {
		t.Fatalf("expected zero AdminClient calls for a read, got %+v", admin.calls)
	}
}

// A failed read must not leak the underlying error text.
func TestHandleReadFailureIsSanitized(t *testing.T) {
	status := &fakeStatusClient{err: errors.New("dial tcp 127.0.0.1:8091: connect: connection refused")}
	b := NewBridge(&countingApprover{}, &fakeAdminClient{}, status)

	result := b.Handle(context.Background(), Operation{Kind: KindStatus})

	if result.OK {
		t.Fatalf("expected failure, got %+v", result)
	}
	if result.Error == nil || result.Error.Code != "status_unavailable" {
		t.Fatalf("expected error code %q, got %+v", "status_unavailable", result.Error)
	}
	if result.Error.Message == "" {
		t.Fatalf("expected a fixed message")
	}
}

// 4. Unknown kinds, empty model ids, and a model id supplied to
// drain/resume are all refused - fail-closed, and before the Approver is
// ever consulted, since none of this depends on who would approve it.
func TestHandleFailClosedValidation(t *testing.T) {
	cases := []struct {
		name     string
		op       Operation
		wantCode string
	}{
		{"unknown kind", Operation{Kind: "delete-everything"}, "unknown_operation"},
		{"empty kind", Operation{Kind: ""}, "unknown_operation"},
		{"empty model id on load", Operation{Kind: KindLoad, ModelID: ""}, "model_id_required"},
		{"whitespace model id on unload", Operation{Kind: KindUnload, ModelID: "   "}, "model_id_required"},
		{"empty model id on switch", Operation{Kind: KindSwitch}, "model_id_required"},
		{"control character in model id", Operation{Kind: KindLoad, ModelID: "qwen\x00"}, "model_id_invalid"},
		{"model id on drain", Operation{Kind: KindDrain, ModelID: "qwen3.6-35b"}, "model_id_not_permitted"},
		{"model id on resume", Operation{Kind: KindResume, ModelID: "qwen3.6-35b"}, "model_id_not_permitted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admin := &fakeAdminClient{}
			approver := &countingApprover{approve: true}
			b := NewBridge(approver, admin, &fakeStatusClient{})

			result := b.Handle(context.Background(), tc.op)

			if result.OK {
				t.Fatalf("expected a refusal, got an OK result: %+v", result)
			}
			if result.Error == nil || result.Error.Code != tc.wantCode {
				t.Fatalf("expected error code %q, got %+v", tc.wantCode, result.Error)
			}
			if got := len(admin.calls); got != 0 {
				t.Fatalf("expected zero AdminClient calls, got %d: %+v", got, admin.calls)
			}
			if approver.calls != 0 {
				t.Fatalf("expected zero Approver calls for fail-closed validation, got %d", approver.calls)
			}
		})
	}
}

// Status reads never touch the AdminClient even when unknown fields are set.
func TestHandleUnknownKindNeverConsultsApproverOrClients(t *testing.T) {
	admin := &fakeAdminClient{}
	approver := &countingApprover{approve: true}
	status := &fakeStatusClient{}
	b := NewBridge(approver, admin, status)

	result := b.Handle(context.Background(), Operation{Kind: "reboot-the-host", ModelID: "anything"})

	if result.Error == nil || result.Error.Code != "unknown_operation" {
		t.Fatalf("expected unknown_operation, got %+v", result.Error)
	}
	if approver.calls != 0 || len(admin.calls) != 0 || status.called {
		t.Fatalf("unknown kind touched a client: approver=%d admin=%v status=%v", approver.calls, admin.calls, status.called)
	}
}

// A nil Approver fails closed rather than panicking or defaulting to
// approved.
func TestHandleNilApproverFailsClosed(t *testing.T) {
	admin := &fakeAdminClient{}
	b := NewBridge(nil, admin, &fakeStatusClient{})

	result := b.Handle(context.Background(), Operation{Kind: KindLoad, ModelID: "qwen3.6-35b"})

	if result.OK {
		t.Fatalf("expected a refusal, got an OK result: %+v", result)
	}
	if result.Error == nil || result.Error.Code != "approval_denied" {
		t.Fatalf("expected error code %q, got %+v", "approval_denied", result.Error)
	}
	if len(admin.calls) != 0 {
		t.Fatalf("expected zero AdminClient calls, got %+v", admin.calls)
	}
}

// --- Envelope / HandleMessage: the bridge correlation id ------------------

// A malformed envelope (not valid JSON at all) gets a clearly-shaped error
// with no id to echo - there is nothing in it that could be trusted as the
// caller's own id.
func TestHandleMessageMalformedJSONHasNoID(t *testing.T) {
	b := NewBridge(&countingApprover{approve: true}, &fakeAdminClient{}, &fakeStatusClient{})

	reply := b.HandleMessage(context.Background(), []byte("not json"))

	if reply.ID != "" {
		t.Fatalf("expected no id to be echoed for unparseable JSON, got %q", reply.ID)
	}
	if reply.Error == nil || reply.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %+v", "invalid_request", reply.Error)
	}
}

// A well-formed JSON object with no id (or an empty one) is refused before
// Handle is ever consulted, and the id is not echoed back - there is no
// legitimate id to echo.
func TestHandleMessageMissingIDIsRejectedWithNoIDEchoed(t *testing.T) {
	admin := &fakeAdminClient{}
	approver := &countingApprover{approve: true}
	b := NewBridge(approver, admin, &fakeStatusClient{})

	reply := b.HandleMessage(context.Background(), []byte(`{"kind":"status"}`))

	if reply.ID != "" {
		t.Fatalf("expected no id to be echoed when the request carried none, got %q", reply.ID)
	}
	if reply.Error == nil || reply.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %+v", "invalid_request", reply.Error)
	}
	if approver.calls != 0 || len(admin.calls) != 0 {
		t.Fatalf("expected id validation to fail before any client is consulted: approver=%d admin=%v", approver.calls, admin.calls)
	}
}

// An id longer than maxRequestIDBytes is refused, fail-closed, before Handle
// is consulted - and is not echoed back.
func TestHandleMessageOversizedIDIsRejected(t *testing.T) {
	b := NewBridge(&countingApprover{approve: true}, &fakeAdminClient{}, &fakeStatusClient{})
	oversized := make([]byte, maxRequestIDBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	raw, err := json.Marshal(struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}{ID: string(oversized), Kind: KindStatus})
	if err != nil {
		t.Fatalf("marshal test request: %v", err)
	}

	reply := b.HandleMessage(context.Background(), raw)

	if reply.ID != "" {
		t.Fatalf("expected no id to be echoed for an oversized id, got a %d-byte id", len(reply.ID))
	}
	if reply.Error == nil || reply.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %+v", "invalid_request", reply.Error)
	}
}

// An id containing a control character is refused the same way an oversized
// one is.
func TestHandleMessageControlCharacterIDIsRejected(t *testing.T) {
	b := NewBridge(&countingApprover{approve: true}, &fakeAdminClient{}, &fakeStatusClient{})
	raw := []byte(`{"id":"abc` + "\x00" + `def","kind":"status"}`)

	reply := b.HandleMessage(context.Background(), raw)

	if reply.ID != "" {
		t.Fatalf("expected no id to be echoed for a control-character id, got %q", reply.ID)
	}
	if reply.Error == nil || reply.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %+v", "invalid_request", reply.Error)
	}
}

// A valid envelope for a read echoes the id back alongside the ordinary
// successful Result.
func TestHandleMessageValidStatusEchoesID(t *testing.T) {
	status := &fakeStatusClient{status: json.RawMessage(`{"service":"cia-edge","version":"test","ready":true}`)}
	b := NewBridge(&countingApprover{approve: false}, &fakeAdminClient{}, status)

	reply := b.HandleMessage(context.Background(), []byte(`{"id":"req-1","kind":"status"}`))

	if reply.ID != "req-1" {
		t.Fatalf("expected the request's id to be echoed back, got %q", reply.ID)
	}
	if !reply.OK || !bytes.Contains(reply.Status, []byte(`"service":"cia-edge"`)) {
		t.Fatalf("expected a successful status result, got %+v", reply.Result)
	}
}

// A denied mutation still echoes the caller's id, and still never reaches
// the AdminClient - proving the id-correlation plumbing introduces no path
// around Bridge.Handle's own approval gate. This is exercised without
// performing any real mutation: fakeAdminClient records calls instead of
// dialing anything.
func TestHandleMessageDeniedMutationEchoesIDAndNeverReachesAdminClient(t *testing.T) {
	admin := &fakeAdminClient{}
	approver := &countingApprover{approve: false}
	b := NewBridge(approver, admin, &fakeStatusClient{})

	reply := b.HandleMessage(context.Background(), []byte(`{"id":"req-2","kind":"load","model_id":"qwen3.6-35b"}`))

	if reply.ID != "req-2" {
		t.Fatalf("expected the request's id to be echoed back even on refusal, got %q", reply.ID)
	}
	if reply.OK {
		t.Fatalf("expected a refusal, got an OK result: %+v", reply.Result)
	}
	if reply.Error == nil || reply.Error.Code != "approval_denied" {
		t.Fatalf("expected error code %q, got %+v", "approval_denied", reply.Error)
	}
	if got := len(admin.calls); got != 0 {
		t.Fatalf("expected zero AdminClient calls for a denied mutation, got %d: %+v", got, admin.calls)
	}
	if approver.calls != 1 {
		t.Fatalf("expected the Approver to be consulted exactly once, got %d", approver.calls)
	}
}

// An approved mutation still echoes the caller's id, and reaches the
// AdminClient exactly once - proving HandleMessage's only path to the
// AdminClient is still through the Approver, id correlation included.
func TestHandleMessageApprovedMutationEchoesIDAndReachesAdminClientOnce(t *testing.T) {
	admin := &fakeAdminClient{loadOut: mcpadmin.OperationOutput{Operation: "load", Model: "qwen3.6-35b", Status: "completed"}}
	approver := &countingApprover{approve: true}
	b := NewBridge(approver, admin, &fakeStatusClient{})

	reply := b.HandleMessage(context.Background(), []byte(`{"id":"req-3","kind":"load","model_id":"qwen3.6-35b"}`))

	if reply.ID != "req-3" {
		t.Fatalf("expected the request's id to be echoed back, got %q", reply.ID)
	}
	if !reply.OK {
		t.Fatalf("expected success, got %+v", reply.Result)
	}
	if len(admin.calls) != 1 {
		t.Fatalf("expected exactly one AdminClient call, got %d: %+v", len(admin.calls), admin.calls)
	}
}

// The reply envelope is flat: "id" sits alongside the Result's own fields,
// not nested under a separate key - matching what bridgeTransport.ts (the
// page side) expects to parse.
func TestReplyEnvelopeIsFlatJSON(t *testing.T) {
	reply := ReplyEnvelope{ID: "abc123", Result: Result{OK: true, Operation: KindStatus, Status: json.RawMessage(`{"service":"cia-edge"}`)}}

	payload, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("marshal ReplyEnvelope: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(payload, &generic); err != nil {
		t.Fatalf("unmarshal into a generic map: %v", err)
	}
	if generic["id"] != "abc123" {
		t.Fatalf("expected top-level id %q, got %+v", "abc123", generic["id"])
	}
	if generic["ok"] != true {
		t.Fatalf("expected top-level ok=true, got %+v", generic["ok"])
	}
	if _, ok := generic["status"]; !ok {
		t.Fatalf("expected a top-level status field, got %+v", generic)
	}
}

// A successful reply with no id at all (the id was invalid, so ID is "")
// omits the "id" key entirely rather than sending an empty string - the
// page's transport should have nothing it could ever match against zero
// pending requests.
func TestReplyEnvelopeOmitsEmptyID(t *testing.T) {
	reply := ReplyEnvelope{Result: Result{Error: &BridgeError{Code: "invalid_request", Message: "request id is required"}}}

	payload, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("marshal ReplyEnvelope: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(payload, &generic); err != nil {
		t.Fatalf("unmarshal into a generic map: %v", err)
	}
	if _, ok := generic["id"]; ok {
		t.Fatalf("expected no top-level id key when the id could not be trusted, got %+v", generic)
	}
}

// An APIError's sanitized Code is surfaced verbatim; anything else collapses
// to one fixed, generic code so the page never sees raw upstream text.
func TestClassifyAdminErrorNeverLeaksRawText(t *testing.T) {
	apiErr := &mcpadmin.APIError{StatusCode: 409, Status: "409 Conflict", Code: "inference_busy"}
	got := classifyAdminError(apiErr)
	if got.Code != "inference_busy" {
		t.Fatalf("expected the sanitized APIError code to be preserved, got %+v", got)
	}

	generic := classifyAdminError(errors.New("dial tcp 127.0.0.1:8091: connect: connection refused"))
	if generic.Code != "administrative_operation_failed" {
		t.Fatalf("expected the generic fallback code, got %+v", generic)
	}
	if generic.Message == "" || generic.Message == "dial tcp 127.0.0.1:8091: connect: connection refused" {
		t.Fatalf("generic error must not leak the underlying error text, got %+v", generic)
	}
}
