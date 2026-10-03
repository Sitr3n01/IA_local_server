package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// inferenceTelemetryPath is the control-plane route the browser monitor polls.
// It reports numbers about requests - token counts, timings, status - and never
// their content. Model IDs are manifest-bound and routes allowlist-bound, so
// nothing a client typed can reach this response.
const inferenceTelemetryPath = "/api/v1/inference"

const (
	// inferenceHistoryLimit bounds the ring of finished requests.
	inferenceHistoryLimit = 64
	// maxObservedLine bounds one buffered SSE line while its numbers are read.
	// A longer line still reaches the client byte for byte; only that
	// request's telemetry goes missing.
	maxObservedLine = 1 << 20
	// maxObservedBody bounds a buffered non-streaming response the same way.
	maxObservedBody = 8 << 20
)

type inferenceSnapshot struct {
	GeneratedAt string            `json:"generated_at"`
	Gate        gateSnapshot      `json:"gate"`
	Live        []inferenceLive   `json:"live"`
	Recent      []inferenceRecord `json:"recent"`
	Totals      inferenceTotals   `json:"totals"`
}

// inferenceLive is a request that holds an admission slot right now. Phase is
// "prompt" until the first token event (prefill, or a cold model load when
// ColdStart is set) and "generating" after it. A non-streaming request stays in
// "prompt": the edge sees nothing of it until the whole answer arrives.
type inferenceLive struct {
	Model           string   `json:"model"`
	Route           string   `json:"route"`
	Stream          bool     `json:"stream"`
	ColdStart       bool     `json:"cold_start"`
	Phase           string   `json:"phase"`
	StartedAt       string   `json:"started_at"`
	ElapsedMS       int64    `json:"elapsed_ms"`
	TTFTMS          *int64   `json:"ttft_ms"`
	OutputTokens    int      `json:"output_tokens"`
	TokensPerSecond *float64 `json:"tokens_per_second"`
	ContextTokens   *int     `json:"context_tokens"`
	MaxOutputTokens *int     `json:"max_output_tokens"`
}

// inferenceRecord is one finished request. Every count is the runtime's own
// figure when it reported one; OutputEstimated marks the one fallback, a
// streamed answer whose token events were counted because no usage arrived.
type inferenceRecord struct {
	StartedAt       string                `json:"started_at"`
	Model           string                `json:"model"`
	Route           string                `json:"route"`
	Stream          bool                  `json:"stream"`
	ColdStart       bool                  `json:"cold_start"`
	Status          int                   `json:"status"`
	Finish          string                `json:"finish"`
	PromptTokens    *int                  `json:"prompt_tokens"`
	CachedTokens    *int                  `json:"cached_tokens"`
	OutputTokens    *int                  `json:"output_tokens"`
	OutputEstimated bool                  `json:"output_estimated"`
	PromptPerSecond *float64              `json:"prompt_tokens_per_second"`
	DecodePerSecond *float64              `json:"decode_tokens_per_second"`
	TTFTMS          *int64                `json:"ttft_ms"`
	DurationMS      int64                 `json:"duration_ms"`
	ContextTokens   *int                  `json:"context_tokens"`
	Measurements    inferenceMeasurements `json:"measurements"`
}

// inferenceMeasurements names the evidence behind each displayed number. An
// empty value means the number is unavailable. In particular, an SSE event is
// not necessarily one token, so that fallback must never look like runtime
// usage.
type inferenceMeasurements struct {
	Prompt     string `json:"prompt"`
	Cache      string `json:"cache"`
	Output     string `json:"output"`
	PromptRate string `json:"prompt_rate"`
	DecodeRate string `json:"decode_rate"`
}

// inferenceTotals accumulates since the edge started. The timed pairs only
// count requests whose runtime reported both the tokens and the milliseconds,
// so an aggregate speed never divides a partial sum by a complete one.
type inferenceTotals struct {
	Since                 string  `json:"since"`
	Requests              int64   `json:"requests"`
	Failed                int64   `json:"failed"`
	PromptTokens          int64   `json:"prompt_tokens"`
	CachedTokens          int64   `json:"cached_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	EstimatedOutputEvents int64   `json:"estimated_output_events"`
	TimedPromptTokens     int64   `json:"timed_prompt_tokens"`
	PromptMS              float64 `json:"prompt_ms"`
	TimedOutputTokens     int64   `json:"timed_output_tokens"`
	DecodeMS              float64 `json:"decode_ms"`
}

type inferenceLog struct {
	mu     sync.Mutex
	now    func() time.Time
	since  time.Time
	nextID uint64
	live   map[uint64]*inferenceTrack
	recent []inferenceRecord // oldest first
	totals inferenceTotals
}

func newInferenceLog(now func() time.Time) *inferenceLog {
	return &inferenceLog{now: now, since: now(), live: make(map[uint64]*inferenceTrack)}
}

// inferenceTrack follows one admitted request until its handler returns. The
// observer updates it from the upstream stream; every field below the first
// blank line changes under log.mu.
type inferenceTrack struct {
	log       *inferenceLog
	id        uint64
	model     string
	route     string
	stream    bool
	coldStart bool
	context   *int
	maxOutput *int
	started   time.Time

	firstToken  time.Time
	tokenEvents int
	finish      string
	usage       inferenceUsage
	timings     inferenceTimings
	ended       bool
}

func (l *inferenceLog) begin(model Model, route string, stream, coldStart bool) *inferenceTrack {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	track := &inferenceTrack{
		log:       l,
		id:        l.nextID,
		model:     model.ID,
		route:     route,
		stream:    stream,
		coldStart: coldStart,
		context:   model.ContextTokens,
		maxOutput: outputCeiling(model),
		started:   l.now(),
	}
	l.live[track.id] = track
	return track
}

// outputCeiling is the most tokens the runtime will emit for this profile:
// n_predict when the manifest pins it, else the advertised output budget.
func outputCeiling(model Model) *int {
	if model.Profile.NPredict != nil {
		return model.Profile.NPredict
	}
	return model.Profile.MaxOutputTokens
}

// end retires the request into the history. status is what the client saw;
// canceled marks a client that went away before the response finished.
func (t *inferenceTrack) end(status int, canceled bool) {
	l := t.log
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.ended {
		return
	}
	t.ended = true
	delete(l.live, t.id)

	record := t.record(l.now(), status, canceled)
	if len(l.recent) == inferenceHistoryLimit {
		copy(l.recent, l.recent[1:])
		l.recent[len(l.recent)-1] = record
	} else {
		l.recent = append(l.recent, record)
	}

	l.totals.Requests++
	if record.Finish == "error" || record.Finish == "unknown" {
		l.totals.Failed++
	}
	if record.PromptTokens != nil {
		l.totals.PromptTokens += int64(*record.PromptTokens)
	}
	if record.CachedTokens != nil {
		l.totals.CachedTokens += int64(*record.CachedTokens)
	}
	if record.OutputTokens != nil {
		if record.OutputEstimated {
			l.totals.EstimatedOutputEvents += int64(*record.OutputTokens)
		} else {
			l.totals.OutputTokens += int64(*record.OutputTokens)
		}
	}
	if n, ms := t.timings.PromptN, t.timings.PromptMS; n != nil && ms != nil && *n >= 0 && finitePositive(*ms) {
		l.totals.TimedPromptTokens += int64(*n)
		l.totals.PromptMS += *ms
	}
	if n, ms := t.timings.PredictedN, t.timings.PredictedMS; n != nil && ms != nil && *n >= 0 && finitePositive(*ms) {
		l.totals.TimedOutputTokens += int64(*n)
		l.totals.DecodeMS += *ms
	}
}

func (t *inferenceTrack) record(now time.Time, status int, canceled bool) inferenceRecord {
	record := inferenceRecord{
		StartedAt:     t.started.UTC().Format(time.RFC3339Nano),
		Model:         t.model,
		Route:         t.route,
		Stream:        t.stream,
		ColdStart:     t.coldStart,
		Status:        status,
		DurationMS:    now.Sub(t.started).Milliseconds(),
		ContextTokens: t.context,
	}
	switch {
	case canceled:
		record.Finish = "cancelled"
	case status == 0:
		record.Finish = "unknown"
	case status >= http.StatusBadRequest:
		record.Finish = "error"
	case t.finish != "":
		record.Finish = t.finish
	default:
		record.Finish = "unknown"
	}

	usage, timings := t.usage, t.timings
	record.PromptTokens = firstCount(usage.PromptTokens, usage.InputTokens)
	if record.PromptTokens != nil {
		record.Measurements.Prompt = "runtime-usage"
	} else if record.PromptTokens = sumCounts(timings.PromptN, timings.CacheN); record.PromptTokens != nil {
		record.Measurements.Prompt = "runtime-timings"
	}
	record.CachedTokens = usage.cachedTokens()
	if record.CachedTokens != nil {
		record.Measurements.Cache = "runtime-usage"
	} else if timings.CacheN != nil {
		record.CachedTokens, record.Measurements.Cache = timings.CacheN, "runtime-timings"
	}
	record.OutputTokens = firstCount(usage.CompletionTokens, usage.OutputTokens)
	if record.OutputTokens != nil {
		record.Measurements.Output = "runtime-usage"
	} else if timings.PredictedN != nil {
		record.OutputTokens, record.Measurements.Output = timings.PredictedN, "runtime-timings"
	}
	if record.OutputTokens == nil && t.stream && t.tokenEvents > 0 {
		events := t.tokenEvents
		record.OutputTokens = &events
		record.OutputEstimated = true
		record.Measurements.Output = "stream-events-estimate"
	}

	record.PromptPerSecond = firstRate(timings.PromptPerSecond, rateOf(timings.PromptN, timings.PromptMS))
	record.DecodePerSecond = firstRate(timings.PredictedPerSecond, rateOf(timings.PredictedN, timings.PredictedMS))
	if record.PromptPerSecond != nil {
		record.Measurements.PromptRate = "runtime-timings"
	}
	if record.DecodePerSecond != nil {
		record.Measurements.DecodeRate = "runtime-timings"
	}
	if !t.firstToken.IsZero() {
		ttft := t.firstToken.Sub(t.started).Milliseconds()
		record.TTFTMS = &ttft
		if record.DecodePerSecond == nil && record.OutputTokens != nil && *record.OutputTokens > 1 {
			if seconds := now.Sub(t.firstToken).Seconds(); seconds > 0 {
				record.DecodePerSecond = finiteRate(float64(*record.OutputTokens-1) / seconds)
				if record.DecodePerSecond != nil {
					record.Measurements.DecodeRate = "wall-clock-estimate"
				}
			}
		}
	}
	return record
}

func (t *inferenceTrack) liveView(now time.Time) inferenceLive {
	view := inferenceLive{
		Model:           t.model,
		Route:           t.route,
		Stream:          t.stream,
		ColdStart:       t.coldStart,
		Phase:           "prompt",
		StartedAt:       t.started.UTC().Format(time.RFC3339Nano),
		ElapsedMS:       now.Sub(t.started).Milliseconds(),
		OutputTokens:    t.tokenEvents,
		ContextTokens:   t.context,
		MaxOutputTokens: t.maxOutput,
	}
	if !t.firstToken.IsZero() {
		view.Phase = "generating"
		ttft := t.firstToken.Sub(t.started).Milliseconds()
		view.TTFTMS = &ttft
		if t.tokenEvents > 1 {
			if seconds := now.Sub(t.firstToken).Seconds(); seconds > 0 {
				view.TokensPerSecond = finiteRate(float64(t.tokenEvents-1) / seconds)
			}
		}
	}
	return view
}

func (l *inferenceLog) snapshot(gate gateSnapshot) inferenceSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	live := make([]inferenceLive, 0, len(l.live))
	tracks := make([]*inferenceTrack, 0, len(l.live))
	for _, track := range l.live {
		tracks = append(tracks, track)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].id < tracks[j].id })
	for _, track := range tracks {
		live = append(live, track.liveView(now))
	}
	recent := make([]inferenceRecord, len(l.recent))
	for i := range l.recent {
		recent[i] = l.recent[len(l.recent)-1-i]
	}
	totals := l.totals
	totals.Since = l.since.UTC().Format(time.RFC3339Nano)
	return inferenceSnapshot{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Gate:        gate,
		Live:        live,
		Recent:      recent,
		Totals:      totals,
	}
}

func (s *Server) writeInferenceTelemetry(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, s.inference.snapshot(s.gate.snapshot()))
}

// ---------------------------------------------------------------- the observer

type inferenceTrackKey struct{}

func withInferenceTrack(ctx context.Context, track *inferenceTrack) context.Context {
	return context.WithValue(ctx, inferenceTrackKey{}, track)
}

func inferenceTrackFrom(ctx context.Context) *inferenceTrack {
	track, _ := ctx.Value(inferenceTrackKey{}).(*inferenceTrack)
	return track
}

// observeInferenceBody returns the body a proxy should read from. Without a
// track in the request context it is the upstream body itself; with one, the
// same bytes pass through an observer that only counts and measures.
func observeInferenceBody(ctx context.Context, response *http.Response) io.Reader {
	track := inferenceTrackFrom(ctx)
	if track == nil {
		return response.Body
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	return &inferenceObserver{
		source: response.Body,
		track:  track,
		sse:    strings.HasPrefix(contentType, "text/event-stream"),
	}
}

// inferenceObserver hands every byte to its reader unchanged. For an SSE
// stream it splits data lines and decodes each JSON event just far enough to
// see whether it carried a token and whether it reports usage, timings or a
// finish reason. For a JSON body it does the same once, at EOF, on a bounded
// copy. Nothing it decodes outlives the call that decoded it.
type inferenceObserver struct {
	source   io.Reader
	track    *inferenceTrack
	sse      bool
	pending  []byte
	skipping bool
	body     bytes.Buffer
	overflow bool
	done     bool
}

func (o *inferenceObserver) Read(p []byte) (int, error) {
	n, err := o.source.Read(p)
	if n > 0 {
		if o.sse {
			o.feedLines(p[:n])
		} else {
			o.feedBody(p[:n])
		}
	}
	if err != nil && !o.done {
		o.done = true
		if errors.Is(err, io.EOF) {
			o.flush()
		}
		o.pending, o.body = nil, bytes.Buffer{}
	}
	return n, err
}

func (o *inferenceObserver) feedLines(data []byte) {
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			if !o.skipping {
				if len(o.pending)+len(data) > maxObservedLine {
					o.pending, o.skipping = o.pending[:0], true
				} else {
					o.pending = append(o.pending, data...)
				}
			}
			return
		}
		segment := data[:newline]
		data = data[newline+1:]
		if o.skipping {
			// The tail of a line too long to observe ends here.
			o.skipping = false
			o.pending = o.pending[:0]
			continue
		}
		if len(o.pending)+len(segment) > maxObservedLine {
			o.pending = o.pending[:0]
			continue
		}
		line := segment
		if len(o.pending) > 0 {
			o.pending = append(o.pending, segment...)
			line = o.pending
		}
		o.observeLine(line)
		o.pending = o.pending[:0]
	}
}

func (o *inferenceObserver) feedBody(data []byte) {
	if o.overflow {
		return
	}
	if o.body.Len()+len(data) > maxObservedBody {
		o.overflow = true
		o.body = bytes.Buffer{}
		return
	}
	o.body.Write(data)
}

func (o *inferenceObserver) flush() {
	if o.sse {
		if len(o.pending) > 0 && !o.skipping {
			o.observeLine(o.pending)
		}
		return
	}
	if o.overflow || o.body.Len() == 0 {
		return
	}
	var chunk inferenceChunk
	if json.Unmarshal(o.body.Bytes(), &chunk) == nil {
		o.track.observe(chunk)
	}
}

func (o *inferenceObserver) observeLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || payload[0] != '{' {
		return // [DONE], and anything that is not an event object
	}
	var chunk inferenceChunk
	if json.Unmarshal(payload, &chunk) == nil {
		o.track.observe(chunk)
	}
}

// inferenceChunk is the union of the shapes the runtime emits: a Chat
// Completions chunk or body, and a Responses event or body. Text fields are
// kept raw so deciding "was there a token" never unescapes the text itself.
type inferenceChunk struct {
	Type              string                `json:"type"`
	Status            string                `json:"status"`
	IncompleteDetails *inferenceIncomplete  `json:"incomplete_details"`
	Choices           []inferenceChoice     `json:"choices"`
	Usage             *inferenceUsage       `json:"usage"`
	Timings           *inferenceTimings     `json:"timings"`
	Response          *inferenceResponseRef `json:"response"`
}

type inferenceChoice struct {
	Delta        *inferenceDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type inferenceDelta struct {
	Content          json.RawMessage `json:"content"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	ToolCalls        json.RawMessage `json:"tool_calls"`
}

type inferenceResponseRef struct {
	Status            string               `json:"status"`
	Usage             *inferenceUsage      `json:"usage"`
	IncompleteDetails *inferenceIncomplete `json:"incomplete_details"`
}

type inferenceIncomplete struct {
	Reason string `json:"reason"`
}

type inferenceUsage struct {
	PromptTokens        *int                   `json:"prompt_tokens"`
	CompletionTokens    *int                   `json:"completion_tokens"`
	InputTokens         *int                   `json:"input_tokens"`
	OutputTokens        *int                   `json:"output_tokens"`
	PromptTokensDetails *inferenceCachedTokens `json:"prompt_tokens_details"`
	InputTokensDetails  *inferenceCachedTokens `json:"input_tokens_details"`
}

type inferenceCachedTokens struct {
	CachedTokens *int `json:"cached_tokens"`
}

func (u inferenceUsage) cachedTokens() *int {
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		return u.PromptTokensDetails.CachedTokens
	}
	if u.InputTokensDetails != nil {
		return u.InputTokensDetails.CachedTokens
	}
	return nil
}

// inferenceTimings are llama-server's own per-request measurements.
type inferenceTimings struct {
	CacheN             *int     `json:"cache_n"`
	PromptN            *int     `json:"prompt_n"`
	PromptMS           *float64 `json:"prompt_ms"`
	PromptPerSecond    *float64 `json:"prompt_per_second"`
	PredictedN         *int     `json:"predicted_n"`
	PredictedMS        *float64 `json:"predicted_ms"`
	PredictedPerSecond *float64 `json:"predicted_per_second"`
}

func (t *inferenceTrack) observe(chunk inferenceChunk) {
	token := strings.HasSuffix(chunk.Type, ".delta")
	finish := ""
	for _, choice := range chunk.Choices {
		if choice.Delta != nil && (rawHasText(choice.Delta.Content) || rawHasText(choice.Delta.ReasoningContent) || rawHasItems(choice.Delta.ToolCalls)) {
			token = true
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finish = normalizeFinish(*choice.FinishReason)
		}
	}

	usage := chunk.Usage
	switch chunk.Type {
	case "response.completed", "response.incomplete", "response.failed":
		if chunk.Response != nil {
			finish = responsesFinish(chunk.Response.Status, chunk.Response.IncompleteDetails)
			if chunk.Response.Usage != nil {
				usage = chunk.Response.Usage
			}
		}
	case "error":
		finish = "error"
	case "":
		// A non-streaming Responses body carries its status at the top level.
		if chunk.Status != "" && len(chunk.Choices) == 0 {
			finish = responsesFinish(chunk.Status, chunk.IncompleteDetails)
		}
	}

	l := t.log
	l.mu.Lock()
	defer l.mu.Unlock()
	if token {
		if t.firstToken.IsZero() {
			t.firstToken = l.now()
		}
		t.tokenEvents++
	}
	if finish != "" {
		t.finish = finish
	}
	if usage != nil {
		t.usage = *usage
	}
	if chunk.Timings != nil {
		t.timings = *chunk.Timings
	}
}

func normalizeFinish(value string) string {
	switch value {
	case "stop", "length", "tool_calls":
		return value
	case "function_call":
		return "tool_calls"
	case "content_filter":
		return "filtered"
	default:
		return "other"
	}
}

func responsesFinish(status string, incomplete *inferenceIncomplete) string {
	switch status {
	case "completed":
		return "stop"
	case "incomplete":
		if incomplete != nil && strings.Contains(incomplete.Reason, "max_output") {
			return "length"
		}
		return "other"
	case "failed", "cancelled":
		return "error"
	default:
		return ""
	}
}

func rawHasText(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`))
}

func rawHasItems(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("[]"))
}

// ---------------------------------------------------------------- numbers

func firstCount(values ...*int) *int {
	for _, value := range values {
		if value != nil && *value >= 0 {
			return value
		}
	}
	return nil
}

func sumCounts(a, b *int) *int {
	if a == nil || b == nil || *a < 0 || *b < 0 {
		return nil
	}
	sum := *a + *b
	return &sum
}

func firstRate(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			if rate := finiteRate(*value); rate != nil {
				return rate
			}
		}
	}
	return nil
}

func rateOf(count *int, milliseconds *float64) *float64 {
	if count == nil || milliseconds == nil || *count <= 0 || !finitePositive(*milliseconds) {
		return nil
	}
	return finiteRate(float64(*count) / (*milliseconds / 1000))
}

// finiteRate refuses NaN, infinities and negatives: encoding/json cannot
// encode the first two, and none of them is a speed.
func finiteRate(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	return &value
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

// requestStreams reports the stream flag of an already validated JSON body.
func requestStreams(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Stream
}

// responseStatus is the status the client was sent, read back through the
// observe middleware's recorder; zero when nothing identifies it.
func responseStatus(w http.ResponseWriter) int {
	for {
		switch writer := w.(type) {
		case *statusWriter:
			return writer.status
		case interface{ Unwrap() http.ResponseWriter }:
			w = writer.Unwrap()
		default:
			return 0
		}
	}
}
