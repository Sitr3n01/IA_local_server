package monitor

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTerminator struct {
	mu           sync.Mutex
	identity     processIdentity
	identifyErr  error
	terminateErr error
	terminated   []processIdentity
}

func (f *fakeTerminator) identify(uint32) (processIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, f.identifyErr
}

func (f *fakeTerminator) terminate(target processIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, target)
	return f.terminateErr
}

func (f *fakeTerminator) ended() []processIdentity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]processIdentity(nil), f.terminated...)
}

func bionicSource() inferenceSource {
	return inferenceSource{
		ID: "lm-studio-20", Kind: kindLMStudio, Label: "LM Studio / Bionic", PID: 20, Status: sourceProtected,
		Stoppable: true, StopTarget: "llama-server.exe · PID 20", stopPID: 20, stopExe: "llama-server.exe",
		VRAMDedicatedMiB: ptr(7400), tool: toolLMStudio,
	}
}

func stopController(t *testing.T, approver approver, terminator processTerminator, sources ...inferenceSource) (*controller, *fakeExecutor) {
	t.Helper()
	executor := &fakeExecutor{}
	control, _, _ := testController(rosterStatus(""), approver, executor)
	control.terminator = terminator
	control.selfPID = 999
	control.sources = func() []inferenceSource { return sources }
	return control, executor
}

func liveIdentity() processIdentity {
	return processIdentity{PID: 20, Exe: "llama-server.exe", Started: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
}

func TestStopEndsTheProcessOnlyAfterTheOperatorConfirmsAndNeverUsesThePipe(t *testing.T) {
	approver := &fakeApprover{decision: approvalGranted, hold: make(chan struct{})}
	terminator := &fakeTerminator{identity: liveIdentity()}
	control, executor := stopController(t, approver, terminator, bionicSource())

	started, failure := control.startStop("lm-studio-20")
	if failure != nil {
		t.Fatalf("refused: %+v", failure)
	}
	if started.Action != actionStopSource || started.State != stateAwaitingConfirmation || started.Target != "LM Studio / Bionic · llama-server.exe · PID 20" {
		t.Fatalf("started = %+v", started)
	}
	time.Sleep(20 * time.Millisecond)
	if len(terminator.ended()) != 0 {
		t.Fatal("the process was ended before the operator answered")
	}
	close(approver.hold)
	finished := waitForOperation(t, control, stateSucceeded)
	if finished.Error != "" {
		t.Fatalf("finished = %+v", finished)
	}
	if ended := terminator.ended(); len(ended) != 1 || ended[0] != liveIdentity() {
		t.Fatalf("ended = %+v; it must be the identity that was checked", ended)
	}
	if len(executor.seen()) != 0 {
		t.Fatalf("a stop went to the edge's pipe: %+v", executor.seen())
	}
	asked := approver.seen()
	if len(asked) != 1 || asked[0].Action != actionStopSource || asked[0].Target != "llama-server.exe · PID 20" ||
		asked[0].DisplayName != "LM Studio / Bionic" || !strings.Contains(asked[0].Detail, "7,2 GiB") {
		t.Fatalf("the confirmation showed %+v", asked)
	}
}

func TestStopDoesNothingWhenTheOperatorDeclinesOrDoesNotAnswer(t *testing.T) {
	for state, decision := range map[string]approval{stateDeclined: approvalDeclined, stateExpired: approvalExpired} {
		terminator := &fakeTerminator{identity: liveIdentity()}
		control, _ := stopController(t, &fakeApprover{decision: decision}, terminator, bionicSource())
		if _, failure := control.startStop("lm-studio-20"); failure != nil {
			t.Fatalf("%s: refused: %+v", state, failure)
		}
		waitForOperation(t, control, state)
		if len(terminator.ended()) != 0 {
			t.Errorf("%s: a process was ended", state)
		}
	}
}

func TestStopRefusesWhatItCannotStandBehindBeforeAskingAnyone(t *testing.T) {
	gone := &fakeTerminator{identifyErr: errProcessGone}
	denied := &fakeTerminator{identifyErr: errNotPermitted}
	renamed := &fakeTerminator{identity: processIdentity{PID: 20, Exe: "notepad.exe", Started: time.Now()}}
	protected := &fakeTerminator{identity: processIdentity{PID: 20, Exe: "llama-server.exe"}}
	notStoppable := bionicSource()
	notStoppable.Stoppable, notStoppable.stopPID = false, 0
	system := bionicSource()
	system.stopExe = "dwm.exe"

	for _, tc := range []struct {
		name       string
		terminator processTerminator
		source     string
		sources    []inferenceSource
		configure  func(*controller)
		status     int
		code       string
	}{
		{"an unknown source", &fakeTerminator{identity: liveIdentity()}, "no-such-1", []inferenceSource{bionicSource()}, nil, 404, "unknown_source"},
		{"a malformed source id", &fakeTerminator{identity: liveIdentity()}, "../../etc", []inferenceSource{bionicSource()}, nil, 400, "invalid_source"},
		{"a source with nothing to stop", &fakeTerminator{identity: liveIdentity()}, "lm-studio-20", []inferenceSource{notStoppable}, nil, 409, "nothing_to_stop"},
		{"a process that has exited", gone, "lm-studio-20", []inferenceSource{bionicSource()}, nil, 409, "process_gone"},
		{"a process of another user", denied, "lm-studio-20", []inferenceSource{bionicSource()}, nil, 403, "not_permitted"},
		{"a pid reused by another program", renamed, "lm-studio-20", []inferenceSource{bionicSource()}, nil, 409, "process_changed"},
		{"the monitor's own pid", protected, "lm-studio-20", []inferenceSource{bionicSource()}, func(c *controller) { c.selfPID = 20 }, 403, "not_permitted"},
		{"a system program named by a subverted list", &fakeTerminator{identity: processIdentity{PID: 20, Exe: "dwm.exe"}}, "lm-studio-20", []inferenceSource{system}, nil, 403, "not_permitted"},
		{"controls turned off", &fakeTerminator{identity: liveIdentity()}, "lm-studio-20", []inferenceSource{bionicSource()}, func(c *controller) { c.reason = "disabled" }, 503, "controls_unavailable"},
		{"no way to end a process here", nil, "lm-studio-20", []inferenceSource{bionicSource()}, nil, 503, "controls_unavailable"},
	} {
		approver := &fakeApprover{decision: approvalGranted}
		control, _ := stopController(t, approver, tc.terminator, tc.sources...)
		if tc.terminator == nil {
			control.terminator = nil
		}
		if tc.configure != nil {
			tc.configure(control)
		}
		_, failure := control.startStop(tc.source)
		if failure == nil || failure.Status != tc.status || failure.Code != tc.code {
			t.Errorf("%s: failure = %+v, want %d %s", tc.name, failure, tc.status, tc.code)
		}
		if len(approver.seen()) != 0 {
			t.Errorf("%s: the operator was asked about a request that cannot succeed", tc.name)
		}
	}
}

func TestStopReportsWhyItFailedWithCodesThePageCanTranslate(t *testing.T) {
	for want, err := range map[string]error{
		"process_gone":      errProcessGone,
		"process_changed":   errProcessChanged,
		"not_permitted":     errNotPermitted,
		"terminate_timeout": errTerminateSlow,
		"terminate_failed":  errTerminateFailed,
	} {
		terminator := &fakeTerminator{identity: liveIdentity(), terminateErr: err}
		control, _ := stopController(t, &fakeApprover{decision: approvalGranted}, terminator, bionicSource())
		if _, failure := control.startStop("lm-studio-20"); failure != nil {
			t.Fatalf("%s: refused: %+v", want, failure)
		}
		if failed := waitForOperation(t, control, stateFailed); failed.Error != want {
			t.Errorf("error = %q, want %q", failed.Error, want)
		}
	}
}

func TestStopSharesTheOneOperationAtATimeRule(t *testing.T) {
	approver := &fakeApprover{decision: approvalGranted, hold: make(chan struct{})}
	terminator := &fakeTerminator{identity: liveIdentity()}
	control, _ := stopController(t, approver, terminator, bionicSource())
	if _, failure := control.startStop("lm-studio-20"); failure != nil {
		t.Fatalf("first refused: %+v", failure)
	}
	if _, failure := control.startStop("lm-studio-20"); failure == nil || failure.Code != "operation_in_progress" {
		t.Fatalf("second = %+v, want operation_in_progress", failure)
	}
	// An edge operation is held off by it, too.
	if _, failure := control.start(actionSwitch, "huge"); failure == nil || failure.Code != "operation_in_progress" {
		t.Fatalf("switch = %+v, want operation_in_progress", failure)
	}
	close(approver.hold)
	waitForOperation(t, control, stateSucceeded)
}

func TestStopIsOfferedIndependentlyOfTheEdgeButFollowsTheMasterSwitch(t *testing.T) {
	control, _ := stopController(t, &fakeApprover{}, &fakeTerminator{}, bionicSource())
	control.reason = "misconfigured" // a bad pipe name breaks the edge's controls, not this one
	if view := control.view(); view.Available || !view.StopAvailable {
		t.Fatalf("misconfigured pipe: %+v", view)
	}
	control.reason = "disabled"
	if view := control.view(); view.StopAvailable {
		t.Fatalf("-admin-pipe off left the stop control on: %+v", view)
	}
	control.reason = ""
	control.terminator = nil
	if view := control.view(); view.StopAvailable {
		t.Fatalf("no terminator: %+v", view)
	}
}

func newStopHandler(t *testing.T, approver approver, terminator processTerminator) (*handler, *fakeTerminator) {
	t.Helper()
	fake, _ := terminator.(*fakeTerminator)
	h := newActionHandler(t, approver, &fakeExecutor{}, true)
	h.collector.control.terminator = terminator
	h.collector.control.selfPID = 999
	h.collector.control.sources = func() []inferenceSource { return []inferenceSource{bionicSource()} }
	return h, fake
}

func TestStopRouteAcceptsASourceAndNothingElse(t *testing.T) {
	h, terminator := newStopHandler(t, &fakeApprover{decision: approvalGranted}, &fakeTerminator{identity: liveIdentity()})
	recorder := postAction(h, http.MethodPost, pageHeaders(), `{"action":"stop_source","source":"lm-studio-20"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Operation operationView `json:"operation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Operation.Action != actionStopSource || response.Operation.Target == "" {
		t.Fatalf("operation = %+v", response.Operation)
	}
	waitForOperation(t, h.collector.control, stateSucceeded)
	if len(terminator.ended()) != 1 {
		t.Fatalf("ended = %+v", terminator.ended())
	}

	// A stop that also names a model or a process, and an edge action that
	// names a source, are malformed. The page cannot pick a pid at all.
	for name, body := range map[string]string{
		"a model beside a stop":   `{"action":"stop_source","source":"lm-studio-20","model":"huge"}`,
		"a source beside a load":  `{"action":"switch","model":"huge","source":"lm-studio-20"}`,
		"a pid":                   `{"action":"stop_source","source":"lm-studio-20","pid":20}`,
		"a stop without a source": `{"action":"stop_source"}`,
	} {
		h, terminator := newStopHandler(t, &fakeApprover{decision: approvalGranted}, &fakeTerminator{identity: liveIdentity()})
		recorder := postAction(h, http.MethodPost, pageHeaders(), body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, recorder.Code, recorder.Body)
		}
		if len(terminator.ended()) != 0 {
			t.Errorf("%s: a process was ended", name)
		}
	}
}

func TestStopRouteIsBehindTheSameOriginAndPeerChecksAsTheOthers(t *testing.T) {
	h, terminator := newStopHandler(t, &fakeApprover{decision: approvalGranted}, &fakeTerminator{identity: liveIdentity()})
	headers := pageHeaders()
	headers["Origin"] = "http://attacker.example"
	if recorder := postAction(h, http.MethodPost, headers, `{"action":"stop_source","source":"lm-studio-20"}`); recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin stop = %d", recorder.Code)
	}
	h.peer = func(*http.Request) bool { return false }
	if recorder := postAction(h, http.MethodPost, pageHeaders(), `{"action":"stop_source","source":"lm-studio-20"}`); recorder.Code != http.StatusForbidden {
		t.Fatalf("another user's stop = %d", recorder.Code)
	}
	if len(terminator.ended()) != 0 {
		t.Fatal("a refused request ended a process")
	}
}
