package monitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sitr3n01/local-ai-provider/internal/adminpipe"
)

type fakeApprover struct {
	mu       sync.Mutex
	decision approval
	hold     chan struct{}
	requests []approvalRequest
}

func (f *fakeApprover) approve(request approvalRequest) approval {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	hold := f.hold
	decision := f.decision
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return decision
}

func (f *fakeApprover) seen() []approvalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]approvalRequest(nil), f.requests...)
}

type fakeExecutor struct {
	mu       sync.Mutex
	err      error
	requests []adminpipe.Request
}

func (f *fakeExecutor) Execute(_ context.Context, request adminpipe.Request) (adminpipe.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.err != nil {
		return adminpipe.Result{}, f.err
	}
	return adminpipe.Result{Operation: request.Operation, Model: request.ModelID, Status: "completed"}, nil
}

func (f *fakeExecutor) seen() []adminpipe.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]adminpipe.Request(nil), f.requests...)
}

type logRecorder struct {
	mu     sync.Mutex
	events []map[string]any
}

func (l *logRecorder) log(_ string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fields)
}

func rosterStatus(active string) *edgeStatus {
	return &edgeStatus{
		ActiveModel: active,
		Models: []edgeModel{
			{ID: "fast", DisplayName: "Fast Model"},
			{ID: "huge", DisplayName: "Huge Model"},
		},
	}
}

func testController(status *edgeStatus, approver approver, executor adminExecutor) (*controller, *logRecorder, func() int) {
	recorder := &logRecorder{}
	refreshes := 0
	var refreshMu sync.Mutex
	control := &controller{
		environment: "canary",
		approver:    approver,
		executor:    executor,
		status:      func() *edgeStatus { return status },
		refresh: func() {
			refreshMu.Lock()
			refreshes++
			refreshMu.Unlock()
		},
		now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) },
		log: recorder.log,
	}
	return control, recorder, func() int {
		refreshMu.Lock()
		defer refreshMu.Unlock()
		return refreshes
	}
}

func waitForOperation(t *testing.T, control *controller, state string) operationView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if view := control.view(); view.Operation != nil && view.Operation.State == state {
			return *view.Operation
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation never reached %q; last view %+v", state, control.view())
	return operationView{}
}

func TestControlSwitchRunsOnlyAfterConfirmation(t *testing.T) {
	approver := &fakeApprover{decision: approvalGranted, hold: make(chan struct{})}
	executor := &fakeExecutor{}
	control, _, refreshes := testController(rosterStatus("fast"), approver, executor)

	started, failure := control.start(actionSwitch, "huge")
	if failure != nil {
		t.Fatalf("start refused: %+v", failure)
	}
	if started.State != stateAwaitingConfirmation || started.Model != "huge" || started.Action != actionSwitch {
		t.Fatalf("started = %+v", started)
	}
	// Nothing may reach the pipe while the operator has not answered.
	time.Sleep(20 * time.Millisecond)
	if len(executor.seen()) != 0 {
		t.Fatal("the pipe was called before the confirmation was answered")
	}
	close(approver.hold)

	finished := waitForOperation(t, control, stateSucceeded)
	if finished.FinishedAt == "" || finished.Error != "" {
		t.Fatalf("finished = %+v", finished)
	}
	requests := executor.seen()
	if len(requests) != 1 || requests[0] != (adminpipe.Request{Operation: adminpipe.OperationSwitch, ModelID: "huge"}) {
		t.Fatalf("pipe requests = %+v", requests)
	}
	asked := approver.seen()
	if len(asked) != 1 || asked[0].DisplayName != "Huge Model" || asked[0].Replaces != "Fast Model" || asked[0].Environment != "canary" {
		t.Fatalf("confirmation showed %+v; it must name the model, what it replaces and the deployment", asked)
	}
	deadline := time.Now().Add(time.Second)
	for refreshes() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if refreshes() != 1 {
		t.Fatalf("status refreshes = %d, want one after the operation", refreshes())
	}
}

func TestControlDeclinedAndExpiredConfirmationsDoNothing(t *testing.T) {
	for decision, state := range map[approval]string{approvalDeclined: stateDeclined, approvalExpired: stateExpired} {
		executor := &fakeExecutor{}
		control, _, _ := testController(rosterStatus("fast"), &fakeApprover{decision: decision}, executor)
		if _, failure := control.start(actionUnload, "fast"); failure != nil {
			t.Fatalf("start refused: %+v", failure)
		}
		waitForOperation(t, control, state)
		if len(executor.seen()) != 0 {
			t.Errorf("a %s confirmation still reached the pipe", state)
		}
	}
}

func TestControlRefusesWhatCannotOrShouldNotRun(t *testing.T) {
	busy := rosterStatus("fast")
	busy.Gate.Active = 1
	queued := rosterStatus("")
	queued.Gate.Queued = 2
	for _, tc := range []struct {
		name   string
		status *edgeStatus
		action string
		model  string
		want   actionError
	}{
		{"an unknown verb", rosterStatus("fast"), "maintenance.drain", "fast", actionError{400, "unknown_action"}},
		{"a load verb the page does not use", rosterStatus(""), "load", "fast", actionError{400, "unknown_action"}},
		{"a model id with control characters", rosterStatus(""), actionSwitch, "fast\n", actionError{400, "invalid_model"}},
		{"a model the edge does not list", rosterStatus(""), actionSwitch, "other", actionError{404, "unknown_model"}},
		{"switching to the loaded model", rosterStatus("fast"), actionSwitch, "fast", actionError{409, "model_already_loaded"}},
		{"unloading a model that is not loaded", rosterStatus("fast"), actionUnload, "huge", actionError{409, "model_not_loaded"}},
		{"a request in flight", busy, actionUnload, "fast", actionError{409, "inference_busy"}},
		{"requests waiting", queued, actionSwitch, "huge", actionError{409, "inference_busy"}},
		{"no status yet", nil, actionSwitch, "huge", actionError{503, "status_unavailable"}},
	} {
		executor := &fakeExecutor{}
		approver := &fakeApprover{decision: approvalGranted}
		control, _, _ := testController(tc.status, approver, executor)
		_, failure := control.start(tc.action, tc.model)
		if failure == nil || *failure != tc.want {
			t.Errorf("%s: failure = %+v, want %+v", tc.name, failure, tc.want)
		}
		if len(approver.seen()) != 0 || len(executor.seen()) != 0 {
			t.Errorf("%s: a refused request still asked for confirmation or reached the pipe", tc.name)
		}
	}

	disabled := &controller{reason: "disabled"}
	if _, failure := disabled.start(actionSwitch, "fast"); failure == nil || failure.Code != "controls_unavailable" {
		t.Fatalf("disabled controls answered %+v", failure)
	}
	var missing *controller
	if view := missing.view(); view.Available || view.Reason != "disabled" {
		t.Fatalf("absent controller view = %+v", view)
	}
}

func TestControlAllowsOneOperationAtATime(t *testing.T) {
	approver := &fakeApprover{decision: approvalDeclined, hold: make(chan struct{})}
	control, _, _ := testController(rosterStatus(""), approver, &fakeExecutor{})
	if _, failure := control.start(actionSwitch, "fast"); failure != nil {
		t.Fatalf("first start refused: %+v", failure)
	}
	if _, failure := control.start(actionSwitch, "huge"); failure == nil || failure.Code != "operation_in_progress" {
		t.Fatalf("second start = %+v, want operation_in_progress", failure)
	}
	close(approver.hold)
	waitForOperation(t, control, stateDeclined)
	approver.mu.Lock()
	approver.hold = nil
	approver.mu.Unlock()
	if _, failure := control.start(actionSwitch, "huge"); failure != nil {
		t.Fatalf("a start after the first finished was refused: %+v", failure)
	}
}

// Failures reach the page as codes. Text from a pipe error can name a path or
// an executable; none of it may pass through.
func TestControlReportsFailuresAsCodesOnly(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&adminpipe.Error{Code: "insufficient_capacity", Message: `configured model C:\private\weights.gguf`}, "insufficient_capacity"},
		{&adminpipe.Error{Code: "inference_busy"}, "inference_busy"},
		{fmt.Errorf("%w: %s", adminpipe.ErrNotListening, `\\.\pipe\cia-local-ai-admin-canary`), "admin_pipe_not_listening"},
		{errors.New(`the administrative pipe is served by an unexpected executable C:\evil.exe`), "admin_transport_failed"},
		{&adminpipe.Error{Code: "Not A Code <script>"}, "admin_transport_failed"},
		{fmt.Errorf("exchange: %w", context.DeadlineExceeded), "admin_timeout"},
	} {
		control, recorder, _ := testController(rosterStatus(""), &fakeApprover{decision: approvalGranted}, &fakeExecutor{err: tc.err})
		if _, failure := control.start(actionSwitch, "fast"); failure != nil {
			t.Fatalf("start refused: %+v", failure)
		}
		finished := waitForOperation(t, control, stateFailed)
		if finished.Error != tc.want {
			t.Errorf("%v: code = %q, want %q", tc.err, finished.Error, tc.want)
		}
		recorder.mu.Lock()
		for _, event := range recorder.events {
			for key, value := range event {
				if text, ok := value.(string); ok && (strings.Contains(text, `C:\`) || strings.Contains(text, "<script>")) {
					t.Errorf("log field %s carried error text %q", key, text)
				}
				switch key {
				case "id", "action", "model", "state", "error":
				default:
					t.Errorf("log carried an unexpected field %q", key)
				}
			}
		}
		recorder.mu.Unlock()
	}
}
