package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A request as llama-server writes it with verbose logging, with the numbers of
// a request that reused most of its prompt. There is no prompt or answer in any
// of it.
const cachedRequestLog = `[2026-09-29 14:08:11][DEBUG] 50.19.678.700 I slot get_availabl: id  0 | task -1 | selected slot by LCP similarity, f_sim_best = 0.980 (> 0.100 thold), f_keep = 0.964
[2026-09-29 14:08:11][DEBUG] 50.19.700.000 I slot launch_slot_: id  0 | task 2874 | processing task, is_child = 0
[2026-09-29 14:08:12][DEBUG] 50.20.500.000 I slot print_timing: id  0 | task 2874 | n_gen = 96, tg = 58.2 t/s, tg_1s = 57.9 t/s
[2026-09-29 14:08:16][DEBUG] 50.24.000.000 I slot print_timing: id  0 | task 2874 | prompt eval time =      88.10 ms /   148 tokens (    0.60 ms per token,  1679.91 tokens per second)
50.24.000.010 I slot print_timing: id  0 | task 2874 |        eval time =    4990.20 ms /   279 tokens (   17.89 ms per token,    55.91 tokens per second)
50.24.000.020 I slot print_timing: id  0 | task 2874 |       total time =    5078.30 ms /   427 tokens
50.24.000.030 I slot print_timing: id  0 | task 2874 |    graphs reused =         84
[2026-09-29 14:08:16][DEBUG] 50.24.000.040 I slot      release: id  0 | task 2874 | stop processing: n_tokens = 7831, truncated = 0
`

const coldRequestLog = `[2026-09-29 13:18:02][DEBUG] 0.10.083.994 I slot launch_slot_: id  0 | task 84 | processing task, is_child = 0
[2026-09-29 13:18:02][DEBUG] 0.10.242.525 I slot print_timing: id  0 | task 84 | prompt eval time =      44.63 ms /    52 tokens (    0.86 ms per token,  1165.19 tokens per second)
0.10.242.530 I slot print_timing: id  0 | task 84 |        eval time =     113.87 ms /     8 tokens (   16.27 ms per token,    61.47 tokens per second)
0.10.242.531 I slot print_timing: id  0 | task 84 |       total time =     158.50 ms /    60 tokens
[2026-09-29 13:18:02][DEBUG] 0.10.242.551 I slot      release: id  0 | task 84 | stop processing: n_tokens = 59, truncated = 0
`

func feedAll(parser *llamaLogParser, text string) []loggedRequest {
	var out []loggedRequest
	for _, line := range strings.Split(text, "\n") {
		if request := parser.feed(strings.TrimRight(line, "\r")); request != nil {
			out = append(out, *request)
		}
	}
	return out
}

func TestLogParserDerivesThePromptAndTheCacheFromWhatTheServerCounted(t *testing.T) {
	requests := feedAll(newLlamaLogParser(time.UTC), cachedRequestLog)
	if len(requests) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	r := requests[0]
	if r.Task != 2874 || r.Slot != 0 || r.PromptEvaluated != 148 || r.Output != 279 || r.Truncated {
		t.Fatalf("request = %+v", r)
	}
	if r.PromptTPS != 1679.91 || r.DecodeTPS != 55.91 || r.PromptMS != 88.10 || r.TotalMS != 5078.30 {
		t.Fatalf("rates = %+v", r)
	}
	// The slot held 7831 tokens at the end, 279 of them generated (and the last
	// one not yet counted): the whole prompt was 7553, of which 148 were new.
	if r.promptTokens() != 7553 || r.cachedTokens() != 7405 {
		t.Fatalf("prompt %d cached %d, want 7553 and 7405", r.promptTokens(), r.cachedTokens())
	}
	if want := time.Date(2026, 9, 29, 14, 8, 11, 0, time.UTC); !r.At.Equal(want) {
		t.Fatalf("started at %v, want %v", r.At, want)
	}
}

func TestLogParserCountsAColdRequestAsFullyProcessed(t *testing.T) {
	requests := feedAll(newLlamaLogParser(time.UTC), coldRequestLog)
	if len(requests) != 1 || requests[0].promptTokens() != 52 || requests[0].cachedTokens() != 0 {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestLogParserReadsOlderBuildsAndIgnoresEverythingElse(t *testing.T) {
	old := strings.ReplaceAll(coldRequestLog, "n_tokens = 59", "n_past = 59")
	noise := "[2026-09-29 13:18:02][DEBUG] 0.10.000.000 W srv operator (): unauthorized: Invalid API Key\n" +
		"[2026-09-29 13:18:02][DEBUG] 0.10.000.001 I srv  load_model: loading model 'C:\\somewhere\\private\\model.gguf'\n"
	parser := newLlamaLogParser(time.UTC)
	requests := feedAll(parser, noise+old+noise)
	if len(requests) != 1 || requests[0].cachedTokens() != 0 {
		t.Fatalf("requests = %+v", requests)
	}
	if len(parser.pending) != 0 {
		t.Fatalf("a finished request stayed pending: %+v", parser.pending)
	}
	// A block that never finishes is bounded, and a release without a launch is
	// not a request.
	parser = newLlamaLogParser(time.UTC)
	for task := 0; task < logPendingLimit*3; task++ {
		parser.feed("slot launch_slot_: id  0 | task " + strconv.Itoa(task+100) + " | processing task, is_child = 0")
	}
	if len(parser.pending) > logPendingLimit {
		t.Fatalf("%d requests pending, limit %d", len(parser.pending), logPendingLimit)
	}
	if request := parser.feed("slot      release: id  0 | task 999999 | stop processing: n_tokens = 5, truncated = 0"); request != nil {
		t.Fatalf("a release without a launch became a request: %+v", request)
	}
}

func TestRecordFromLogCarriesEverythingThePageShows(t *testing.T) {
	request := feedAll(newLlamaLogParser(time.UTC), cachedRequestLog)[0]
	source := inferenceSource{ID: "lm-studio-1", Label: "LM Studio / Bionic", Models: []sourceModel{{ID: "gemma"}}}
	record := recordFromLog(source, request)
	if record.SourceID != "lm-studio-1" || record.Model != "gemma" || record.Measured != "server-log" || record.Basis != "log" {
		t.Fatalf("record = %+v", record)
	}
	if *record.PromptTokens != 7553 || *record.CachedTokens != 7405 || *record.OutputTokens != 279 || *record.ContextTokens != 7831 {
		t.Fatalf("tokens = %d %d %d %d", *record.PromptTokens, *record.CachedTokens, *record.OutputTokens, *record.ContextTokens)
	}
	if *record.TokensPerSecond != 55.91 || *record.PromptTPS != 1679.91 || *record.TTFTMS != 88 || record.DurationMS != 5078 {
		t.Fatalf("rates = %v %v %v %d", *record.TokensPerSecond, *record.PromptTPS, *record.TTFTMS, record.DurationMS)
	}
	if record.Measurements.Prompt != "server-log-derived" || record.Measurements.Cache != "server-log-derived" ||
		record.Measurements.Output != "server-log" || record.Measurements.DecodeRate != "server-log" {
		t.Fatalf("log origins = %+v", record.Measurements)
	}
}

func writeLog(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLog(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestTailerReadsWhatIsAppendedAndHoldsAHalfWrittenLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-09-29.1.log")
	writeLog(t, path, "")
	tailer := newLogTailer(path, time.UTC)
	if requests, err := tailer.poll(); err != nil || len(requests) != 0 {
		t.Fatalf("empty log: %v %v", requests, err)
	}
	// The request arrives in two writes, the second cutting a line in half.
	cut := strings.Index(cachedRequestLog, "graphs reused") - 20
	appendLog(t, path, cachedRequestLog[:cut])
	if requests, _ := tailer.poll(); len(requests) != 0 {
		t.Fatalf("a request was reported before it finished: %+v", requests)
	}
	appendLog(t, path, cachedRequestLog[cut:])
	requests, err := tailer.poll()
	if err != nil || len(requests) != 1 || requests[0].Task != 2874 {
		t.Fatalf("requests = %+v err=%v", requests, err)
	}
	if again, _ := tailer.poll(); len(again) != 0 {
		t.Fatalf("the same request was reported twice: %+v", again)
	}
	if !tailer.parser.seenTiming {
		t.Fatal("timing lines were seen but not noted")
	}
}

func TestTailerStartsFromTheEndOfALargeLogAndSurvivesRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-09-29.1.log")
	filler := strings.Repeat("[2026-09-29 12:00:00][DEBUG] 0.00.000.000 W srv operator (): unauthorized: Invalid API Key\n", (logFirstWindow/90)+1000)
	writeLog(t, path, coldRequestLog+filler+cachedRequestLog)
	tailer := newLogTailer(path, time.UTC)
	requests, err := tailer.poll()
	if err != nil {
		t.Fatal(err)
	}
	// Only the end of the file was read, so the request at the top is beyond it.
	if len(requests) != 1 || requests[0].Task != 2874 {
		t.Fatalf("requests = %+v", requests)
	}
	// The log is replaced by a shorter one: reading starts again.
	writeLog(t, path, coldRequestLog)
	requests, err = tailer.poll()
	if err != nil || len(requests) != 1 || requests[0].Task != 84 {
		t.Fatalf("after rotation: %+v err=%v", requests, err)
	}
	if _, err := newLogTailer(filepath.Join(t.TempDir(), "absent.log"), time.UTC).poll(); err == nil {
		t.Fatal("a missing log was not an error")
	}
	if _, err := newLogTailer(t.TempDir(), time.UTC).poll(); err == nil {
		t.Fatal("a directory was read as a log")
	}
}

func TestLatestServerLogPicksTheNewestMonthDayAndRotation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"2026-08/2026-08-31.1.log", "2026-09/2026-09-28.3.log", "2026-09/2026-09-29.1.log", "2026-09/2026-09-29.2.log", "2026-09/notes.txt", "2026-09/2026-09-29.log"} {
		writeLog(t, filepath.Join(root, filepath.FromSlash(name)), "x")
	}
	if got, want := latestServerLog(root), filepath.Join(root, "2026-09", "2026-09-29.2.log"); got != want {
		t.Fatalf("latest = %s, want %s", got, want)
	}
	if latestServerLog(filepath.Join(root, "absent")) != "" || latestServerLog(t.TempDir()) != "" {
		t.Fatal("an absent or empty folder produced a log")
	}
}

func lmStudioHost(t *testing.T, serverPort uint16, logPath string) *fakeHost {
	t.Helper()
	return &fakeHost{
		table:  map[uint32]procEntry{10: {PID: 10, Exe: "Bionic.exe"}, 20: {PID: 20, PPID: 10, Exe: "llama-server.exe"}},
		paths:  map[uint32]string{20: `C:\Users\me\.lmstudio\extensions\backends\llama.cpp-vulkan\llama-server.exe`},
		listen: []tcpListener{loopbackListener(serverPort, 20)},
		launch: map[uint32]launchInfo{20: parseLaunchArguments([]string{"llama-server", "--model", `C:\m\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf`, "-c", "131072"})},
		logs:   map[string]string{toolLMStudio: logPath},
	}
}

func TestDiscoveryMetersAProtectedServerFromItsLogAndSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "2026-09-29.1.log")
	writeLog(t, path, coldRequestLog+cachedRequestLog)
	host := lmStudioHost(t, portOf(t, server), path)
	gpu := []gpuProcess{{PID: 20, Name: "llama-server.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 7400}}
	d := newTestDiscoverer(host)

	sources := d.discover(context.Background(), gpu)
	if len(sources) != 1 || sources[0].Metering != "server-log" || sources[0].MeteringNote != "" {
		t.Fatalf("source = %+v", sources)
	}
	if coverage := coverageOf("ok", sources); coverage.ExternalMeasured != 1 || coverage.ExternalActivityOnly != 0 {
		t.Fatalf("a protected but logged source was marked unmeasured: %+v", coverage)
	}
	records := d.loggedRecords()
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	newest := records[0]
	if newest.Measured != "server-log" || newest.Model != "gemma-4-12B-it-qat-UD-Q4_K_XL" || *newest.CachedTokens != 7405 || *newest.PromptTokens != 7553 {
		t.Fatalf("newest = %+v", newest)
	}
	// Looking again reads nothing new and adds nothing.
	d.discover(context.Background(), gpu)
	if len(d.loggedRecords()) != 2 {
		t.Fatalf("a second look duplicated records: %+v", d.loggedRecords())
	}
	appendLog(t, path, strings.ReplaceAll(coldRequestLog, "task 84", "task 85"))
	d.discover(context.Background(), gpu)
	if got := d.loggedRecords(); len(got) != 3 {
		t.Fatalf("a new request was not picked up: %+v", got)
	}
}

func TestDiscoveryExplainsWhyThereAreNoPerRequestNumbers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	gpu := []gpuProcess{{PID: 20, Name: "llama-server.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 7400}}

	noLog := lmStudioHost(t, portOf(t, server), "")
	if sources := newTestDiscoverer(noLog).discover(context.Background(), gpu); sources[0].Metering != "" || !strings.Contains(sources[0].MeteringNote, "log do servidor") {
		t.Fatalf("no log: %+v", sources[0])
	}
	// A log with no request in it yet is not "metered", and says it is waiting.
	path := filepath.Join(t.TempDir(), "2026-09-29.1.log")
	writeLog(t, path, "[2026-09-29 13:17:52][DEBUG] 0.00.119.963 I srv  llama_server: initializing ...\n")
	waiting := lmStudioHost(t, portOf(t, server), path)
	if sources := newTestDiscoverer(waiting).discover(context.Background(), gpu); sources[0].Metering != "" || !strings.Contains(sources[0].MeteringNote, "ainda não registrou") {
		t.Fatalf("empty log: %+v", sources[0])
	}
	// A server nobody can meter says how to get numbers.
	other := &fakeHost{table: map[uint32]procEntry{30: {PID: 30, Exe: "ollama.exe"}}}
	ollama := newTestDiscoverer(other).discover(context.Background(), []gpuProcess{{PID: 30, Name: "ollama.exe", Tool: toolOllama, VRAMDedicatedMiB: 3000}})
	if len(ollama) != 1 || !strings.Contains(ollama[0].MeteringNote, "edge") {
		t.Fatalf("ollama: %+v", ollama)
	}
}

func TestAProtectedServerIsNotAskedAgainEveryTwoSeconds(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	host := lmStudioHost(t, portOf(t, server), "")
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	d := newTestDiscoverer(host)
	d.now = func() time.Time { return clock }
	gpu := []gpuProcess{{PID: 20, Name: "llama-server.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 7400}}

	for range 5 {
		d.discover(context.Background(), gpu)
		clock = clock.Add(2 * time.Second)
	}
	if hits.Load() != 1 {
		t.Fatalf("a protected server was asked %d times in ten seconds, want 1", hits.Load())
	}
	// Still reported as protected while it is not being asked.
	if sources := d.discover(context.Background(), gpu); len(sources) != 1 || sources[0].Status != sourceProtected {
		t.Fatalf("source = %+v", sources)
	}
	clock = clock.Add(protectedProbeTTL)
	d.discover(context.Background(), gpu)
	if hits.Load() != 2 {
		t.Fatalf("after the retry period it was asked %d times, want 2", hits.Load())
	}
}

func TestAnAnsweringServerIsStillReadEveryPass(t *testing.T) {
	var hits atomic.Int64
	server := router(map[string]string{"/api/v0/models": lmStudioModels}, &hits)
	defer server.Close()
	host := lmStudioHost(t, portOf(t, server), "")
	d := newTestDiscoverer(host)
	for range 3 {
		d.discover(context.Background(), []gpuProcess{{PID: 20, Tool: toolLMStudio, VRAMDedicatedMiB: 7400}})
	}
	if hits.Load() != 3 {
		t.Fatalf("a working API was read %d times in three passes, want 3", hits.Load())
	}
}

func TestLoggedRequestsReplaceTheGPUEstimateOfTheSameActivity(t *testing.T) {
	sampler := &fakeSampler{reading: hardwareSample{PowerW: ptr(200)}}
	collector, clock := newTestCollector(t, fakeEdge(t, serveInference(inferenceJSON)), sampler)
	started := clock.Now()
	collector.sources = &discoverer{records: []externalRecord{{
		SourceID: "lm-studio-20", Label: "LM Studio / Bionic", Measured: "server-log",
		StartedAt: started.Add(2 * time.Second).UTC().Format(time.RFC3339Nano), DurationMS: 4000,
		PromptTokens: int64Ptr(100), OutputTokens: int64Ptr(50),
	}}}
	ctx := context.Background()
	collector.refreshStatus(ctx)
	for range 10 {
		collector.tick(ctx)
		clock.Advance(time.Second)
	}
	// An estimate of the same source that overlaps the logged request.
	collector.bursts.done = []externalRecord{
		{SourceID: "lm-studio-20", Label: "LM Studio / Bionic", StartedAt: started.Add(3 * time.Second).UTC().Format(time.RFC3339Nano), DurationMS: 3000},
		{SourceID: "other-1", Label: "Other", StartedAt: started.Add(7 * time.Second).UTC().Format(time.RFC3339Nano), DurationMS: 1000},
	}
	got := collector.externalActivity()
	if len(got) != 2 {
		t.Fatalf("records = %+v; the estimate of a logged request must be dropped and the other kept", got)
	}
	if got[0].SourceID != "other-1" || got[1].SourceID != "lm-studio-20" {
		t.Fatalf("order = %s, %s; want newest first", got[0].SourceID, got[1].SourceID)
	}
	logged := got[1]
	// Energy was integrated from the board's power over the request's four
	// seconds: readings at 2, 3, 4, 5 and 6 s fall in it (one per second).
	if logged.EnergyWh == nil || logged.PeakPowerW == nil || *logged.PeakPowerW != 200 || *logged.EnergyWh < 0.19 || *logged.EnergyWh > 0.30 {
		t.Fatalf("energy = %v peak = %v", logged.EnergyWh, logged.PeakPowerW)
	}
}

func TestAMeteredSourceGetsNoGPUEstimate(t *testing.T) {
	var tracker burstTracker
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	metered := working("a", 90, nil)
	metered.Metering = "server-log"
	unmetered := working("b", 90, nil)
	for range 8 {
		clock = clock.Add(time.Second)
		tracker.observe(clock, []inferenceSource{metered, unmetered}, ptr(100))
	}
	for range 5 {
		clock = clock.Add(time.Second)
		tracker.observe(clock, nil, nil)
	}
	records := tracker.records()
	if len(records) != 1 || records[0].SourceID != "b" {
		t.Fatalf("records = %+v; only the unmetered source may be estimated", records)
	}
}

// A server's own log has no wall-clock time on its lines; this is what
// llama-server writes to --log-file itself.
var unstampedRequestLog = strings.NewReplacer("[2026-09-29 13:18:02][DEBUG] ", "").Replace(coldRequestLog)

func TestAnUnstampedLogDatesNewRequestsByWhenTheyWereReadAndNeverTheHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llama.log")
	writeLog(t, path, unstampedRequestLog)
	tailer := newLogTailer(path, time.UTC)
	clock := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	tailer.now = func() time.Time { return clock }

	if requests, err := tailer.poll(); err != nil || len(requests) != 0 {
		t.Fatalf("the history of an undated log was reported: %+v err=%v", requests, err)
	}
	if !tailer.parser.seenTiming {
		t.Fatal("the log carries timing lines, which the first look should have noticed")
	}
	clock = clock.Add(time.Minute)
	appendLog(t, path, strings.ReplaceAll(unstampedRequestLog, "task 84", "task 85"))
	requests, err := tailer.poll()
	if err != nil || len(requests) != 1 || requests[0].Task != 85 {
		t.Fatalf("requests = %+v err=%v", requests, err)
	}
	// The request began 158.5 ms before the moment it was read.
	if delta := clock.Sub(requests[0].At); delta < 100*time.Millisecond || delta > time.Second {
		t.Fatalf("dated %v, read at %v", requests[0].At, clock)
	}
}
