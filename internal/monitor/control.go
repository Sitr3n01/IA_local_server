package monitor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sitr3n01/local-ai-provider/internal/adminpipe"
)

// The monitor exposes exactly two mutations: switch to a model, and unload the
// loaded one. Both travel over the edge's DACL-protected administrative pipe,
// which the operating system authenticates, so the monitor still holds no
// credential. What it adds is a route from a browser page to that pipe, and
// every control in this file and in server.go exists to keep that route from
// being usable by anything but the operator in front of the machine:
//
//   - the request must come from this page, in this user's browser
//     (server.go: sameOrigin, peer check);
//   - the operator must confirm it in a native dialog the page cannot reach,
//     which expires on its own (approve_windows.go) - ADR 0018's control 1;
//   - the model must be one the edge itself lists, and only one operation may
//     be pending at a time.
//
// Drain and resume are deliberately absent: they are release-transaction
// verbs, not something a status page should be one click away from.
const (
	actionSwitch = adminpipe.OperationSwitch
	actionUnload = adminpipe.OperationUnload
	// actionStopSource ends the process that holds a model another tool loaded.
	// It does not travel over the pipe: those models are not the edge's, and the
	// edge cannot unload them (see terminate.go).
	actionStopSource = "stop_source"

	// operationTimeout bounds the pipe exchange. A switch returns only once the
	// new model has loaded, and the largest profiles take minutes to map.
	operationTimeout = 5 * time.Minute
)

// Operation states, in order.
const (
	stateAwaitingConfirmation = "awaiting_confirmation"
	stateRunning              = "running"
	stateSucceeded            = "succeeded"
	stateFailed               = "failed"
	stateDeclined             = "declined"
	stateExpired              = "expired"
)

// ControlOptions says how the monitor reaches the administrative pipe. An empty
// Pipe disables the controls.
type ControlOptions struct {
	Pipe   string
	Server string
}

type approval int

const (
	approvalDeclined approval = iota
	approvalGranted
	approvalExpired
)

// approvalRequest is what the confirmation dialog shows. Every string in it
// comes from the edge's status, which binds model IDs and names to the manifest.
type approvalRequest struct {
	Environment string
	Action      string
	Model       string
	DisplayName string
	Replaces    string
	// Target and Detail describe the process a stop would end: its name and pid,
	// and what it holds. DisplayName is then the tool's name.
	Target string
	Detail string
}

type approver interface {
	approve(approvalRequest) approval
}

type adminExecutor interface {
	Execute(ctx context.Context, request adminpipe.Request) (adminpipe.Result, error)
}

type operationView struct {
	ID          string `json:"id"`
	Action      string `json:"action"`
	Model       string `json:"model"`
	Target      string `json:"target,omitempty"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	RequestedAt string `json:"requested_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

type controlView struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// StopAvailable is whether the page may offer to end another tool's model
	// process. It does not depend on the edge, and follows the same switch that
	// turns every control off.
	StopAvailable bool           `json:"stop_available"`
	Operation     *operationView `json:"operation,omitempty"`
}

// actionError is a refusal made before anything was attempted. Code is one of
// the fixed strings below; the page maps it to a sentence.
type actionError struct {
	Status int
	Code   string
}

type controller struct {
	environment string
	reason      string
	executor    adminExecutor
	approver    approver
	status      func() *edgeStatus
	refresh     func()
	now         func() time.Time
	log         func(event string, fields map[string]any)

	// Ending another tool's process: the sources discovery found, the means of
	// ending one, and the monitor's own pid, which is never a target.
	sources    func() []inferenceSource
	terminator processTerminator
	selfPID    uint32

	mu      sync.Mutex
	nextID  uint64
	pending bool
	current *operationView
}

func (c *controller) view() controlView {
	if c == nil {
		return controlView{Reason: "disabled"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := controlView{
		Available:     c.reason == "",
		Reason:        c.reason,
		StopAvailable: c.reason != "disabled" && c.terminator != nil && c.sources != nil,
	}
	if c.current != nil {
		operation := *c.current
		out.Operation = &operation
	}
	return out
}

// start validates a request and, if it may proceed, begins it in the
// background: the confirmation dialog blocks for up to approvalTimeout and the
// operation itself for minutes, and neither should hold an HTTP request open.
func (c *controller) start(action, model string) (operationView, *actionError) {
	if c == nil || c.reason != "" {
		return operationView{}, &actionError{Status: 503, Code: "controls_unavailable"}
	}
	if action != actionSwitch && action != actionUnload {
		return operationView{}, &actionError{Status: 400, Code: "unknown_action"}
	}
	modelID, err := adminpipe.ValidateModelID(model)
	if err != nil || modelID != model {
		return operationView{}, &actionError{Status: 400, Code: "invalid_model"}
	}
	status := c.status()
	if status == nil {
		return operationView{}, &actionError{Status: 503, Code: "status_unavailable"}
	}
	displayName, listed := rosterName(status, modelID)
	if !listed {
		return operationView{}, &actionError{Status: 404, Code: "unknown_model"}
	}
	switch {
	case action == actionSwitch && status.ActiveModel == modelID:
		return operationView{}, &actionError{Status: 409, Code: "model_already_loaded"}
	case action == actionUnload && status.ActiveModel != modelID:
		return operationView{}, &actionError{Status: 409, Code: "model_not_loaded"}
	case status.Gate.Active > 0 || status.Gate.Queued > 0:
		// The edge refuses control while inference holds the gate. Saying so
		// here spares the operator a dialog whose answer cannot matter.
		return operationView{}, &actionError{Status: 409, Code: "inference_busy"}
	}
	replaces := ""
	if action == actionSwitch && status.ActiveModel != "" {
		replaces, _ = rosterName(status, status.ActiveModel)
	}

	c.mu.Lock()
	if c.pending {
		c.mu.Unlock()
		return operationView{}, &actionError{Status: 409, Code: "operation_in_progress"}
	}
	c.pending = true
	c.nextID++
	operation := &operationView{
		ID:          "op-" + strconv.FormatUint(c.nextID, 10),
		Action:      action,
		Model:       modelID,
		State:       stateAwaitingConfirmation,
		RequestedAt: c.now().UTC().Format(time.RFC3339Nano),
	}
	c.current = operation
	view := *operation
	c.mu.Unlock()

	c.log("operation", map[string]any{"id": view.ID, "action": action, "model": modelID, "state": view.State})
	go c.run(operation, approvalRequest{
		Environment: c.environment,
		Action:      action,
		Model:       modelID,
		DisplayName: displayName,
		Replaces:    replaces,
	})
	return view, nil
}

func (c *controller) run(operation *operationView, request approvalRequest) {
	switch c.approver.approve(request) {
	case approvalGranted:
	case approvalExpired:
		c.finish(operation, stateExpired, "")
		return
	default:
		c.finish(operation, stateDeclined, "")
		return
	}

	c.setState(operation, stateRunning)
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	_, err := c.executor.Execute(ctx, adminpipe.Request{Operation: request.Action, ModelID: request.Model})
	if err != nil {
		c.finish(operation, stateFailed, failureCode(err))
		return
	}
	c.finish(operation, stateSucceeded, "")
}

func (c *controller) setState(operation *operationView, state string) {
	c.mu.Lock()
	operation.State = state
	view := *operation
	c.mu.Unlock()
	c.log("operation", operationFields(view, ""))
}

func (c *controller) finish(operation *operationView, state, code string) {
	c.mu.Lock()
	operation.State = state
	operation.Error = code
	operation.FinishedAt = c.now().UTC().Format(time.RFC3339Nano)
	c.pending = false
	view := *operation
	c.mu.Unlock()
	c.log("operation", operationFields(view, code))
	if c.refresh != nil {
		// The page should see the new model at once, not at the next poll.
		c.refresh()
	}
}

// operationFields are the metadata an operation's log line carries. The target
// of a stop is a program name and a pid; nothing about the process's arguments
// or memory is ever logged.
func operationFields(view operationView, code string) map[string]any {
	fields := map[string]any{"id": view.ID, "action": view.Action, "model": view.Model, "state": view.State}
	if view.Target != "" {
		fields["target"] = view.Target
	}
	if code != "" {
		fields["error"] = code
	}
	return fields
}

var safeSourceID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// startStop begins ending the process that holds one of the discovered sources'
// model. The page names the source; everything else is decided here, from the
// monitor's own last look at the machine.
func (c *controller) startStop(sourceID string) (operationView, *actionError) {
	if c == nil || c.reason == "disabled" || c.terminator == nil || c.sources == nil {
		return operationView{}, &actionError{Status: 503, Code: "controls_unavailable"}
	}
	if !safeSourceID.MatchString(sourceID) {
		return operationView{}, &actionError{Status: 400, Code: "invalid_source"}
	}
	var source *inferenceSource
	for _, candidate := range c.sources() {
		if candidate.ID == sourceID {
			found := candidate
			source = &found
			break
		}
	}
	if source == nil {
		return operationView{}, &actionError{Status: 404, Code: "unknown_source"}
	}
	if !source.Stoppable || source.stopPID == 0 {
		return operationView{}, &actionError{Status: 409, Code: "nothing_to_stop"}
	}
	identity, err := c.terminator.identify(source.stopPID)
	switch {
	case errors.Is(err, errProcessGone):
		return operationView{}, &actionError{Status: 409, Code: "process_gone"}
	case err != nil:
		return operationView{}, &actionError{Status: 403, Code: "not_permitted"}
	case !strings.EqualFold(identity.Exe, source.stopExe):
		// The pid is now some other program than the one the page was showing.
		return operationView{}, &actionError{Status: 409, Code: "process_changed"}
	case !endable(identity.Exe, identity.PID, c.selfPID):
		return operationView{}, &actionError{Status: 403, Code: "not_permitted"}
	}

	c.mu.Lock()
	if c.pending {
		c.mu.Unlock()
		return operationView{}, &actionError{Status: 409, Code: "operation_in_progress"}
	}
	c.pending = true
	c.nextID++
	operation := &operationView{
		ID:          "op-" + strconv.FormatUint(c.nextID, 10),
		Action:      actionStopSource,
		Target:      source.Label + " · " + source.StopTarget,
		State:       stateAwaitingConfirmation,
		RequestedAt: c.now().UTC().Format(time.RFC3339Nano),
	}
	c.current = operation
	view := *operation
	c.mu.Unlock()

	c.log("operation", operationFields(view, ""))
	detail := ""
	if source.VRAMDedicatedMiB != nil {
		detail = ", " + strings.Replace(fmt.Sprintf("%.1f", *source.VRAMDedicatedMiB/1024), ".", ",", 1) + " GiB de VRAM"
	}
	go c.runStop(operation, approvalRequest{
		Environment: c.environment,
		Action:      actionStopSource,
		DisplayName: source.Label,
		Target:      source.StopTarget,
		Detail:      detail,
	}, identity)
	return view, nil
}

func (c *controller) runStop(operation *operationView, request approvalRequest, target processIdentity) {
	switch c.approver.approve(request) {
	case approvalGranted:
	case approvalExpired:
		c.finish(operation, stateExpired, "")
		return
	default:
		c.finish(operation, stateDeclined, "")
		return
	}
	c.setState(operation, stateRunning)
	if err := c.terminator.terminate(target); err != nil {
		c.finish(operation, stateFailed, terminateCode(err))
		return
	}
	c.finish(operation, stateSucceeded, "")
}

// terminateCode reduces a failure to a code the page can translate.
func terminateCode(err error) string {
	switch {
	case errors.Is(err, errProcessGone):
		return "process_gone"
	case errors.Is(err, errProcessChanged):
		return "process_changed"
	case errors.Is(err, errNotPermitted):
		return "not_permitted"
	case errors.Is(err, errTerminateSlow):
		return "terminate_timeout"
	default:
		return "terminate_failed"
	}
}

func rosterName(status *edgeStatus, id string) (string, bool) {
	for _, model := range status.Models {
		if model.ID == id {
			if model.DisplayName != "" {
				return model.DisplayName, true
			}
			return id, true
		}
	}
	return "", false
}

var safeFailureCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// failureCode reduces an error to a code the page can translate. The edge's
// own codes are fixed strings it chose; anything else - including a transport
// failure whose text names a path - collapses to a generic code rather than
// being passed through.
func failureCode(err error) string {
	var pipeErr *adminpipe.Error
	switch {
	case errors.As(err, &pipeErr) && safeFailureCode.MatchString(pipeErr.Code):
		return pipeErr.Code
	case errors.Is(err, adminpipe.ErrNotListening):
		return "admin_pipe_not_listening"
	case errors.Is(err, adminpipe.ErrUnsupported):
		return "admin_pipe_unsupported"
	case errors.Is(err, context.DeadlineExceeded):
		return "admin_timeout"
	default:
		return "admin_transport_failed"
	}
}
