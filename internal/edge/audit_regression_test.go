package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression scenarios reproduced during the October 2026 audit.
func regressionSSE(t *testing.T, wire string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(wire, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestRegressionAnthropicMixedStreamIndexes(t *testing.T) {
	for _, order := range []string{"text-first", "tool-first"} {
		t.Run(order, func(t *testing.T) {
			text := `data: {"choices":[{"delta":{"content":"Checking."}}]}` + "\n\n"
			tool := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_file","arguments":"{}"}}]}}]}` + "\n\n"
			stream := text + tool
			if order == "tool-first" {
				stream = tool + text
			}
			stream += "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
			w := httptest.NewRecorder()
			if err := writeAnthropicStream(w, strings.NewReader(stream), "local-coding", "audit"); err != nil {
				t.Fatal(err)
			}
			next := 0
			for _, event := range regressionSSE(t, w.Body.String()) {
				if event["type"] == "content_block_start" {
					if index := int(event["index"].(float64)); index != next {
						t.Fatalf("block index=%d; expected index=%d (final content array)", index, next)
					}
					next++
				}
			}
		})
	}
}

func TestRegressionAnthropicTruncatedStream(t *testing.T) {
	w := httptest.NewRecorder()
	err := writeAnthropicStream(w, strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"), "local-coding", "audit")
	if err == nil {
		t.Fatalf("truncated upstream returned success: %s", w.Body.String())
	}
}

func TestRegressionAnthropicStreamUsageFromTimings(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"timings\":{\"prompt_n\":23,\"cache_n\":7,\"predicted_n\":5}}\n\ndata: [DONE]\n\n"
	w := httptest.NewRecorder()
	if err := writeAnthropicStream(w, strings.NewReader(stream), "local-coding", "audit"); err != nil {
		t.Fatal(err)
	}
	for _, event := range regressionSSE(t, w.Body.String()) {
		if event["type"] == "message_delta" {
			usage := event["usage"].(map[string]any)
			if output := int(usage["output_tokens"].(float64)); output != 5 {
				t.Errorf("output_tokens=%d, want runtime's 5", output)
			}
		}
	}
}

func TestRegressionAnthropicNonstreamUsageFromTimings(t *testing.T) {
	_, usage, _, err := anthropicResponseFromChat([]byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"timings":{"prompt_n":23,"cache_n":7,"predicted_n":5}}`), "local-coding", "audit")
	if err != nil {
		t.Fatal(err)
	}
	if usage["input_tokens"] != 30 || usage["output_tokens"] != 5 {
		t.Fatalf("usage=%v, want input_tokens=30 output_tokens=5", usage)
	}
}

func TestRegressionOpenAIRoutesEnforceCapabilities(t *testing.T) {
	for _, route := range []string{"/v1/responses", "/v1/chat/completions"} {
		t.Run(route, func(t *testing.T) {
			var forwarded atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/running" {
					_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
					return
				}
				forwarded.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"unsupported feature executed"}}]}`)
			}))
			defer backend.Close()
			cfg := testConfig(backend.URL)
			cfg.Models[0].Capabilities = Capabilities{}
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server.memoryStatus = fixedMemory(64, 32)
			body := `{"model":"local-coding","stream":true,"messages":[{"role":"user","content":"test"}]}`
			if route == "/v1/responses" {
				body = `{"model":"local-coding","stream":true,"input":"test"}`
			}
			w := dataRequest(t, server.DataHandler(), http.MethodPost, route, []byte(body))
			if w.Code < 400 || forwarded.Load() != 0 {
				t.Fatalf("all capabilities=false; status=%d forwarded=%d; expected refusal before inference", w.Code, forwarded.Load())
			}
		})
	}
}

func TestRegressionAnthropicEnforcesToolChoice(t *testing.T) {
	converted, err := decodeAnthropicRequest([]byte(`{"model":"local-coding","max_tokens":10,"messages":[{"role":"user","content":"test"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(converted.body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["tool_choice"]; !exists {
		t.Fatalf("forced tool choice silently removed from upstream request: %s", converted.body)
	}
}

func TestRegressionOpenAIQueuedBodySurvivesReadDeadline(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer backend.Close()
	cfg := testConfig(backend.URL)
	cfg.QueueWait = time.Second
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(64, 32)
	release, err := server.gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseHeld := func() { releaseOnce.Do(release) }
	defer releaseHeld()
	released := make(chan struct{})
	go func() {
		defer close(released)
		deadline := time.Now().Add(time.Second)
		for server.gate.snapshot().Queued == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(250 * time.Millisecond)
		releaseHeld()
	}()
	httpServer := httptest.NewUnstartedServer(server.DataHandler())
	// Scale the production 30-second read deadline below the queue wait.
	// Production allows 120 seconds in queue; before the fix the body was read
	// only after that wait, past the 30-second read deadline.
	httpServer.Config.ReadTimeout = 100 * time.Millisecond
	httpServer.Start()
	defer httpServer.Close()
	body, _ := json.Marshal(map[string]any{"model": "local-coding", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("x", 128<<10)}}})
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	<-released
	text, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid queued request rejected after read deadline: HTTP %d %s", response.StatusCode, text)
	}
}

func TestAnthropicStreamKeepsMultipleToolFragmentsOnTheirBlocks(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"{\"n\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	w := httptest.NewRecorder()
	if err := writeAnthropicStream(w, strings.NewReader(stream), "local-coding", "test"); err != nil {
		t.Fatal(err)
	}
	blocks := make([]map[string]any, 0)
	fragments := make(map[int]string)
	stopped := make(map[int]bool)
	for _, event := range regressionSSE(t, w.Body.String()) {
		switch event["type"] {
		case "content_block_start":
			index := int(event["index"].(float64))
			if index != len(blocks) {
				t.Fatalf("non-sequential block: %v", event)
			}
			blocks = append(blocks, event["content_block"].(map[string]any))
		case "content_block_delta":
			index := int(event["index"].(float64))
			if index >= len(blocks) || stopped[index] {
				t.Fatalf("delta without an open block: %v", event)
			}
			delta := event["delta"].(map[string]any)
			if delta["type"] == "text_delta" {
				if blocks[index]["type"] != "text" {
					t.Fatal("text applied to a tool")
				}
				fragments[index] += delta["text"].(string)
			} else {
				if blocks[index]["type"] != "tool_use" {
					t.Fatal("tool input applied to text")
				}
				fragments[index] += delta["partial_json"].(string)
			}
		case "content_block_stop":
			index := int(event["index"].(float64))
			if stopped[index] {
				t.Fatal("block stopped twice")
			}
			stopped[index] = true
		}
	}
	if len(blocks) != 3 || len(stopped) != 3 || fragments[0] != `{"n":1}` || fragments[1] != "hello" || fragments[2] != `{}` {
		t.Fatalf("incorrect accumulated content: blocks=%v fragments=%v stops=%v", blocks, fragments, stopped)
	}
}

func TestAnthropicStreamDistinguishesCompletionFromErrors(t *testing.T) {
	for _, test := range []struct {
		name, stream string
		failed       bool
	}{
		{"EOF after finish", `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n", false},
		{"DONE without finish", "data: [DONE]\n\n", true},
		{"upstream error", `data: {"error":{"message":"private upstream detail"}}` + "\n\n", true},
		{"invalid chunk", "data: {broken}\n\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			err := writeAnthropicStream(w, strings.NewReader(test.stream), "local-coding", "test")
			if (err != nil) != test.failed {
				t.Fatalf("error=%v, want failed=%v", err, test.failed)
			}
			wire := w.Body.String()
			if strings.Contains(wire, "event: error") != test.failed || strings.Contains(wire, "event: message_stop") == test.failed {
				t.Fatalf("wrong terminal events: %s", wire)
			}
			if strings.Contains(wire, "private upstream detail") {
				t.Fatal("upstream error detail exposed")
			}
		})
	}
}

func TestAnthropicStreamingUsageMergesCountsAcrossChunks(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"content":"hello"}}],"usage":{"prompt_tokens":30}}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"completion_tokens":5}}` + "\n\ndata: [DONE]\n\n"
	w := httptest.NewRecorder()
	if err := writeAnthropicStream(w, strings.NewReader(stream), "local-coding", "test"); err != nil {
		t.Fatal(err)
	}
	for _, event := range regressionSSE(t, w.Body.String()) {
		if event["type"] != "message_delta" {
			continue
		}
		usage := event["usage"].(map[string]any)
		if usage["input_tokens"] != float64(30) || usage["output_tokens"] != float64(5) {
			t.Fatalf("usage = %v", usage)
		}
		return
	}
	t.Fatal("missing final usage")
}

// TestRegressionAnthropicRefusesUnqualifiedRequestsBeforeAdmission holds the
// only inference slot and checks that /v1/messages refuses requests it can
// never serve without queueing for, or reserving, that slot. Before the fix
// the handler acquired the slot first, so each request below waited the full
// QueueWait and then failed with 429 instead of its own refusal. Each case
// builds its own server because each turns off a different capability.
func TestRegressionAnthropicRefusesUnqualifiedRequestsBeforeAdmission(t *testing.T) {
	var upstreamCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"refused request executed"},"finish_reason":"stop"}]}`)
	}))
	defer backend.Close()
	const queueWait = 5 * time.Second

	for _, test := range []struct {
		name    string
		disable func(*Capabilities)
		body    string
		status  int
		kind    string
		message string
	}{
		{
			name:    "chat completions",
			disable: func(c *Capabilities) { c.ChatCompletions = false },
			body:    `{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`,
			status:  http.StatusBadRequest,
			kind:    "invalid_request_error",
			message: "selected model does not support chat completions",
		},
		{
			name:    "streaming",
			disable: func(c *Capabilities) { c.Streaming = false },
			body:    `{"model":"local-coding","max_tokens":4,"stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			status:  http.StatusBadRequest,
			kind:    "invalid_request_error",
			message: "selected model does not support streaming",
		},
		{
			name:    "required tool choice",
			disable: func(c *Capabilities) { c.FunctionCalling = false },
			body:    `{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"x","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`,
			status:  http.StatusBadRequest,
			kind:    "invalid_request_error",
			message: "selected model does not support the required tool choice",
		},
		{
			name:    "tool-use history",
			disable: func(c *Capabilities) { c.FunctionCalling = false },
			body: `{"model":"local-coding","max_tokens":4,"messages":[
				{"role":"user","content":"hello"},
				{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"x","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}
			]}`,
			status:  http.StatusBadRequest,
			kind:    "invalid_request_error",
			message: "selected model cannot continue a tool-use conversation",
		},
		{
			name:    "unknown model",
			body:    `{"model":"unknown","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`,
			status:  http.StatusNotFound,
			kind:    "not_found_error",
			message: "requested model is not available",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(backend.URL)
			cfg.QueueWait = queueWait
			if test.disable != nil {
				test.disable(&cfg.Models[0].Capabilities)
			}
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server.memoryStatus = fixedMemory(64, 32)
			release, err := server.gate.acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			started := time.Now()
			response := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(test.body))
			elapsed := time.Since(started)
			var refusal struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &refusal)
			if response.Code != test.status || refusal.Error.Type != test.kind || refusal.Error.Message != test.message {
				t.Fatalf("status=%d body=%s after %v; want %d %s %q", response.Code, response.Body.String(), elapsed, test.status, test.kind, test.message)
			}
			if elapsed > time.Second {
				t.Fatalf("refusal took %v with the slot held; it waited for admission (QueueWait=%v)", elapsed, queueWait)
			}
			if snapshot := server.gate.snapshot(); snapshot.Active != 1 || snapshot.Queued != 0 || snapshot.Rejected != 0 || snapshot.TimedOut != 0 {
				t.Fatalf("refused request touched the inference gate: %+v", snapshot)
			}
			if reserved := len(server.bodies); reserved != 0 {
				t.Fatalf("%d body reservations still held after the refusal", reserved)
			}
		})
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("refused requests reached upstream %d times", calls)
	}
}

// auditEventLog is a LogOutput the test can read while a handler goroutine
// may still write to it.
type auditEventLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *auditEventLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *auditEventLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// claudeEvents returns the claude.* events in the log, in order, without the
// time and service fields that every event carries. Each log line is written
// whole, so a read that races a handler never sees half a line.
func (l *auditEventLog) claudeEvents(t *testing.T) []map[string]any {
	t.Helper()
	events := []map[string]any{}
	for _, line := range strings.Split(l.String(), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if name, _ := entry["event"].(string); strings.HasPrefix(name, "claude.") {
			delete(entry, "time")
			delete(entry, "service")
			events = append(events, entry)
		}
	}
	return events
}

// TestRegressionAnthropicAdmissibleRequestWaitsForTheInferenceSlot is the
// positive counterpart of the test above. Moving the checks ahead of admission
// must not let an admissible /v1/messages request skip the gate: it queues
// while the only slot is held, reaches upstream only once that slot is
// released, and logs the selection events only after admission.
func TestRegressionAnthropicAdmissibleRequestWaitsForTheInferenceSlot(t *testing.T) {
	var upstreamCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		upstreamCalls.Add(1)
		if body, _ := io.ReadAll(r.Body); bytes.Contains(body, []byte(`"tools"`)) {
			t.Errorf("tool definitions reached a model without function calling: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_admitted","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer backend.Close()
	const queueWait = 5 * time.Second
	logs := &auditEventLog{}
	cfg := testConfig(backend.URL)
	cfg.QueueWait = queueWait
	cfg.LogOutput = logs
	cfg.Models[0].Capabilities.FunctionCalling = false
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.memoryStatus = fixedMemory(64, 32)
	release, err := server.gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseHeld := func() { releaseOnce.Do(release) }
	defer releaseHeld()

	// Tools without a tool_choice on a model without function calling is the
	// Cowork text fallback: admissible, with the tools omitted.
	body := []byte(`{"model":"local-coding","max_tokens":4,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"x","input_schema":{"type":"object"}}]}`)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", body)
	}()
	waitUntil := time.Now().Add(2 * time.Second)
	for server.gate.snapshot().Queued != 1 {
		select {
		case response := <-done:
			t.Fatalf("admissible request finished without the held inference slot: status=%d body=%s", response.Code, response.Body.String())
		default:
		}
		if time.Now().After(waitUntil) {
			t.Fatalf("admissible request never queued for the held slot: %+v", server.gate.snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case response := <-done:
		t.Fatalf("queued request finished while the slot was held: status=%d body=%s", response.Code, response.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("queued request reached upstream %d times before admission", calls)
	}
	if events := logs.claudeEvents(t); len(events) != 0 {
		t.Fatalf("selection events logged before admission: %v", events)
	}

	releaseHeld()
	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(queueWait / 2):
		t.Fatalf("queued request was not admitted after the slot was released: %+v", server.gate.snapshot())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("admitted request status=%d body=%s", response.Code, response.Body.String())
	}
	if calls := upstreamCalls.Load(); calls != 1 {
		t.Fatalf("admitted request reached upstream %d times, want 1", calls)
	}
	want := []map[string]any{
		{"event": "claude.tools.omitted", "model": "local-coding"},
		{"event": "claude.model.selected", "model": "local-coding", "external_alias_used": false},
	}
	if events := logs.claudeEvents(t); !reflect.DeepEqual(events, want) {
		t.Errorf("admitted request logged %v, want %v", events, want)
	}
	if snapshot := server.gate.snapshot(); snapshot.Active != 0 || snapshot.Queued != 0 || snapshot.Rejected != 0 || snapshot.TimedOut != 0 {
		t.Fatalf("inference gate after the admitted request: %+v", snapshot)
	}
	if reserved := len(server.bodies); reserved != 0 {
		t.Fatalf("%d body reservations still held after the request finished", reserved)
	}
}

// TestRegressionAnthropicSelectionEventsDescribeTheRequest pins the selection
// events of admitted requests to their fields at HEAD: claude.tools.omitted
// only when tool definitions were actually dropped, and claude.model.selected
// with the model and whether the client used the Desktop wire alias. The case
// with tools dropped is covered by the admission test above.
func TestRegressionAnthropicSelectionEventsDescribeTheRequest(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_selected","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer backend.Close()
	const tools = `,"tools":[{"name":"x","input_schema":{"type":"object"}}]`
	for _, test := range []struct {
		name            string
		functionCalling bool
		model           string
		extra           string
		aliasUsed       bool
	}{
		{name: "no tools without function calling", model: "local-coding"},
		{name: "tools kept with function calling", functionCalling: true, model: "local-coding", extra: tools},
		{name: "Desktop wire alias", model: claudeExternalModelID("local-coding"), aliasUsed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := &auditEventLog{}
			cfg := testConfig(backend.URL)
			cfg.LogOutput = logs
			cfg.Models[0].Capabilities.FunctionCalling = test.functionCalling
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server.memoryStatus = fixedMemory(64, 32)
			body := `{"model":"` + test.model + `","max_tokens":4,"messages":[{"role":"user","content":"hello"}]` + test.extra + `}`
			response := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", []byte(body))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			want := []map[string]any{{"event": "claude.model.selected", "model": "local-coding", "external_alias_used": test.aliasUsed}}
			if events := logs.claudeEvents(t); !reflect.DeepEqual(events, want) {
				t.Fatalf("logged %v, want %v", events, want)
			}
		})
	}
}
