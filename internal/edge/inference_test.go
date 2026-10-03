package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// steppedReader hands out its pieces in order and moves the clock once as each
// piece starts - however many Reads the consumer needs for it - so token
// timing is deterministic.
type steppedReader struct {
	pieces []string
	clock  *fakeClock
	step   time.Duration
	inside bool
}

func (r *steppedReader) Read(p []byte) (int, error) {
	if len(r.pieces) == 0 {
		return 0, io.EOF
	}
	if r.clock != nil && !r.inside {
		r.clock.Advance(r.step)
	}
	n := copy(p, r.pieces[0])
	r.pieces[0] = r.pieces[0][n:]
	r.inside = r.pieces[0] != ""
	if !r.inside {
		r.pieces = r.pieces[1:]
	}
	return n, nil
}

// splitEvery breaks a payload at a fixed width, so SSE lines arrive torn.
func splitEvery(payload string, width int) []string {
	var pieces []string
	for len(payload) > width {
		pieces = append(pieces, payload[:width])
		payload = payload[width:]
	}
	return append(pieces, payload)
}

func observeThrough(t *testing.T, track *inferenceTrack, contentType string, source io.Reader) string {
	t.Helper()
	response := &http.Response{Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(source)}
	reader := observeInferenceBody(withInferenceTrack(context.Background(), track), response)
	var out bytes.Buffer
	buffer := make([]byte, 7)
	for {
		n, err := reader.Read(buffer)
		out.Write(buffer[:n])
		if err != nil {
			if err != io.EOF {
				t.Fatalf("read: %v", err)
			}
			return out.String()
		}
	}
}

func testModel() Model {
	contextTokens, nPredict := 131072, 8192
	return Model{ID: "local-coding", ContextTokens: &contextTokens, Profile: ProfileSummary{NPredict: &nPredict}}
}

func onlyRecord(t *testing.T, log *inferenceLog) inferenceRecord {
	t.Helper()
	snapshot := log.snapshot(gateSnapshot{})
	if len(snapshot.Live) != 0 || len(snapshot.Recent) != 1 {
		t.Fatalf("live=%d recent=%d, want 0 and 1", len(snapshot.Live), len(snapshot.Recent))
	}
	return snapshot.Recent[0]
}

func intValue(t *testing.T, name string, value *int, want int) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("%s = %v, want %d", name, value, want)
	}
}

func floatValue(t *testing.T, name string, value *float64, want float64) {
	t.Helper()
	if value == nil || math.Abs(*value-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, value, want)
	}
}

const chatStream = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]," +
	"\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":100}}," +
	"\"timings\":{\"cache_n\":100,\"prompt_n\":20,\"prompt_ms\":50,\"prompt_per_second\":400," +
	"\"predicted_n\":4,\"predicted_ms\":200,\"predicted_per_second\":20}}\n\n" +
	"data: [DONE]\n\n"

func TestInferenceObserverPassesBytesThroughUnchanged(t *testing.T) {
	for _, width := range []int{1, 3, 17, 64, 4096} {
		log := newInferenceLog(time.Now)
		track := log.begin(testModel(), "/v1/chat/completions", true, false)
		out := observeThrough(t, track, "text/event-stream", &steppedReader{pieces: splitEvery(chatStream, width)})
		if out != chatStream {
			t.Fatalf("width %d: observer altered the stream", width)
		}
	}
}

func TestInferenceObserverRecordsRuntimeNumbersFromAChatStream(t *testing.T) {
	clock := newFakeClock()
	log := newInferenceLog(clock.Now)
	track := log.begin(testModel(), "/v1/chat/completions", true, true)
	observeThrough(t, track, "text/event-stream; charset=utf-8",
		&steppedReader{pieces: splitEvery(chatStream, 13), clock: clock, step: 10 * time.Millisecond})
	track.end(http.StatusOK, false)

	record := onlyRecord(t, log)
	if record.Model != "local-coding" || record.Route != "/v1/chat/completions" || !record.Stream || !record.ColdStart {
		t.Fatalf("identity = %+v", record)
	}
	if record.Finish != "stop" || record.Status != http.StatusOK {
		t.Fatalf("finish=%q status=%d", record.Finish, record.Status)
	}
	intValue(t, "prompt", record.PromptTokens, 120)
	intValue(t, "cached", record.CachedTokens, 100)
	intValue(t, "output", record.OutputTokens, 4)
	if record.Measurements.Prompt != "runtime-usage" || record.Measurements.Cache != "runtime-usage" ||
		record.Measurements.Output != "runtime-usage" || record.Measurements.DecodeRate != "runtime-timings" {
		t.Fatalf("measurement origins = %+v", record.Measurements)
	}
	if record.OutputEstimated {
		t.Fatal("the runtime reported usage, so output must not be marked estimated")
	}
	floatValue(t, "prompt rate", record.PromptPerSecond, 400)
	floatValue(t, "decode rate", record.DecodePerSecond, 20)
	if record.TTFTMS == nil || *record.TTFTMS <= 0 {
		t.Fatalf("ttft = %v, want a positive time to the first token", record.TTFTMS)
	}
	intValue(t, "context", record.ContextTokens, 131072)
}

// The shape llama-server b10225 actually emits when the client does not ask for
// usage - captured from a real stream, text replaced: the finishing chunk
// carries timings and no usage, so every count comes from the timings.
func TestInferenceObserverReadsLlamaServerTimingsWithoutUsage(t *testing.T) {
	log := newInferenceLog(time.Now)
	track := log.begin(testModel(), "/v1/chat/completions", true, false)
	stream := "data: {\"choices\":[{\"finish_reason\":null,\"index\":0,\"delta\":{\"reasoning_content\":\"x\"}}],\"object\":\"chat.completion.chunk\"}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"length\",\"index\":0,\"delta\":{}}],\"object\":\"chat.completion.chunk\"," +
		"\"timings\":{\"cache_n\":0,\"prompt_n\":23,\"prompt_ms\":137.142,\"prompt_per_token_ms\":5.962695652173913," +
		"\"prompt_per_second\":167.70938151696782,\"predicted_n\":24,\"predicted_ms\":416.334," +
		"\"predicted_per_token_ms\":17.34725,\"predicted_per_second\":57.64602458602949}}\n\n" +
		"data: [DONE]\n\n"
	observeThrough(t, track, "text/event-stream", strings.NewReader(stream))
	track.end(http.StatusOK, false)

	record := onlyRecord(t, log)
	intValue(t, "prompt", record.PromptTokens, 23)
	intValue(t, "cached", record.CachedTokens, 0)
	intValue(t, "output", record.OutputTokens, 24)
	if record.Measurements.Prompt != "runtime-timings" || record.Measurements.Cache != "runtime-timings" ||
		record.Measurements.Output != "runtime-timings" || record.Measurements.DecodeRate != "runtime-timings" {
		t.Fatalf("timing origins = %+v", record.Measurements)
	}
	if record.OutputEstimated || record.Finish != "length" {
		t.Fatalf("estimated=%v finish=%q", record.OutputEstimated, record.Finish)
	}
	floatValue(t, "decode rate", record.DecodePerSecond, 57.64602458602949)
}

func TestInferenceObserverCountsTokenEventsWhenNoUsageArrives(t *testing.T) {
	clock := newFakeClock()
	log := newInferenceLog(clock.Now)
	track := log.begin(testModel(), "/v1/chat/completions", true, false)
	events := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"c\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"d\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"e\"},\"finish_reason\":\"length\"}]}\n\n",
	}
	observeThrough(t, track, "text/event-stream", &steppedReader{pieces: events, clock: clock, step: 100 * time.Millisecond})
	track.end(http.StatusOK, false)

	record := onlyRecord(t, log)
	intValue(t, "output", record.OutputTokens, 5)
	if !record.OutputEstimated {
		t.Fatal("output counted from token events must be marked estimated")
	}
	if record.Measurements.Output != "stream-events-estimate" || record.Measurements.DecodeRate != "wall-clock-estimate" {
		t.Fatalf("estimated origins = %+v", record.Measurements)
	}
	if totals := log.snapshot(gateSnapshot{}).Totals; totals.OutputTokens != 0 || totals.EstimatedOutputEvents != 5 {
		t.Fatalf("event chunks cannot be added to exact token totals: %+v", totals)
	}
	if record.Finish != "length" {
		t.Fatalf("finish = %q, want length", record.Finish)
	}
	// Four tokens after the first, over the 400 ms between the first token and the end.
	floatValue(t, "decode rate", record.DecodePerSecond, 10)
	if record.PromptTokens != nil || record.PromptPerSecond != nil {
		t.Fatalf("prompt numbers were invented: %+v", record)
	}
}

func TestInferenceObserverReadsTheResponsesStream(t *testing.T) {
	log := newInferenceLog(time.Now)
	track := log.begin(testModel(), "/v1/responses", true, false)
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"!\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"," +
		"\"usage\":{\"input_tokens\":50,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":40}}}}\n\n"
	observeThrough(t, track, "text/event-stream", strings.NewReader(stream))
	track.end(http.StatusOK, false)

	record := onlyRecord(t, log)
	intValue(t, "prompt", record.PromptTokens, 50)
	intValue(t, "cached", record.CachedTokens, 40)
	intValue(t, "output", record.OutputTokens, 2)
	if record.Finish != "stop" || record.TTFTMS == nil {
		t.Fatalf("finish=%q ttft=%v", record.Finish, record.TTFTMS)
	}
}

func TestInferenceObserverReadsANonStreamingBodyOnce(t *testing.T) {
	log := newInferenceLog(time.Now)
	track := log.begin(testModel(), "/v1/chat/completions", false, false)
	body := `{"choices":[{"message":{"content":"answer"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":7},` +
		`"timings":{"prompt_n":10,"prompt_ms":25,"predicted_n":7,"predicted_ms":350}}`
	if out := observeThrough(t, track, "application/json", strings.NewReader(body)); out != body {
		t.Fatal("observer altered a JSON body")
	}
	track.end(http.StatusOK, false)

	record := onlyRecord(t, log)
	intValue(t, "prompt", record.PromptTokens, 10)
	intValue(t, "output", record.OutputTokens, 7)
	floatValue(t, "prompt rate", record.PromptPerSecond, 400)
	floatValue(t, "decode rate", record.DecodePerSecond, 20)
	if record.Finish != "length" || record.TTFTMS != nil {
		t.Fatalf("finish=%q ttft=%v; a buffered answer has no first-token time", record.Finish, record.TTFTMS)
	}
}

func TestInferenceObserverSurvivesALineTooLongToObserve(t *testing.T) {
	log := newInferenceLog(time.Now)
	track := log.begin(testModel(), "/v1/chat/completions", true, false)
	huge := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", maxObservedLine) + "\"}}]}\n\n"
	final := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n"
	payload := huge + final
	if out := observeThrough(t, track, "text/event-stream", &steppedReader{pieces: splitEvery(payload, 65536)}); out != payload {
		t.Fatal("observer altered a stream carrying an oversized line")
	}
	track.end(http.StatusOK, false)

	record := onlyRecord(t, log)
	intValue(t, "output", record.OutputTokens, 1)
	if record.Finish != "stop" {
		t.Fatalf("finish = %q; the line after the oversized one was not observed", record.Finish)
	}
}

func TestInferenceRatesRefuseNonFiniteAndNegativeValues(t *testing.T) {
	if finiteRate(math.NaN()) != nil || finiteRate(math.Inf(1)) != nil || finiteRate(-1) != nil {
		t.Fatal("finiteRate accepted a value that is not a speed")
	}
	log := newInferenceLog(time.Now)
	track := log.begin(testModel(), "/v1/chat/completions", false, false)
	observeThrough(t, track, "application/json", strings.NewReader(
		`{"choices":[{"finish_reason":"stop"}],"timings":{"prompt_n":-5,"prompt_ms":0,"prompt_per_second":-3,"predicted_n":2,"predicted_ms":-1}}`))
	track.end(http.StatusOK, false)
	record := onlyRecord(t, log)
	if record.PromptPerSecond != nil || record.DecodePerSecond != nil || record.PromptTokens != nil {
		t.Fatalf("invalid runtime numbers reached the record: %+v", record)
	}
	if _, err := json.Marshal(log.snapshot(gateSnapshot{})); err != nil {
		t.Fatalf("snapshot does not encode: %v", err)
	}
}

func TestInferenceLogKeepsABoundedNewestFirstHistory(t *testing.T) {
	clock := newFakeClock()
	log := newInferenceLog(clock.Now)
	for i := 0; i < inferenceHistoryLimit+6; i++ {
		track := log.begin(testModel(), "/v1/chat/completions", false, false)
		clock.Advance(time.Second)
		track.end(http.StatusOK, false)
	}
	snapshot := log.snapshot(gateSnapshot{})
	if len(snapshot.Recent) != inferenceHistoryLimit {
		t.Fatalf("recent = %d, want %d", len(snapshot.Recent), inferenceHistoryLimit)
	}
	if snapshot.Recent[0].StartedAt <= snapshot.Recent[1].StartedAt {
		t.Fatal("recent requests are not newest first")
	}
	if snapshot.Totals.Requests != int64(inferenceHistoryLimit+6) {
		t.Fatalf("totals = %d, want every request, not only the kept ones", snapshot.Totals.Requests)
	}
}

func TestInferenceLiveViewReportsPhaseAndSpeed(t *testing.T) {
	clock := newFakeClock()
	log := newInferenceLog(clock.Now)
	track := log.begin(testModel(), "/v1/chat/completions", true, true)
	clock.Advance(2 * time.Second)
	if live := log.snapshot(gateSnapshot{}).Live; len(live) != 1 || live[0].Phase != "prompt" || live[0].ElapsedMS != 2000 || !live[0].ColdStart {
		t.Fatalf("before the first token: %+v", live)
	}
	for i := 0; i < 11; i++ {
		track.observe(inferenceChunk{Choices: []inferenceChoice{{Delta: &inferenceDelta{Content: json.RawMessage(`"t"`)}}}})
		clock.Advance(100 * time.Millisecond)
	}
	live := log.snapshot(gateSnapshot{}).Live[0]
	if live.Phase != "generating" || live.OutputTokens != 11 || live.TTFTMS == nil || *live.TTFTMS != 2000 {
		t.Fatalf("while generating: %+v", live)
	}
	floatValue(t, "live rate", live.TokensPerSecond, 10/1.1)
	intValue(t, "ceiling", live.MaxOutputTokens, 8192)
}

// ---------------------------------------------------------------- through the edge

func telemetryRequest(t *testing.T, handler http.Handler) (*httptest.ResponseRecorder, inferenceSnapshot) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8091"+inferenceTelemetryPath, nil)
	request.Host = "127.0.0.1:8091"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var snapshot inferenceSnapshot
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
			t.Fatalf("decode telemetry: %v; body=%s", err, recorder.Body.String())
		}
	}
	return recorder, snapshot
}

func waitForRecords(t *testing.T, handler http.Handler, want int) inferenceSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, snapshot := telemetryRequest(t, handler)
		if len(snapshot.Recent) >= want && len(snapshot.Live) == 0 {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("telemetry never reached %d finished requests: %+v", want, snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const promptMarker = "PROMPT-MARKER-must-not-leak"
const answerMarker = "ANSWER-MARKER-must-not-leak"

func streamingUpstream(running string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, running)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"" + answerMarker + "\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"!\"},\"finish_reason\":\"stop\"}]," +
				"\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":30}}," +
				"\"timings\":{\"cache_n\":30,\"prompt_n\":12,\"prompt_ms\":30,\"predicted_n\":2,\"predicted_ms\":100}}\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, event)
			w.(http.Flusher).Flush()
		}
	})
}

func TestInferenceTelemetryReportsNumbersAndNeverContent(t *testing.T) {
	server, _ := newTestServer(t, streamingUpstream(`{"running":[]}`))
	body := []byte(`{"model":"local-coding","stream":true,"messages":[{"role":"user","content":"` + promptMarker + `"}]}`)
	response := dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), answerMarker) {
		t.Fatalf("proxy changed the answer: %d %s", response.Code, response.Body.String())
	}

	recorder, snapshot := telemetryRequest(t, server.ControlHandler())
	if recorder.Code != http.StatusOK {
		t.Fatalf("telemetry = %d, want 200 without any credential", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), promptMarker) || strings.Contains(recorder.Body.String(), answerMarker) {
		t.Fatalf("telemetry leaked request or response content: %s", recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if len(snapshot.Recent) != 1 {
		t.Fatalf("recent = %d, want 1", len(snapshot.Recent))
	}
	record := snapshot.Recent[0]
	if record.Model != "local-coding" || !record.Stream || !record.ColdStart || record.Finish != "stop" || record.Status != http.StatusOK {
		t.Fatalf("record = %+v", record)
	}
	intValue(t, "prompt", record.PromptTokens, 42)
	intValue(t, "cached", record.CachedTokens, 30)
	intValue(t, "output", record.OutputTokens, 2)
	floatValue(t, "decode rate", record.DecodePerSecond, 20)
	if snapshot.Totals.Requests != 1 || snapshot.Totals.TimedOutputTokens != 2 || snapshot.Totals.DecodeMS != 100 {
		t.Fatalf("totals = %+v", snapshot.Totals)
	}
}

func TestInferenceTelemetryPollingDoesNotFillTheEventLog(t *testing.T) {
	server, _ := newTestServer(t, streamingUpstream(`{"running":[]}`))
	for i := 0; i < 5; i++ {
		telemetryRequest(t, server.ControlHandler())
	}
	for _, item := range server.events.recent() {
		if item.Path == inferenceTelemetryPath {
			t.Fatalf("a successful telemetry read was recorded as an event: %+v", item)
		}
	}
	// A refused read is still an event worth keeping.
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8091"+inferenceTelemetryPath, nil)
	request.Host = "127.0.0.1:8091"
	recorder := httptest.NewRecorder()
	server.ControlHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST telemetry = %d, want 405", recorder.Code)
	}
	events := server.events.recent()
	if last := events[len(events)-1]; last.Path != inferenceTelemetryPath || last.Status != http.StatusMethodNotAllowed {
		t.Fatalf("refused telemetry read was not recorded: %+v", last)
	}
}

func TestInferenceTelemetryMarksWarmRequestsAndUpstreamErrors(t *testing.T) {
	server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"running":[{"model":"local-coding","state":"ready"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	dataRequest(t, server.DataHandler(), http.MethodPost, "/v1/chat/completions", []byte(`{"model":"local-coding","messages":[]}`))
	record := waitForRecords(t, server.ControlHandler(), 1).Recent[0]
	if record.ColdStart || record.Finish != "error" || record.Status != http.StatusInternalServerError || record.Stream {
		t.Fatalf("record = %+v", record)
	}
}

func TestInferenceTelemetryRecordsAClientThatWentAway(t *testing.T) {
	firstChunk := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/running" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"running":[]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(firstChunk)
		<-r.Context().Done()
	}))
	defer backend.Close()
	server, err := New(testConfig(backend.URL))
	if err != nil {
		t.Fatal(err)
	}
	edgeHTTP := httptest.NewServer(server.DataHandler())
	defer edgeHTTP.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, edgeHTTP.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"local-coding","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testInferenceToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := edgeHTTP.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	<-firstChunk
	if live := waitForLive(t, server.ControlHandler()); live.Phase != "generating" || live.OutputTokens != 1 {
		t.Fatalf("live = %+v, want one token generating", live)
	}
	cancel()
	_ = response.Body.Close()

	record := waitForRecords(t, server.ControlHandler(), 1).Recent[0]
	if record.Finish != "cancelled" {
		t.Fatalf("finish = %q, want cancelled", record.Finish)
	}
}

func waitForLive(t *testing.T, handler http.Handler) inferenceLive {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, snapshot := telemetryRequest(t, handler)
		if len(snapshot.Live) == 1 && snapshot.Live[0].OutputTokens > 0 {
			return snapshot.Live[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live request with a token: %+v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInferenceTelemetryCoversTheAnthropicGateway(t *testing.T) {
	server, _ := newTestServer(t, streamingUpstream(`{"running":[]}`))
	body := []byte(`{"model":"local-coding","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"` + promptMarker + `"}]}`)
	response := anthropicRequest(t, server.DataHandler(), http.MethodPost, "/v1/messages", body)
	if response.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", response.Code, response.Body.String())
	}
	recorder, snapshot := telemetryRequest(t, server.ControlHandler())
	if strings.Contains(recorder.Body.String(), promptMarker) || strings.Contains(recorder.Body.String(), answerMarker) {
		t.Fatalf("telemetry leaked content: %s", recorder.Body.String())
	}
	if len(snapshot.Recent) != 1 || snapshot.Recent[0].Route != "/v1/messages" || snapshot.Recent[0].Finish != "stop" {
		t.Fatalf("recent = %+v", snapshot.Recent)
	}
	intValue(t, "output", snapshot.Recent[0].OutputTokens, 2)
}

// The monitor tells a model being loaded from a slow prompt by the router's
// process state, so the status reports it - but only as one of the router's
// known words, never as whatever text the router happened to send.
func TestStatusReportsWhereTheModelProcessIs(t *testing.T) {
	for _, tc := range []struct {
		running string
		want    string
	}{
		{`{"running":[{"model":"local-coding","state":"starting"}]}`, "starting"},
		{`{"running":[{"model":"local-coding","state":"ready"}]}`, "ready"},
		{`{"running":[{"model":"local-coding","state":"<b>loading 40%</b>"}]}`, "unknown"},
		{`{"running":[]}`, "stopped"},
	} {
		server, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, tc.running)
		}))
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8091/api/v1/status", nil)
		request.Host = "127.0.0.1:8091"
		recorder := httptest.NewRecorder()
		server.ControlHandler().ServeHTTP(recorder, request)
		var status struct {
			ModelStatuses []struct {
				ID           string `json:"id"`
				ProcessState string `json:"process_state"`
			} `json:"model_statuses"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if len(status.ModelStatuses) != 1 || status.ModelStatuses[0].ProcessState != tc.want {
			t.Errorf("router said %s; model_statuses = %+v, want process_state %q", tc.running, status.ModelStatuses, tc.want)
		}
		if strings.Contains(recorder.Body.String(), "loading 40%") {
			t.Error("router text reached the status response")
		}
	}
}
