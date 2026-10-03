package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const lmStudioModels = `{"object":"list","data":[
 {"id":"qwen/qwen3-4b","object":"model","type":"llm","publisher":"qwen","arch":"qwen3","compatibility_type":"gguf","quantization":"Q4_K_M","state":"loaded","max_context_length":262144,"loaded_context_length":32768},
 {"id":"google/gemma-3n","object":"model","type":"llm","arch":"gemma3n","quantization":"Q8_0","state":"not-loaded","max_context_length":32768},
 {"id":"text-embedding-nomic","object":"model","type":"embeddings","arch":"nomic-bert","quantization":"Q4_K_M","state":"loaded","max_context_length":2048,"loaded_context_length":2048}]}`

const ollamaProcesses = `{"models":[{"name":"llama3.2:3b","model":"llama3.2:3b","size":3987000000,"digest":"abc",
 "details":{"parent_model":"","format":"gguf","family":"llama","families":["llama"],"parameter_size":"3.2B","quantization_level":"Q4_K_M"},
 "expires_at":"2026-09-29T12:00:00Z","size_vram":3987000000,"context_length":4096}]}`

const llamaProps = `{"default_generation_settings":{"n_ctx":262144,"params":{"temperature":1.0}},"total_slots":1,
 "model_path":"C:\\Users\\me\\models\\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf","build_info":"b10225-d2a74a4a3"}`

const openAIModels = `{"object":"list","data":[{"id":"my-model","object":"model"},{"id":"other","object":"model"}]}`

func TestParseLMStudioModelsSeparatesWhatIsLoadedFromWhatIsInstalled(t *testing.T) {
	models, library, ok := parseLMStudioModels([]byte(lmStudioModels))
	if !ok || len(models) != 2 {
		t.Fatalf("models = %+v ok=%v", models, ok)
	}
	if len(library) != 1 || library[0].ID != "google/gemma-3n" || library[0].Quantization != "Q8_0" ||
		library[0].Loaded == nil || *library[0].Loaded || library[0].ContextLoaded != nil ||
		library[0].ContextMax == nil || *library[0].ContextMax != 32768 {
		t.Fatalf("library = %+v", library)
	}
	first := models[0]
	if first.ID != "qwen/qwen3-4b" || first.Arch != "qwen3" || first.Quantization != "Q4_K_M" || first.Type != "llm" ||
		first.ContextLoaded == nil || *first.ContextLoaded != 32768 || first.ContextMax == nil || *first.ContextMax != 262144 ||
		first.Loaded == nil || !*first.Loaded {
		t.Fatalf("first = %+v", first)
	}
	if models[1].Type != "embeddings" {
		t.Fatalf("second = %+v", models[1])
	}
	if _, _, ok := parseLMStudioModels([]byte(`{"data":[{"id":"x"}]}`)); ok {
		t.Fatal("a list without any model state was taken for LM Studio")
	}
	if models, library, ok := parseLMStudioModels([]byte(`{"data":[]}`)); !ok || len(models) != 0 || len(library) != 0 {
		t.Fatalf("an empty library = %+v %+v ok=%v", models, library, ok)
	}
	for _, bad := range []string{``, `[]`, `not json`, `{"data":"x"}`} {
		if _, _, ok := parseLMStudioModels([]byte(bad)); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
	// A large library is bounded, and loaded models are never crowded out by it.
	var many strings.Builder
	many.WriteString(`{"data":[{"id":"held","state":"loaded"}`)
	for index := 0; index < maxLibraryModels+30; index++ {
		many.WriteString(`,{"id":"m` + strconv.Itoa(index) + `","state":"not-loaded"}`)
	}
	many.WriteString(`]}`)
	loaded, bounded, _ := parseLMStudioModels([]byte(many.String()))
	if len(loaded) != 1 || len(bounded) != maxLibraryModels {
		t.Fatalf("loaded %d, library %d; want 1 and %d", len(loaded), len(bounded), maxLibraryModels)
	}
}

const ollamaTags = `{"models":[
 {"name":"llama3.2:3b","model":"llama3.2:3b","size":2019393189,"details":{"family":"llama","parameter_size":"3.2B","quantization_level":"Q4_K_M"}},
 {"name":"qwen3:8b","model":"qwen3:8b","size":5225388164,"details":{"family":"qwen3","parameter_size":"8.2B","quantization_level":"Q4_K_M"}}]}`

func TestParseOllamaTagsAndTheLibraryExcludesWhatIsLoaded(t *testing.T) {
	library := parseOllamaTags([]byte(ollamaTags))
	if len(library) != 2 || library[1].ID != "qwen3:8b" || library[1].Params != "8.2B" || library[1].Quantization != "Q4_K_M" ||
		library[1].SizeBytes == nil || library[1].Loaded == nil || *library[1].Loaded {
		t.Fatalf("library = %+v", library)
	}
	loaded, _ := parseOllamaProcesses([]byte(ollamaProcesses))
	rest := withoutLoaded(library, loaded)
	if len(rest) != 1 || rest[0].ID != "qwen3:8b" {
		t.Fatalf("a loaded model was listed twice: %+v", rest)
	}
	if parseOllamaTags([]byte(`not json`)) != nil || len(parseOllamaTags([]byte(`{"models":[]}`))) != 0 {
		t.Fatal("malformed or empty tags misparsed")
	}
}

func TestParseOllamaProcesses(t *testing.T) {
	models, ok := parseOllamaProcesses([]byte(ollamaProcesses))
	if !ok || len(models) != 1 {
		t.Fatalf("models = %+v ok=%v", models, ok)
	}
	m := models[0]
	if m.ID != "llama3.2:3b" || m.Arch != "llama" || m.Params != "3.2B" || m.Quantization != "Q4_K_M" ||
		m.ContextLoaded == nil || *m.ContextLoaded != 4096 || m.VRAMMiB == nil || int(*m.VRAMMiB) != 3802 {
		t.Fatalf("model = %+v", m)
	}
	if models, ok := parseOllamaProcesses([]byte(`{"models":[]}`)); !ok || len(models) != 0 {
		t.Fatalf("nothing loaded = %+v ok=%v", models, ok)
	}
	if _, ok := parseOllamaProcesses([]byte(`{"data":[]}`)); ok {
		t.Fatal("an object without models was taken for Ollama")
	}
}

func TestParseLlamaPropsKeepsOnlyTheFileName(t *testing.T) {
	models, ok := parseLlamaProps([]byte(llamaProps))
	if !ok || len(models) != 1 {
		t.Fatalf("models = %+v ok=%v", models, ok)
	}
	m := models[0]
	if m.ID != "gemma-4-12B-it-qat-UD-Q4_K_XL" || m.Quantization != "Q4_K_XL" || m.ContextLoaded == nil || *m.ContextLoaded != 262144 {
		t.Fatalf("model = %+v", m)
	}
	if strings.Contains(m.ID, "Users") || strings.Contains(m.ID, `\`) {
		t.Fatalf("a directory reached the model name: %q", m.ID)
	}
	aliased, _ := parseLlamaProps([]byte(`{"total_slots":2,"model_alias":"my-alias","model_path":"x/y/model-Q6_K.gguf"}`))
	if aliased[0].ID != "my-alias" || aliased[0].Quantization != "Q6_K" {
		t.Fatalf("aliased = %+v", aliased)
	}
	// llama.cpp fills the alias with the whole path when none was set.
	pathAlias, _ := parseLlamaProps([]byte(`{"total_slots":1,"model_alias":"C:\\Users\\me\\models\\big-Q4_K_M.gguf","model_path":"C:\\Users\\me\\models\\big-Q4_K_M.gguf"}`))
	if pathAlias[0].ID != "big-Q4_K_M" || strings.ContainsAny(pathAlias[0].ID, `/\`) {
		t.Fatalf("a path alias leaked a directory: %+v", pathAlias)
	}
	if _, ok := parseLlamaProps([]byte(`{"hello":"world"}`)); ok {
		t.Fatal("an unrelated object was taken for llama.cpp")
	}
}

func TestQuantFromName(t *testing.T) {
	for name, want := range map[string]string{
		"gemma-4-12B-it-qat-UD-Q4_K_XL.gguf": "Q4_K_XL",
		"Qwen3.8-27B-UD-IQ4_XS.gguf":         "IQ4_XS",
		"model-Q8_0.gguf":                    "Q8_0",
		"model-BF16.gguf":                    "BF16",
		"llama-7b.gguf":                      "",
	} {
		if got := quantFromName(name); got != want {
			t.Errorf("quantFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseLlamaSlotsReadsBothShapesOfNextToken(t *testing.T) {
	current := parseLlamaSlots([]byte(`[{"id":0,"is_processing":true,"next_token":{"n_decoded":120}},{"id":1,"is_processing":false,"next_token":{"n_decoded":0}}]`))
	if current == nil || !current.processing || current.decoded[0] != 120 || len(current.decoded) != 2 {
		t.Fatalf("current shape = %+v", current)
	}
	older := parseLlamaSlots([]byte(`[{"id":0,"is_processing":false,"next_token":[{"n_decoded":7}]}]`))
	if older == nil || older.processing || older.decoded[0] != 7 {
		t.Fatalf("older shape = %+v", older)
	}
	for _, bad := range []string{``, `{}`, `[]`, `not json`} {
		if parseLlamaSlots([]byte(bad)) != nil {
			t.Errorf("%q gave slot state", bad)
		}
	}
}

func TestParseOpenAIModelsCannotSayWhichAreLoaded(t *testing.T) {
	models, ok := parseOpenAIModels([]byte(openAIModels))
	if !ok || len(models) != 2 || models[0].Loaded != nil {
		t.Fatalf("models = %+v ok=%v", models, ok)
	}
	if _, ok := parseOpenAIModels([]byte(`{"nope":1}`)); ok {
		t.Fatal("an object without data was accepted")
	}
}

func TestLMStudioStateFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if port, pid, ok := lmStudioStateFile(write("good.json", `{"host":"127.0.0.1","pid":37260,"port":41343}`)); !ok || port != 41343 || pid != 37260 {
		t.Fatalf("good = %d %d %v", port, pid, ok)
	}
	for name, content := range map[string]string{
		"lan.json":     `{"host":"192.168.1.20","pid":1,"port":1234}`,
		"badport.json": `{"host":"127.0.0.1","pid":1,"port":70000}`,
		"nopid.json":   `{"host":"127.0.0.1","port":1234}`,
		"junk.json":    `not json`,
		"big.json":     `{"host":"127.0.0.1","pid":1,"port":1234,"pad":"` + strings.Repeat("x", 5000) + `"}`,
	} {
		if _, _, ok := lmStudioStateFile(write(name, content)); ok {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, _, ok := lmStudioStateFile(filepath.Join(dir, "missing.json")); ok {
		t.Error("a missing file was accepted")
	}
}

// ------------------------------------------------------------ discovery

type fakeHost struct {
	table   map[uint32]procEntry
	paths   map[uint32]string
	listen  []tcpListener
	foreign map[uint32]bool
	lmPort  uint16
	lmPID   uint32
	// launch, ram and started answer the questions about a process's launch
	// arguments, memory and start time; a process absent from them has none.
	launch  map[uint32]launchInfo
	ram     map[uint32]float64
	started map[uint32]time.Time
	// logs answers serverLog by tool name.
	logs map[string]string
}

func (h *fakeHost) processes() map[uint32]procEntry { return h.table }
func (h *fakeHost) imagePath(pid uint32, _ string) string {
	return h.paths[pid]
}
func (h *fakeHost) listeners() []tcpListener { return h.listen }
func (h *fakeHost) sameUser(pid uint32) bool { return !h.foreign[pid] }
func (h *fakeHost) lmStudioState() (uint16, uint32, bool) {
	return h.lmPort, h.lmPID, h.lmPort != 0
}

func portOf(t *testing.T, server *httptest.Server) uint16 {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}

func loopbackListener(port uint16, pid uint32) tcpListener {
	return tcpListener{Addr: netip.MustParseAddr("127.0.0.1"), Port: port, PID: pid}
}

// router serves a fixed set of routes and counts every request it receives.
func router(routes map[string]string, hits *atomic.Int64) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	return server
}

func newTestDiscoverer(host *fakeHost, skip ...uint16) *discoverer {
	d := newDiscoverer(host, skip, 1)
	d.now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	return d
}

func find(sources []inferenceSource, kind string) *inferenceSource {
	for index := range sources {
		if sources[index].Kind == kind {
			return &sources[index]
		}
	}
	return nil
}

func TestDiscoveryFingerprintsEachKindOfServer(t *testing.T) {
	lm := router(map[string]string{"/api/v0/models": lmStudioModels}, nil)
	ollama := router(map[string]string{"/api/ps": ollamaProcesses}, nil)
	llama := router(map[string]string{"/props": llamaProps}, nil)
	generic := router(map[string]string{"/v1/models": openAIModels}, nil)
	defer lm.Close()
	defer ollama.Close()
	defer llama.Close()
	defer generic.Close()

	host := &fakeHost{
		table: map[uint32]procEntry{
			10: {PID: 10, Exe: "Bionic.exe"}, 20: {PID: 20, Exe: "ollama.exe"},
			30: {PID: 30, Exe: "llama-server.exe"}, 40: {PID: 40, Exe: "vllm-serve.exe"},
		},
		listen: []tcpListener{
			loopbackListener(portOf(t, lm), 10), loopbackListener(portOf(t, ollama), 20),
			loopbackListener(portOf(t, llama), 30), loopbackListener(portOf(t, generic), 40),
		},
	}
	gpu := []gpuProcess{{PID: 40, Name: "vllm-serve.exe", VRAMDedicatedMiB: 9000}}
	sources := newTestDiscoverer(host).discover(context.Background(), gpu)

	if len(sources) != 4 {
		t.Fatalf("found %d sources: %+v", len(sources), sources)
	}
	if s := find(sources, kindLMStudio); s == nil || s.Label != "LM Studio / Bionic" || s.PID != 10 || len(s.Models) != 2 || s.Status != sourceOK {
		t.Fatalf("lm studio = %+v", s)
	}
	if s := find(sources, kindOllama); s == nil || len(s.Models) != 1 || s.Models[0].ID != "llama3.2:3b" {
		t.Fatalf("ollama = %+v", s)
	}
	if s := find(sources, kindLlamaCpp); s == nil || len(s.Models) != 1 || s.Models[0].Quantization != "Q4_K_XL" {
		t.Fatalf("llama.cpp = %+v", s)
	}
	if s := find(sources, kindOpenAICompatible); s == nil || s.Process != "vllm-serve.exe" || len(s.Models) != 2 || s.VRAMDedicatedMiB == nil {
		t.Fatalf("openai-compatible = %+v", s)
	}
}

func TestDiscoveryCarriesTheInstalledLibraryOfLMStudioAndOllama(t *testing.T) {
	lm := router(map[string]string{"/api/v0/models": lmStudioModels}, nil)
	ollama := router(map[string]string{"/api/ps": ollamaProcesses, "/api/tags": ollamaTags}, nil)
	defer lm.Close()
	defer ollama.Close()
	host := &fakeHost{
		table:  map[uint32]procEntry{10: {PID: 10, Exe: "Bionic.exe"}, 20: {PID: 20, Exe: "ollama.exe"}},
		listen: []tcpListener{loopbackListener(portOf(t, lm), 10), loopbackListener(portOf(t, ollama), 20)},
	}
	sources := newTestDiscoverer(host).discover(context.Background(), nil)
	if s := find(sources, kindLMStudio); s == nil || len(s.Models) != 2 || len(s.Library) != 1 || s.Library[0].ID != "google/gemma-3n" {
		t.Fatalf("lm studio = %+v", s)
	}
	if s := find(sources, kindOllama); s == nil || len(s.Models) != 1 || len(s.Library) != 1 || s.Library[0].ID != "qwen3:8b" {
		t.Fatalf("ollama = %+v", s)
	}
	// Whatever the tool, the library is a list and never null.
	noAPI := newTestDiscoverer(&fakeHost{table: map[uint32]procEntry{30: {PID: 30, Exe: "koboldcpp.exe"}}}).
		discover(context.Background(), []gpuProcess{{PID: 30, Tool: toolKobold, VRAMDedicatedMiB: 5000}})
	if len(noAPI) != 1 || noAPI[0].Library == nil || noAPI[0].Models == nil {
		t.Fatalf("no-API source = %+v", noAPI)
	}
}

func TestDiscoveryDoesNotProbeProcessesItHasNoReasonToAsk(t *testing.T) {
	var hits atomic.Int64
	server := router(map[string]string{"/v1/models": openAIModels}, &hits)
	defer server.Close()
	host := &fakeHost{
		table:  map[uint32]procEntry{50: {PID: 50, Exe: "some-dev-server.exe"}},
		listen: []tcpListener{loopbackListener(portOf(t, server), 50)},
	}
	d := newTestDiscoverer(host)
	if sources := d.discover(context.Background(), nil); len(sources) != 0 || hits.Load() != 0 {
		t.Fatalf("an unrelated local server was probed: %d sources, %d requests", len(sources), hits.Load())
	}
	// The same process holding real GPU memory is worth asking.
	if sources := d.discover(context.Background(), []gpuProcess{{PID: 50, VRAMDedicatedMiB: 700}}); len(sources) != 1 || hits.Load() == 0 {
		t.Fatalf("a GPU-heavy server was not probed: %d sources", len(sources))
	}
	// Only a process of this user is ever contacted.
	hits.Store(0)
	host.foreign = map[uint32]bool{50: true}
	if sources := d.discover(context.Background(), []gpuProcess{{PID: 50, VRAMDedicatedMiB: 700}}); len(sources) != 0 || hits.Load() != 0 {
		t.Fatalf("another user's server was probed: %d requests", hits.Load())
	}
}

func TestDiscoveryLeavesTheEdgeAloneWhateverItsPortsAndParents(t *testing.T) {
	var hits atomic.Int64
	server := router(map[string]string{"/props": llamaProps}, &hits)
	defer server.Close()
	port := portOf(t, server)
	host := &fakeHost{
		table: map[uint32]procEntry{
			5:  {PID: 5, Exe: "llama-swap.exe"},
			60: {PID: 60, PPID: 5, Exe: "llama-server.exe"},
			70: {PID: 70, Exe: "llama-server.exe"},
		},
		listen: []tcpListener{loopbackListener(port, 60)},
	}
	if sources := newTestDiscoverer(host).discover(context.Background(), []gpuProcess{{PID: 60, Tool: toolLlamaCpp, VRAMDedicatedMiB: 9000}}); len(sources) != 0 || hits.Load() != 0 {
		t.Fatalf("the edge's own model process became a source: %+v (%d requests)", sources, hits.Load())
	}
	host.listen = []tcpListener{loopbackListener(port, 70)}
	if sources := newTestDiscoverer(host, port).discover(context.Background(), nil); len(sources) != 0 || hits.Load() != 0 {
		t.Fatalf("a skipped port was probed: %+v", sources)
	}
	if sources := newTestDiscoverer(host).discover(context.Background(), nil); len(sources) != 1 {
		t.Fatalf("a standalone llama-server was not found: %+v", sources)
	}
}

func TestDiscoveryReportsAProtectedAPIWithoutTryingToOpenIt(t *testing.T) {
	var authorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	host := &fakeHost{
		table:  map[uint32]procEntry{10: {PID: 10, Exe: "Bionic.exe"}},
		listen: []tcpListener{loopbackListener(portOf(t, server), 10)},
	}
	sources := newTestDiscoverer(host).discover(context.Background(), nil)
	if len(sources) != 1 || sources[0].Status != sourceProtected || sources[0].Kind != kindLMStudio || len(sources[0].Models) != 0 {
		t.Fatalf("sources = %+v", sources)
	}
	if got, _ := authorization.Load().(string); got != "" {
		t.Fatalf("the probe sent credentials: %q", got)
	}
}

func TestDiscoveryReportsAKnownToolWhoseAPICannotBeReached(t *testing.T) {
	host := &fakeHost{table: map[uint32]procEntry{10: {PID: 10, Exe: "Bionic.exe"}}}
	gpu := []gpuProcess{{PID: 10, Name: "Bionic.exe", Tool: toolLMStudio, VRAMDedicatedMiB: 6500, GPUUtil: ptr(72)}}
	sources := newTestDiscoverer(host).discover(context.Background(), gpu)
	if len(sources) != 1 {
		t.Fatalf("sources = %+v", sources)
	}
	s := sources[0]
	if s.Status != sourceNoAPI || s.Kind != kindLMStudio || s.Note == "" || s.Activity != activityGenerating || s.ActivityBasis != "gpu" ||
		s.VRAMDedicatedMiB == nil || *s.VRAMDedicatedMiB != 6500 {
		t.Fatalf("source = %+v", s)
	}
	// A tool that holds almost nothing is not a source.
	quiet := []gpuProcess{{PID: 10, Tool: toolLMStudio, VRAMDedicatedMiB: 50}}
	if sources := newTestDiscoverer(host).discover(context.Background(), quiet); len(sources) != 0 {
		t.Fatalf("an idle tool became a source: %+v", sources)
	}
}

func TestDiscoveryFollowsNoRedirectsAndBoundsWhatItReads(t *testing.T) {
	target := router(map[string]string{"/api/v0/models": lmStudioModels}, nil)
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("x", maxProbeBody+10) + `"}`))
	}))
	defer huge.Close()
	host := &fakeHost{
		table:  map[uint32]procEntry{10: {PID: 10, Exe: "Bionic.exe"}, 11: {PID: 11, Exe: "ollama.exe"}},
		listen: []tcpListener{loopbackListener(portOf(t, redirector), 10), loopbackListener(portOf(t, huge), 11)},
	}
	if sources := newTestDiscoverer(host).discover(context.Background(), nil); len(sources) != 0 {
		t.Fatalf("a redirect or an oversized body produced a source: %+v", sources)
	}
}

func TestDiscoveryPrefersTheEndpointLMStudioRecordedForItself(t *testing.T) {
	recorded := router(map[string]string{"/api/v0/models": lmStudioModels}, nil)
	other := router(map[string]string{"/api/v0/models": `{"data":[]}`}, nil)
	defer recorded.Close()
	defer other.Close()
	host := &fakeHost{
		table:  map[uint32]procEntry{10: {PID: 10, Exe: "node.exe"}},
		paths:  map[uint32]string{10: `C:\Users\me\.lmstudio\bin\llmster\node.exe`},
		listen: []tcpListener{loopbackListener(portOf(t, other), 10), loopbackListener(portOf(t, recorded), 10)},
		lmPort: portOf(t, recorded), lmPID: 10,
	}
	sources := newTestDiscoverer(host).discover(context.Background(), nil)
	if len(sources) != 1 || len(sources[0].Models) != 2 {
		t.Fatalf("the recorded endpoint did not win: %+v", sources)
	}
}

func TestDiscoveryDerivesRatesFromLlamaSlotsAndBelievesThem(t *testing.T) {
	var decoded atomic.Int64
	decoded.Store(100)
	var processing atomic.Bool
	processing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			_, _ = w.Write([]byte(llamaProps))
		case "/slots":
			state := "false"
			if processing.Load() {
				state = "true"
			}
			_, _ = w.Write([]byte(`[{"id":0,"is_processing":` + state + `,"next_token":{"n_decoded":` + strconv.FormatInt(decoded.Load(), 10) + `}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host := &fakeHost{
		table:  map[uint32]procEntry{30: {PID: 30, Exe: "llama-server.exe"}},
		listen: []tcpListener{loopbackListener(portOf(t, server), 30)},
	}
	clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	d := newTestDiscoverer(host)
	d.now = func() time.Time { return clock }

	// No GPU reading at all: the server's own word is enough.
	first := d.discover(context.Background(), nil)
	if len(first) != 1 || first[0].Activity != activityGenerating || first[0].ActivityBasis != "slots" || first[0].TokensPerSecond != nil {
		t.Fatalf("first look = %+v", first)
	}
	clock = clock.Add(2 * time.Second)
	decoded.Store(180)
	second := d.discover(context.Background(), nil)
	if second[0].TokensPerSecond == nil || *second[0].TokensPerSecond != 40 {
		t.Fatalf("rate = %v, want 40", second[0].TokensPerSecond)
	}
	// The server says it is idle although the GPU looks busy: it is believed.
	processing.Store(false)
	clock = clock.Add(2 * time.Second)
	idle := d.discover(context.Background(), []gpuProcess{{PID: 30, Tool: toolLlamaCpp, VRAMDedicatedMiB: 9000, GPUUtil: ptr(95)}})
	if idle[0].Activity != activityIdle || idle[0].TokensPerSecond != nil {
		t.Fatalf("idle = %+v", idle[0])
	}
}

func TestDiscoveryWithoutAHostFindsNothing(t *testing.T) {
	var d *discoverer
	if d.snapshot() != nil {
		t.Fatal("a nil discoverer returned sources")
	}
	d.run(context.Background(), func() []gpuProcess { return nil })
	if got := newDiscoverer(nil, nil, 1); got.host != nil {
		t.Fatal("expected a nil host to be kept nil")
	}
}

func (h *fakeHost) launchInfo(pid uint32) (launchInfo, bool) {
	info, ok := h.launch[pid]
	return info, ok
}

func (h *fakeHost) memory(pid uint32) (float64, bool) {
	bytes, ok := h.ram[pid]
	return bytes, ok
}

func (h *fakeHost) startTime(pid uint32) (time.Time, bool) {
	started, ok := h.started[pid]
	return started, ok
}

func (h *fakeHost) serverLog(tool string, launch launchInfo) string {
	if launch.LogFile != "" {
		return launch.LogFile
	}
	return h.logs[tool]
}
