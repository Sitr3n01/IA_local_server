// Package main hosts the WebView2 operator console. This file is the
// portable bridge core: it decides whether a page request is a read or a
// mutation, and it is the one place that enforces ADR 0018 control 1 (every
// mutation requires a native confirmation outside the DOM). It has no
// Windows imports and no build tag so the ubuntu-latest "race" CI job can
// compile and run its tests with `go test -race ./...` the same as every
// other package in this module.
package main

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/sitr3n/local-ai-provider/internal/mcpadmin"
)

// Operation kinds. This is the explicit allowlist ADR 0018 requires: the
// bridge classifies a request by looking it up in readKinds/mutationKinds
// below, never by inspecting the string itself (no prefix or substring
// matching), so an operation kind that is not in either map is refused
// rather than defaulting into either bucket.
const (
	KindStatus = "status"
	KindLoad   = "load"
	KindUnload = "unload"
	KindSwitch = "switch"
	KindDrain  = "drain"
	KindResume = "resume"
)

// readKinds never require approval and never reach the AdminClient.
var readKinds = map[string]bool{
	KindStatus: true,
}

// mutationKinds always require Approver.Approve before the AdminClient is
// touched.
var mutationKinds = map[string]bool{
	KindLoad:   true,
	KindUnload: true,
	KindSwitch: true,
	KindDrain:  true,
	KindResume: true,
}

// modelIDRequired lists the mutations that must carry a non-empty model_id.
var modelIDRequired = map[string]bool{
	KindLoad:   true,
	KindUnload: true,
	KindSwitch: true,
}

// modelIDForbidden lists the mutations that must NOT carry a model_id. The
// administrative pipe protocol already refuses one on drain/resume; the
// bridge mirrors that instead of forwarding a malformed request and letting
// the pipe be the only thing that notices.
var modelIDForbidden = map[string]bool{
	KindDrain:  true,
	KindResume: true,
}

const maxModelIDBytes = 256

// Operation is a bridge request the page may ask the host to perform. It is
// decoded directly from the JSON web message the page posts, so every field
// is untrusted input.
type Operation struct {
	Kind    string `json:"kind"`
	ModelID string `json:"model_id,omitempty"`
}

// Approver decides whether a privileged Operation may proceed. The
// production implementation (main_windows.go) is a native Win32
// MB_OKCANCEL dialog owned by the host window, naming the operation and its
// target; it is the control that stands in for the second factor the
// credential-free bridge cannot provide (ADR 0018, control 1). Tests supply
// a double instead of a real dialog.
type Approver interface {
	Approve(Operation) bool
}

// AdminClient is the narrow slice of internal/mcpadmin.Client the bridge
// uses. internal/mcpadmin.Client satisfies this interface without any
// adapter, but the bridge never imports a concrete type wider than this, so
// nothing beyond Load/Unload/Switch/Drain/Resume is reachable through it.
type AdminClient interface {
	Load(ctx context.Context, modelID string) (mcpadmin.OperationOutput, error)
	Unload(ctx context.Context, modelID string) (mcpadmin.OperationOutput, error)
	Switch(ctx context.Context, modelID string) (mcpadmin.OperationOutput, error)
	Drain(ctx context.Context) (mcpadmin.MaintenanceOutput, error)
	Resume(ctx context.Context) (mcpadmin.MaintenanceOutput, error)
}

// StatusClient is the narrow read slice of internal/mcpserver.ControlClient
// the bridge uses for the credential-free status read.
//
// It reads StatusRaw, not Status. The bridge's job for a read is to carry
// cia-edge's snapshot to the page, which validates it against its own schema
// (frontend/src/api/schemas/status.ts) - so anything this host decodes into a
// Go struct on the way past is a field it can silently drop. mcpserver.Status
// declares only what that package's own MCP tools need, and routing the
// console's read through it did exactly that: 24652 bytes from cia-edge
// arrived at the page as 16645, missing uptime_seconds, runtimes, gpu_memory,
// maintenance, each model's display_name/capabilities, each model status's
// profile/runtime/checkpoints/context_tokens, and every physical and VRAM
// figure in capacity. The page's schema requires those, so the parse threw
// and the console rendered an error state while the bridge reported success.
type StatusClient interface {
	StatusRaw(ctx context.Context) (json.RawMessage, error)
}

// BridgeError is the stable, machine-readable error shape returned to the
// page. It mirrors internal/edge's {code, message} error envelope and
// mcpadmin.APIError's sanitized Code field: it never carries prompt text, a
// credential, or a raw upstream error body, and callers must never derive it
// by substring-matching an error's text.
type BridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Result is what the bridge hands back to the page for one Operation.
type Result struct {
	OK          bool                        `json:"ok"`
	Operation   string                      `json:"operation"`
	ModelID     string                      `json:"model_id,omitempty"`
	Status      json.RawMessage             `json:"status,omitempty"`
	Load        *mcpadmin.OperationOutput   `json:"result,omitempty"`
	Maintenance *mcpadmin.MaintenanceOutput `json:"maintenance,omitempty"`
	Error       *BridgeError                `json:"error,omitempty"`
}

// Bridge is the single decision point between the page's web messages and
// the two credential-free/DACL-protected clients. Nothing else in this
// program is permitted to call AdminClient directly.
type Bridge struct {
	approver     Approver
	adminClient  AdminClient
	statusClient StatusClient
}

// NewBridge builds a Bridge. A nil Approver fails closed: every mutation is
// refused rather than treated as approved.
func NewBridge(approver Approver, admin AdminClient, status StatusClient) *Bridge {
	return &Bridge{approver: approver, adminClient: admin, statusClient: status}
}

// Handle classifies and executes one Operation. It is the only entry point
// main_windows.go's WebMessageReceived wiring is allowed to call.
func (b *Bridge) Handle(ctx context.Context, op Operation) Result {
	switch {
	case readKinds[op.Kind]:
		return b.handleRead(ctx, op)
	case mutationKinds[op.Kind]:
		return b.handleMutation(ctx, op)
	default:
		return Result{Operation: op.Kind, Error: &BridgeError{
			Code:    "unknown_operation",
			Message: "operation is not recognized",
		}}
	}
}

func (b *Bridge) handleRead(ctx context.Context, op Operation) Result {
	if b.statusClient == nil {
		return Result{Operation: op.Kind, Error: &BridgeError{
			Code:    "status_unavailable",
			Message: "status is currently unavailable",
		}}
	}
	status, err := b.statusClient.StatusRaw(ctx)
	if err != nil {
		return Result{Operation: op.Kind, Error: &BridgeError{
			Code:    "status_unavailable",
			Message: "status is currently unavailable",
		}}
	}
	return Result{OK: true, Operation: op.Kind, Status: status}
}

// handleMutation validates the request shape first (cheap, local, no I/O and
// no human interruption for a request that is malformed regardless of who
// approves it), then requires approval, and only then reaches the
// AdminClient. A denied approval - or a validation failure - returns before
// the AdminClient is touched at all.
func (b *Bridge) handleMutation(ctx context.Context, op Operation) Result {
	modelID, verr := validateMutationModelID(op)
	if verr != nil {
		return Result{Operation: op.Kind, Error: verr}
	}

	if b.approver == nil || !b.approver.Approve(op) {
		return Result{Operation: op.Kind, ModelID: modelID, Error: &BridgeError{
			Code:    "approval_denied",
			Message: "the operation was not approved",
		}}
	}

	if b.adminClient == nil {
		return Result{Operation: op.Kind, ModelID: modelID, Error: &BridgeError{
			Code:    "administrative_operation_failed",
			Message: "administrative control operation failed",
		}}
	}

	switch op.Kind {
	case KindLoad:
		out, err := b.adminClient.Load(ctx, modelID)
		return mutationResult(op.Kind, modelID, out, err)
	case KindUnload:
		out, err := b.adminClient.Unload(ctx, modelID)
		return mutationResult(op.Kind, modelID, out, err)
	case KindSwitch:
		out, err := b.adminClient.Switch(ctx, modelID)
		return mutationResult(op.Kind, modelID, out, err)
	case KindDrain:
		out, err := b.adminClient.Drain(ctx)
		return maintenanceResult(op.Kind, out, err)
	case KindResume:
		out, err := b.adminClient.Resume(ctx)
		return maintenanceResult(op.Kind, out, err)
	default:
		// Unreachable: op.Kind was already confirmed to be in mutationKinds,
		// and validateMutationModelID covers every member of that map. Kept
		// as a fail-closed default rather than a panic.
		return Result{Operation: op.Kind, Error: &BridgeError{
			Code:    "unknown_operation",
			Message: "operation is not recognized",
		}}
	}
}

func mutationResult(kind, modelID string, out mcpadmin.OperationOutput, err error) Result {
	if err != nil {
		return Result{Operation: kind, ModelID: modelID, Error: classifyAdminError(err)}
	}
	return Result{OK: true, Operation: kind, ModelID: modelID, Load: &out}
}

func maintenanceResult(kind string, out mcpadmin.MaintenanceOutput, err error) Result {
	if err != nil {
		return Result{Operation: kind, Error: classifyAdminError(err)}
	}
	return Result{OK: true, Operation: kind, Maintenance: &out}
}

// classifyAdminError turns an AdminClient error into the stable,
// machine-readable code the page receives. It never returns the error's own
// text and never substring-matches it: an *mcpadmin.APIError already carries
// a sanitized Code (see internal/mcpadmin.operationError, which allowlists
// the character set and drops anything unsafe), so that is reused verbatim
// when present; anything else - including a transport failure that never
// reached cia-edge at all - collapses to one fixed, generic code.
func classifyAdminError(err error) *BridgeError {
	if apiErr, ok := asAPIError(err); ok && apiErr.Code != "" {
		return &BridgeError{Code: apiErr.Code, Message: "administrative control API returned an error"}
	}
	return &BridgeError{Code: "administrative_operation_failed", Message: "administrative control operation failed"}
}

// asAPIError reports whether err is (or wraps) an *mcpadmin.APIError,
// without ever looking at err.Error()'s text.
func asAPIError(err error) (*mcpadmin.APIError, bool) {
	type apiErrorUnwrapper interface {
		Unwrap() error
	}
	for err != nil {
		if apiErr, ok := err.(*mcpadmin.APIError); ok {
			return apiErr, true
		}
		unwrapper, ok := err.(apiErrorUnwrapper)
		if !ok {
			return nil, false
		}
		err = unwrapper.Unwrap()
	}
	return nil, false
}

// validateMutationModelID enforces the model_id shape rules for a mutation
// kind before any approval is requested: required and non-empty for
// load/unload/switch, and absent for drain/resume, mirroring what the
// administrative pipe protocol already refuses so the bridge does not
// forward a request the pipe would reject anyway.
// maxRequestIDBytes bounds the page-generated correlation id carried on
// every request envelope (see Envelope below). The id plays no role in
// Handle's own decision at all - it is opaque to the host, never an
// authorization token, and never used to look anything up - but it still
// crosses the trust boundary from the page like every other envelope
// field, so it gets the same bound-then-reject discipline
// internal/adminpipe applies to its own wire inputs (see
// internal/adminpipe.MaxModelIDBytes and ValidateModelID) rather than being
// assumed well-formed just because only this program's own frontend is
// expected to send it.
const maxRequestIDBytes = 128

// Envelope is the correlation wrapper the page's web message carries over
// window.chrome.webview.postMessage. ID is the one addition the native bridge
// work makes to the wire contract; Operation is embedded (not duplicated), so its own
// json tags apply unchanged and the wire shape stays flat:
// {"id": "...", "kind": "...", "model_id": "..."} - no nested object.
type Envelope struct {
	ID string `json:"id"`
	Operation
}

// ReplyEnvelope is the JSON object the host delivers back to the page for
// one request: the request's own id, echoed verbatim, alongside the Result
// Handle produced. ID is left empty (and omitted from the JSON via
// `omitempty`) exactly when no id could be trusted enough to echo - an
// envelope that failed to parse as JSON at all, or one whose own id failed
// validateRequestID - because echoing back an id the page never
// legitimately sent would let unrelated code on the page correlate a reply
// against a request it never made.
type ReplyEnvelope struct {
	ID string `json:"id,omitempty"`
	Result
}

// HandleMessage decodes one raw web message as an Envelope and dispatches
// it through Handle - the same single decision point every other caller
// uses, with no separate path to the Approver, AdminClient, or
// StatusClient - returning the ReplyEnvelope the host should deliver back
// to the page. This is the entire decode-validate-dispatch pipeline for the
// correlation id; main_windows.go's handleBridgeMessage only has to encode
// the result and hand it to Eval, which is not something this portable file
// can exercise directly (no Windows imports here - see the package doc
// comment).
func (b *Bridge) HandleMessage(ctx context.Context, raw []byte) ReplyEnvelope {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ReplyEnvelope{Result: Result{Error: &BridgeError{
			Code:    "invalid_request",
			Message: "request body must be a JSON object",
		}}}
	}

	id, idErr := validateRequestID(env.ID)
	if idErr != nil {
		// The id itself is missing or malformed: there is nothing
		// legitimate to correlate against, so the page gets a
		// clearly-shaped error with no id, rather than an id it never
		// actually sent.
		return ReplyEnvelope{Result: Result{Error: idErr}}
	}

	return ReplyEnvelope{ID: id, Result: b.Handle(ctx, env.Operation)}
}

// validateRequestID bounds the one envelope field Handle itself never sees.
// Like validateMutationModelID below, it fails closed: empty, oversized, or
// control-character-laden ids are all refused outright rather than passed
// through and hoped to be harmless.
func validateRequestID(id string) (string, *BridgeError) {
	if id == "" {
		return "", &BridgeError{Code: "invalid_request", Message: "request id is required"}
	}
	if len(id) > maxRequestIDBytes {
		return "", &BridgeError{Code: "invalid_request", Message: "request id exceeds the maximum length"}
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return "", &BridgeError{Code: "invalid_request", Message: "request id contains control characters"}
		}
	}
	return id, nil
}

func validateMutationModelID(op Operation) (string, *BridgeError) {
	switch {
	case modelIDRequired[op.Kind]:
		id := strings.TrimSpace(op.ModelID)
		if id == "" {
			return "", &BridgeError{Code: "model_id_required", Message: "model_id is required for this operation"}
		}
		if len(id) > maxModelIDBytes {
			return "", &BridgeError{Code: "model_id_invalid", Message: "model_id exceeds the maximum length"}
		}
		for _, r := range id {
			if unicode.IsControl(r) {
				return "", &BridgeError{Code: "model_id_invalid", Message: "model_id contains control characters"}
			}
		}
		return id, nil
	case modelIDForbidden[op.Kind]:
		if strings.TrimSpace(op.ModelID) != "" {
			return "", &BridgeError{Code: "model_id_not_permitted", Message: "model_id is not permitted for this operation"}
		}
		return "", nil
	default:
		// Reached only if mutationKinds ever grows a kind that neither list
		// above accounts for. Fail closed rather than silently permitting it.
		return "", &BridgeError{Code: "unknown_operation", Message: "operation is not recognized"}
	}
}
