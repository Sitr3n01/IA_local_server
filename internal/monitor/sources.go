package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The cia-edge reports everything about its own traffic, because every request
// passes through it. Other tools on the same machine do not: a chat app, a
// second server, a developer's own llama-server. The monitor still has to say
// what the GPU is doing for them, so it looks for them the way a person would:
// which programs hold the GPU, which of those listen on a loopback port, and
// what that port says when asked, read-only, what it is serving.
//
// The rules that keep this safe are few. Only loopback is contacted, only with
// GET, only on ports owned by a process of this user that is a known inference
// tool or holds real GPU memory, and never with a credential: an API that wants
// one is reported as protected, not attempted. Responses are bounded, parsed
// into small fixed shapes, and redirects are not followed.

const (
	discoveryInterval = 2 * time.Second
	discoveryTimeout  = 3 * time.Second
	probeTimeout      = 700 * time.Millisecond
	maxProbeBody      = 1 << 20
	maxProbes         = 16
	maxProbeParallel  = 6
	maxSourceModels   = 8
	maxLibraryModels  = 24

	// A tool's processes count as working when their share of the adapter is
	// at least this. The desktop compositor's idle share is well under it.
	sourceBusyUtil = 8.0
	// A process with this much dedicated memory is worth asking what it serves
	// even when its program is not one the monitor knows.
	probeMinVRAMMiB = 512.0
	// A known tool holding this much is reported as a source even when its API
	// cannot be reached, so its load is not attributed to nothing.
	sourceMinVRAMMiB = 256.0
)

const (
	kindLMStudio         = toolLMStudio
	kindOllama           = toolOllama
	kindLlamaCpp         = toolLlamaCpp
	kindOpenAICompatible = "openai-compatible"

	sourceOK        = "ok"
	sourceProtected = "protected"
	sourceNoAPI     = "no-api"
)

// inferenceSource is one program serving a model on this machine.
type inferenceSource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Process  string `json:"process,omitempty"`
	PID      uint32 `json:"pid"`
	Endpoint string `json:"endpoint,omitempty"`
	Status   string `json:"status"`
	// Models are the models the API says are loaded, or served when it does
	// not distinguish. Library is what else the tool has installed and could
	// load, for the tools whose API says so.
	Models   []sourceModel `json:"models"`
	Library  []sourceModel `json:"library"`
	Activity string        `json:"activity"`
	// ActivityBasis says how Activity was decided: "slots" when the server
	// reported its own, "gpu" when it is inferred from the tool's share of the
	// adapter.
	ActivityBasis    string   `json:"activity_basis,omitempty"`
	TokensPerSecond  *float64 `json:"tokens_per_second"`
	VRAMDedicatedMiB *float64 `json:"vram_dedicated_mib"`
	GPUUtil          *float64 `json:"gpu_util"`
	// RAMBytes is the working set of the process that holds the model, and
	// StartedAt when that process began, which for a model server is close to
	// when the model was loaded.
	RAMBytes  *float64 `json:"ram_bytes"`
	StartedAt string   `json:"started_at,omitempty"`
	Note      string   `json:"note,omitempty"`
	// Stoppable says the page may offer to end the process that holds the model;
	// StopTarget names it for the button. The page never sends a process: it
	// sends this source's id, and the monitor resolves the process itself.
	Stoppable  bool   `json:"stoppable"`
	StopTarget string `json:"stop_target,omitempty"`
	// Metering says where this source's per-request numbers come from: "server-log"
	// when its server's own log is being read, and empty when there is nothing
	// but the GPU to go on. MeteringNote says why, for the page.
	Metering     string `json:"metering,omitempty"`
	MeteringNote string `json:"metering_note,omitempty"`

	tool    string
	stopPID uint32
	stopExe string
}

type sourceModel struct {
	ID            string   `json:"id"`
	Arch          string   `json:"arch,omitempty"`
	Params        string   `json:"params,omitempty"`
	Quantization  string   `json:"quantization,omitempty"`
	Type          string   `json:"type,omitempty"`
	ContextLoaded *int64   `json:"context_loaded"`
	ContextMax    *int64   `json:"context_max"`
	SizeBytes     *int64   `json:"size_bytes"`
	VRAMMiB       *float64 `json:"vram_mib"`
	// Slots and GPULayers come from a server's launch arguments, where its API
	// is not readable.
	Slots     *int64 `json:"slots"`
	GPULayers *int64 `json:"gpu_layers"`
	// Loaded is nil when the API lists models without saying which are loaded.
	Loaded *bool `json:"loaded"`
}

const (
	activityGenerating = "generating"
	activityIdle       = "idle"
)

// hostView is what discovery needs to know about the machine. It is an
// interface so the logic can be exercised without a machine.
type hostView interface {
	processes() map[uint32]procEntry
	// imagePath is where the process's program lives, or "" when it cannot be
	// opened. exe is the name the process table gave, which notices a pid that
	// was reused by another program.
	imagePath(pid uint32, exe string) string
	listeners() []tcpListener
	sameUser(pid uint32) bool
	// lmStudioState is the endpoint LM Studio's daemon recorded for itself.
	lmStudioState() (port uint16, pid uint32, ok bool)
	// launchInfo is the allowlisted part of a process's launch arguments (see
	// launch.go); memory its working set; startTime when it began.
	launchInfo(pid uint32) (launchInfo, bool)
	memory(pid uint32) (workingSetBytes float64, ok bool)
	startTime(pid uint32) (time.Time, bool)
	// serverLog is the log file of the llama.cpp server behind a tool, or "": the
	// one its launch arguments name, else the one the tool is known to write.
	serverLog(tool string, launch launchInfo) string
}

// tcpListener is a listening socket reachable through loopback, with the
// address to connect to.
type tcpListener struct {
	Addr netip.Addr
	Port uint16
	PID  uint32
}

type discoverer struct {
	host      hostView
	client    *http.Client
	skipPorts map[uint16]struct{}
	selfPID   uint32
	now       func() time.Time

	mu     sync.Mutex
	latest []inferenceSource

	// Owned by the discovery goroutine.
	previous map[string]slotObservation
	probed   map[string]cachedProbe
	tailers  map[string]*logTailer

	// records are the requests read from server logs, newest first.
	recordsMu sync.Mutex
	records   []externalRecord
}

// cachedProbe remembers an answer that will not change by being asked again
// every two seconds: a server that wants a key, or that is none of the kinds.
// Asking each time made such a server log a rejected request every two seconds.
type cachedProbe struct {
	result probeResult
	at     time.Time
}

const (
	protectedProbeTTL = 60 * time.Second
	unknownProbeTTL   = 30 * time.Second
	maxLoggedRecords  = 40
)

type slotObservation struct {
	at      time.Time
	decoded map[int64]int64
}

func newDiscoverer(host hostView, skipPorts []uint16, selfPID uint32) *discoverer {
	skip := make(map[uint16]struct{}, len(skipPorts))
	for _, port := range skipPorts {
		if port != 0 {
			skip[port] = struct{}{}
		}
	}
	return &discoverer{
		host: host,
		client: &http.Client{
			Timeout: probeTimeout,
			// Redirects are how a request meant for one local port ends up
			// somewhere else; none is followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:                  nil,
				DisableKeepAlives:      true,
				DialContext:            (&net.Dialer{Timeout: probeTimeout}).DialContext,
				ResponseHeaderTimeout:  probeTimeout,
				MaxResponseHeaderBytes: 64 << 10,
			},
		},
		skipPorts: skip,
		selfPID:   selfPID,
		now:       time.Now,
		previous:  make(map[string]slotObservation),
		probed:    make(map[string]cachedProbe),
		tailers:   make(map[string]*logTailer),
	}
}

// loggedRecords is what the server logs have said so far, newest first; the
// caller must not modify it.
func (d *discoverer) loggedRecords() []externalRecord {
	if d == nil {
		return nil
	}
	d.recordsMu.Lock()
	defer d.recordsMu.Unlock()
	out := make([]externalRecord, len(d.records))
	copy(out, d.records)
	return out
}

// snapshot is the latest result; the caller must not modify it.
func (d *discoverer) snapshot() []inferenceSource {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.latest
}

// run discovers until ctx ends. gpu gives the collector's latest view of who is
// using the adapter.
func (d *discoverer) run(ctx context.Context, gpu func() []gpuProcess) {
	if d == nil || d.host == nil {
		return
	}
	step := func() {
		found, cancel := context.WithTimeout(ctx, discoveryTimeout)
		defer cancel()
		sources := d.discover(found, gpu())
		d.mu.Lock()
		d.latest = sources
		d.mu.Unlock()
	}
	step()
	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			step()
		}
	}
}

type probeTask struct {
	listener tcpListener
	exe      string
	tool     string
}

type probeResult struct {
	kind      string
	models    []sourceModel
	library   []sourceModel
	protected bool
	slots     *slotState
}

func (d *discoverer) discover(ctx context.Context, gpu []gpuProcess) []inferenceSource {
	procs := d.host.processes()
	lmPort, lmPID, lmKnown := d.host.lmStudioState()
	// The edge reports its own model processes in full. Left in, they would be
	// added to a stand-alone server's memory and could be picked as the process
	// to end.
	gpu = withoutManaged(gpu, procs)

	gpuByPID := make(map[uint32]gpuProcess, len(gpu))
	for _, process := range gpu {
		gpuByPID[process.PID] = process
	}

	// Reachable loopback listeners, one per (process, port).
	type endpoint struct {
		pid  uint32
		port uint16
	}
	seen := make(map[endpoint]struct{})
	var tasks []probeTask
	classified := make(map[uint32]string)
	toolOf := func(pid uint32) string {
		if tool, ok := classified[pid]; ok {
			return tool
		}
		tool := classifyTool(procs[pid].Exe, d.host.imagePath(pid, procs[pid].Exe))
		classified[pid] = tool
		return tool
	}
	for _, listener := range d.host.listeners() {
		pid := listener.PID
		entry, alive := procs[pid]
		if !alive || pid == d.selfPID {
			continue
		}
		if _, skip := d.skipPorts[listener.Port]; skip {
			continue
		}
		key := endpoint{pid, listener.Port}
		if _, dup := seen[key]; dup {
			continue
		}
		tool := toolOf(pid)
		worth := tool != "" || gpuByPID[pid].VRAMDedicatedMiB >= probeMinVRAMMiB || (lmKnown && pid == lmPID && listener.Port == lmPort)
		if !worth || managedByEdge(procs, pid) || !d.host.sameUser(pid) {
			continue
		}
		seen[key] = struct{}{}
		tasks = append(tasks, probeTask{listener: listener, exe: entry.Exe, tool: tool})
	}
	// Prefer the endpoint LM Studio recorded for itself when several ports of
	// one process qualify, then the lowest port, so the choice is stable.
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		aLM := lmKnown && a.listener.PID == lmPID && a.listener.Port == lmPort
		bLM := lmKnown && b.listener.PID == lmPID && b.listener.Port == lmPort
		if aLM != bLM {
			return aLM
		}
		if a.listener.PID != b.listener.PID {
			return a.listener.PID < b.listener.PID
		}
		return a.listener.Port < b.listener.Port
	})
	if len(tasks) > maxProbes {
		tasks = tasks[:maxProbes]
	}

	results := make([]probeResult, len(tasks))
	var wait sync.WaitGroup
	gate := make(chan struct{}, maxProbeParallel)
	now := d.now()
	for index := range tasks {
		key := fmt.Sprintf("%d:%d", tasks[index].listener.PID, tasks[index].listener.Port)
		if cached, ok := d.probed[key]; ok && d.probeStillValid(cached, now) {
			results[index] = cached.result
			continue
		}
		wait.Add(1)
		gate <- struct{}{}
		go func(index int) {
			defer wait.Done()
			defer func() { <-gate }()
			results[index] = d.probe(ctx, tasks[index].listener.Addr, tasks[index].listener.Port)
		}(index)
	}
	wait.Wait()
	for index := range tasks {
		key := fmt.Sprintf("%d:%d", tasks[index].listener.PID, tasks[index].listener.Port)
		if result := results[index]; result.protected || result.kind == "" {
			if _, ok := d.probed[key]; !ok || !d.probeStillValid(d.probed[key], now) {
				d.probed[key] = cachedProbe{result: result, at: now}
			}
		} else {
			delete(d.probed, key)
		}
	}
	// A cache entry for a server that is gone would only grow.
	for key := range d.probed {
		if now.Sub(d.probed[key].at) > 10*time.Minute {
			delete(d.probed, key)
		}
	}

	var sources []inferenceSource
	covered := make(map[uint32]struct{})
	coveredTool := make(map[string]struct{})
	for index, task := range tasks {
		result := results[index]
		if result.kind == "" && !result.protected {
			continue
		}
		pid := task.listener.PID
		if _, done := covered[pid]; done {
			continue
		}
		covered[pid] = struct{}{}
		kind := result.kind
		if kind == "" || (kind == kindOpenAICompatible && task.tool != "") {
			kind = firstNonEmpty(task.tool, kindOpenAICompatible)
		}
		source := inferenceSource{
			ID:       fmt.Sprintf("%s-%d", kind, pid),
			Kind:     kind,
			Label:    firstNonEmpty(toolLabel(kind), "Servidor compatível com OpenAI"),
			Process:  task.exe,
			PID:      pid,
			Endpoint: net.JoinHostPort(loopbackName(task.listener.Addr), fmt.Sprint(task.listener.Port)),
			Status:   sourceOK,
			Models:   nonNil(result.models),
			Library:  nonNil(result.library),
			tool:     firstNonEmpty(task.tool, kind),
		}
		if result.protected {
			source.Status = sourceProtected
			source.Note = protectedHint(source.tool)
		}
		d.applyActivity(&source, result.slots, gpu)
		d.describeProcess(&source, procs, gpu)
		sources = append(sources, source)
		coveredTool[source.tool] = struct{}{}
	}

	// A known tool that holds GPU memory but offers no API we could read is
	// still a source: its load is real, and saying nothing would attribute it
	// to nothing.
	for tool, group := range groupByTool(gpu) {
		if _, has := coveredTool[tool]; has || group.vram < sourceMinVRAMMiB || tool == "" {
			continue
		}
		source := inferenceSource{
			ID:      fmt.Sprintf("%s-%d", tool, group.pids[0]),
			Kind:    tool,
			Label:   toolLabel(tool),
			Process: procs[group.pids[0]].Exe,
			PID:     group.pids[0],
			Status:  sourceNoAPI,
			Models:  []sourceModel{},
			Library: []sourceModel{},
			tool:    tool,
			Note:    noAPIHint(tool),
		}
		d.applyActivity(&source, nil, gpu)
		d.describeProcess(&source, procs, gpu)
		sources = append(sources, source)
	}

	sort.SliceStable(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if (a.Activity == activityGenerating) != (b.Activity == activityGenerating) {
			return a.Activity == activityGenerating
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.PID < b.PID
	})
	d.forget(sources)
	return sources
}

// forget drops the per-source memory of sources that are gone, so it cannot
// grow and a returning source does not start from a stale reading.
func (d *discoverer) forget(sources []inferenceSource) {
	live := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		live[source.ID] = struct{}{}
	}
	for id := range d.previous {
		if _, ok := live[id]; !ok {
			delete(d.previous, id)
		}
	}
}

type toolGroup struct {
	pids []uint32
	vram float64
}

func groupByTool(gpu []gpuProcess) map[string]toolGroup {
	groups := make(map[string]toolGroup)
	for _, process := range gpu {
		if process.Tool == "" {
			continue
		}
		group := groups[process.Tool]
		group.pids = append(group.pids, process.PID)
		group.vram += process.VRAMDedicatedMiB
		groups[process.Tool] = group
	}
	return groups
}

// withoutManaged drops the GPU processes that descend from the edge.
func withoutManaged(gpu []gpuProcess, procs map[uint32]procEntry) []gpuProcess {
	kept := make([]gpuProcess, 0, len(gpu))
	for _, process := range gpu {
		if !managedByEdge(procs, process.PID) {
			kept = append(kept, process)
		}
	}
	return kept
}

// meter reads the server's own log for this source, when one can be found, and
// records the requests it finished. It sets Metering when the log turns out to
// carry timing lines, and otherwise says why there are no per-request numbers.
func (d *discoverer) meter(source *inferenceSource, launch launchInfo) {
	path := d.host.serverLog(source.tool, launch)
	if path == "" {
		source.MeteringNote = noMeteringNote(source.tool)
		return
	}
	tailer := d.tailers[path]
	if tailer == nil {
		tailer = newLogTailer(path, time.Local)
		d.tailers[path] = tailer
	}
	requests, err := tailer.poll()
	if err != nil {
		source.MeteringNote = "O log do servidor não pôde ser lido agora."
		return
	}
	for _, request := range requests {
		d.addRecord(recordFromLog(*source, request))
	}
	if tailer.parser.seenTiming {
		source.Metering = "server-log"
		return
	}
	source.MeteringNote = "O log do servidor ainda não registrou nenhuma requisição; os números aparecem na primeira."
}

func (d *discoverer) addRecord(record externalRecord) {
	d.recordsMu.Lock()
	defer d.recordsMu.Unlock()
	d.records = append([]externalRecord{record}, d.records...)
	sort.SliceStable(d.records, func(i, j int) bool { return d.records[i].startTime().After(d.records[j].startTime()) })
	if len(d.records) > maxLoggedRecords {
		d.records = d.records[:maxLoggedRecords]
	}
}

// recordFromLog turns a finished request into what the page shows.
func recordFromLog(source inferenceSource, request loggedRequest) externalRecord {
	record := externalRecord{
		SourceID:   source.ID,
		Label:      source.Label,
		StartedAt:  request.At.UTC().Format(time.RFC3339Nano),
		DurationMS: int64(request.TotalMS),
		Measured:   "server-log",
		Basis:      "log",
		Truncated:  request.Truncated,
	}
	if len(source.Models) > 0 {
		record.Model = source.Models[0].ID
	}
	prompt := int64(request.promptTokens())
	output := int64(request.Output)
	record.PromptTokens, record.OutputTokens = &prompt, &output
	record.Measurements.Prompt = "server-log-derived"
	record.Measurements.Output = "server-log"
	if cached := request.cachedTokens(); cached >= 0 {
		value := int64(cached)
		record.CachedTokens = &value
		record.Measurements.Cache = "server-log-derived"
	}
	if request.PromptTPS > 0 {
		record.PromptTPS = ptr(request.PromptTPS)
		record.Measurements.PromptRate = "server-log"
	}
	if request.DecodeTPS > 0 {
		record.TokensPerSecond = ptr(request.DecodeTPS)
		record.Measurements.DecodeRate = "server-log"
	}
	if request.PromptMS > 0 {
		ttft := int64(request.PromptMS)
		record.TTFTMS, record.PromptMS = &ttft, ptr(request.PromptMS)
	}
	if request.DecodeMS > 0 {
		record.DecodeMS = ptr(request.DecodeMS)
	}
	if request.ContextAtEnd != nil {
		context := int64(*request.ContextAtEnd)
		record.ContextTokens = &context
	}
	return record
}

func noMeteringNote(tool string) string {
	switch tool {
	case toolOllama:
		return "O Ollama não expõe contagem de tokens por requisição. Aponte a ferramenta para o edge para medir."
	case toolLMStudio:
		return "Não foi encontrado o log do servidor do LM Studio / Bionic; sem ele só a atividade da GPU é observada."
	default:
		return "Esta ferramenta não expõe contagem de tokens por requisição; só a atividade da GPU é observada. Aponte-a para o edge para medir."
	}
}

// launchNote says where a model name came from when the tool's API would not
// say. It also states what is not kept, because the line it was read from is
// where such tools put their keys.
const launchNote = "O modelo vem da linha de comando do processo: o monitor guarda só o nome, o contexto e os slots e descarta o resto, chave de API inclusive. Ligue o servidor local da ferramenta para ver mais."

// describeProcess adds what can be learned about the process that holds the
// model without asking its API: which model its launch arguments name, how
// much memory it uses, when it started, and whether it may be ended.
func (d *discoverer) describeProcess(source *inferenceSource, procs map[uint32]procEntry, gpu []gpuProcess) {
	holder := source.PID
	if target, ok := stopTarget(*source, gpu, procs, d.selfPID); ok {
		holder = target.PID
		source.Stoppable = true
		source.StopTarget = describeTarget(target)
		source.stopPID, source.stopExe = target.PID, target.Name
	}
	var launch launchInfo
	for _, pid := range []uint32{holder, source.PID} {
		info, ok := d.host.launchInfo(pid)
		if !ok {
			continue
		}
		if launch.File == "" && launch.Alias == "" {
			launch = info
		}
		if launch.LogFile == "" {
			launch.LogFile = info.LogFile
		}
		if len(source.Models) == 0 {
			if model, ok := info.model(); ok {
				source.Models = []sourceModel{model}
				if source.Status != sourceOK {
					source.Note = launchNote
				}
			}
		}
	}
	d.meter(source, launch)
	if bytes, ok := d.host.memory(holder); ok && bytes > 0 {
		source.RAMBytes = ptr(bytes)
	}
	if started, ok := d.host.startTime(holder); ok {
		source.StartedAt = started.UTC().Format(time.RFC3339)
	}
}

// protectedHint explains an API that wants a credential. LM Studio and Bionic
// start their model process with a key of their own, so the honest advice is to
// use the tool's own server, which has no key unless the operator set one.
func protectedHint(tool string) string {
	if tool == toolLMStudio {
		return "Este modelo roda num servidor com chave própria do LM Studio / Bionic, que o monitor não tem nem pede. Ligue o servidor local do LM Studio nas configurações para ver o modelo aqui; o uso da GPU já aparece."
	}
	return "A API pede uma credencial. O monitor não tem nenhuma e não tenta adivinhar."
}

func noAPIHint(tool string) string {
	switch tool {
	case toolLMStudio:
		return "A API local do LM Studio não respondeu. Ative o servidor local para ver qual modelo está carregado."
	case toolOllama:
		return "A API do Ollama não respondeu; o processo usa a GPU, mas não foi possível ler o modelo."
	default:
		return "Este programa usa a GPU, mas não foi encontrada uma API local para ler o modelo."
	}
}

func loopbackName(addr netip.Addr) string {
	if addr.Is6() {
		return "::1"
	}
	return "127.0.0.1"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// applyActivity fills the resource share and the activity of a source. A server
// that reports its own slots is believed; otherwise the tool's share of the
// adapter decides, and the page is told that this is an inference.
func (d *discoverer) applyActivity(source *inferenceSource, slots *slotState, gpu []gpuProcess) {
	var vram, util float64
	var hasUtil bool
	for _, process := range gpu {
		if process.PID != source.PID && (source.tool == "" || process.Tool != source.tool) {
			continue
		}
		vram += process.VRAMDedicatedMiB
		if process.GPUUtil != nil {
			util += *process.GPUUtil
			hasUtil = true
		}
	}
	if vram > 0 {
		source.VRAMDedicatedMiB = ptr(vram)
	}
	if hasUtil {
		source.GPUUtil = ptr(clampPercent(util))
	}
	source.Activity = activityIdle
	if slots != nil {
		source.ActivityBasis = "slots"
		if slots.processing {
			source.Activity = activityGenerating
		}
		source.TokensPerSecond = d.slotRate(source.ID, slots)
		return
	}
	source.ActivityBasis = "gpu"
	if hasUtil && util >= sourceBusyUtil {
		source.Activity = activityGenerating
	}
}

// slotRate turns the decoded-token counters of two consecutive looks at a
// server into a rate. A slot that was not busy at both looks contributes
// nothing: its counter restarted.
func (d *discoverer) slotRate(id string, slots *slotState) *float64 {
	now := d.now()
	previous, had := d.previous[id]
	d.previous[id] = slotObservation{at: now, decoded: slots.decoded}
	if !had || !slots.processing {
		return nil
	}
	elapsed := now.Sub(previous.at).Seconds()
	if elapsed < 0.5 || elapsed > 10 {
		return nil
	}
	var produced int64
	for slot, count := range slots.decoded {
		if before, ok := previous.decoded[slot]; ok && count > before {
			produced += count - before
		}
	}
	if produced <= 0 {
		return nil
	}
	return ptr(float64(produced) / elapsed)
}

// ---------------------------------------------------------------- probing

// probeStillValid reports whether a remembered answer may be reused: only
// answers that are refusals or "not a server I know" are, and only for a while.
func (d *discoverer) probeStillValid(entry cachedProbe, now time.Time) bool {
	age := now.Sub(entry.at)
	switch {
	case entry.result.protected:
		return age < protectedProbeTTL
	case entry.result.kind == "":
		return age < unknownProbeTTL
	}
	return false
}

func (d *discoverer) probe(ctx context.Context, addr netip.Addr, port uint16) probeResult {
	base := "http://" + net.JoinHostPort(loopbackName(addr), fmt.Sprint(port))

	// LM Studio first: its REST API is the only one of these with a route that
	// no other server answers.
	status, body, err := d.get(ctx, base+"/api/v0/models")
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return probeResult{protected: true}
	}
	if err == nil && status == http.StatusOK {
		if models, library, ok := parseLMStudioModels(body); ok {
			return probeResult{kind: kindLMStudio, models: models, library: library}
		}
	}
	if status, body, err = d.get(ctx, base+"/api/ps"); err == nil && status == http.StatusOK {
		if models, ok := parseOllamaProcesses(body); ok {
			result := probeResult{kind: kindOllama, models: models}
			if tagStatus, tagBody, tagErr := d.get(ctx, base+"/api/tags"); tagErr == nil && tagStatus == http.StatusOK {
				result.library = withoutLoaded(parseOllamaTags(tagBody), models)
			}
			return result
		}
	}
	if status, body, err = d.get(ctx, base+"/props"); err == nil && status == http.StatusOK {
		if models, ok := parseLlamaProps(body); ok {
			result := probeResult{kind: kindLlamaCpp, models: models}
			if slotStatus, slotBody, slotErr := d.get(ctx, base+"/slots"); slotErr == nil && slotStatus == http.StatusOK {
				result.slots = parseLlamaSlots(slotBody)
			}
			return result
		}
	}
	if status, body, err = d.get(ctx, base+"/v1/models"); err == nil {
		switch status {
		case http.StatusOK:
			if models, ok := parseOpenAIModels(body); ok {
				return probeResult{kind: kindOpenAICompatible, models: models}
			}
		case http.StatusUnauthorized, http.StatusForbidden:
			return probeResult{protected: true}
		}
	}
	return probeResult{}
}

var errBodyTooLarge = errors.New("response larger than the probe limit")

// get reads a bounded body. Any status is returned so the caller can tell a
// protected API from an absent route.
func (d *discoverer) get(ctx context.Context, target string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cia-monitor")
	response, err := d.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBody+1))
	if err != nil {
		return response.StatusCode, nil, err
	}
	if len(body) > maxProbeBody {
		return response.StatusCode, nil, errBodyTooLarge
	}
	return response.StatusCode, body, nil
}

// ---------------------------------------------------------------- parsers

func int64Ptr(value int64) *int64 { return &value }

func positiveInt(value float64) *int64 {
	if value <= 0 || value > 1e15 {
		return nil
	}
	return int64Ptr(int64(value))
}

func boolPtr(value bool) *bool { return &value }

func decodeObject(body []byte) (map[string]json.RawMessage, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return nil, false
	}
	return object, true
}

// parseLMStudioModels reads GET /api/v0/models. Every downloaded model is
// listed; only the ones in state "loaded" are reported, because the question
// the page answers is what is in memory.
func parseLMStudioModels(body []byte) (loaded, library []sourceModel, ok bool) {
	object, ok := decodeObject(body)
	if !ok {
		return nil, nil, false
	}
	var list []struct {
		ID            string  `json:"id"`
		Type          string  `json:"type"`
		Arch          string  `json:"arch"`
		Quantization  string  `json:"quantization"`
		State         string  `json:"state"`
		MaxContext    float64 `json:"max_context_length"`
		LoadedContext float64 `json:"loaded_context_length"`
	}
	if json.Unmarshal(object["data"], &list) != nil {
		return nil, nil, false
	}
	// A server that answers this route without any model carrying a state is
	// some other API that happens to have a /api/v0 path.
	stateSeen := false
	for _, entry := range list {
		if entry.State != "" {
			stateSeen = true
		}
	}
	if !stateSeen && len(list) > 0 {
		return nil, nil, false
	}
	loaded, library = []sourceModel{}, []sourceModel{}
	for _, entry := range list {
		if entry.ID == "" {
			continue
		}
		model := sourceModel{
			ID: entry.ID, Type: entry.Type, Arch: entry.Arch, Quantization: entry.Quantization,
			ContextMax: positiveInt(entry.MaxContext),
		}
		if entry.State == "loaded" {
			model.ContextLoaded, model.Loaded = positiveInt(entry.LoadedContext), boolPtr(true)
			if len(loaded) < maxSourceModels {
				loaded = append(loaded, model)
			}
			continue
		}
		model.Loaded = boolPtr(false)
		if len(library) < maxLibraryModels {
			library = append(library, model)
		}
	}
	return loaded, library, true
}

// parseOllamaTags reads GET /api/tags, every model Ollama has installed.
func parseOllamaTags(body []byte) []sourceModel {
	object, ok := decodeObject(body)
	if !ok {
		return nil
	}
	var list []struct {
		Name    string  `json:"name"`
		Model   string  `json:"model"`
		Size    float64 `json:"size"`
		Details struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	}
	if json.Unmarshal(object["models"], &list) != nil {
		return nil
	}
	models := []sourceModel{}
	for _, entry := range list {
		id := firstNonEmpty(entry.Name, entry.Model)
		if id == "" {
			continue
		}
		models = append(models, sourceModel{
			ID: id, Arch: entry.Details.Family, Params: entry.Details.ParameterSize,
			Quantization: entry.Details.QuantizationLevel, SizeBytes: positiveInt(entry.Size),
			Loaded: boolPtr(false),
		})
		if len(models) == maxLibraryModels {
			break
		}
	}
	return models
}

// withoutLoaded drops from a library the models already listed as loaded, so
// one model is not shown twice.
func withoutLoaded(library, loaded []sourceModel) []sourceModel {
	out := []sourceModel{}
	for _, model := range library {
		duplicate := false
		for _, held := range loaded {
			if held.ID == model.ID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, model)
		}
	}
	return out
}

func nonNil(models []sourceModel) []sourceModel {
	if models == nil {
		return []sourceModel{}
	}
	return models
}

// parseOllamaProcesses reads GET /api/ps, the models Ollama holds in memory.
func parseOllamaProcesses(body []byte) ([]sourceModel, bool) {
	object, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	raw, present := object["models"]
	if !present {
		return nil, false
	}
	var list []struct {
		Name          string  `json:"name"`
		Model         string  `json:"model"`
		Size          float64 `json:"size"`
		SizeVRAM      float64 `json:"size_vram"`
		ContextLength float64 `json:"context_length"`
		Details       struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, false
	}
	models := []sourceModel{}
	for _, entry := range list {
		id := firstNonEmpty(entry.Name, entry.Model)
		if id == "" {
			continue
		}
		model := sourceModel{
			ID: id, Arch: entry.Details.Family, Params: entry.Details.ParameterSize,
			Quantization: entry.Details.QuantizationLevel, ContextLoaded: positiveInt(entry.ContextLength),
			SizeBytes: positiveInt(entry.Size), Loaded: boolPtr(true),
		}
		if entry.SizeVRAM > 0 {
			model.VRAMMiB = ptr(entry.SizeVRAM / bytesPerMiB)
		}
		models = append(models, model)
		if len(models) == maxSourceModels {
			break
		}
	}
	return models, true
}

var quantPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])(I?Q[1-8]_[A-Z0-9_]+|BF16|F16|F32|MXFP4)(?:[^A-Za-z0-9]|$)`)

// quantFromName recovers a quantization label from a GGUF file name, where the
// runtime does not report one.
func quantFromName(name string) string {
	match := quantPattern.FindStringSubmatch(name)
	if match == nil {
		return ""
	}
	return strings.ToUpper(match[1])
}

// fileName is the last element of a path written with either separator, or ""
// for an empty one. Directories never reach the page.
func fileName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Base(strings.ReplaceAll(path, `\`, "/"))
}

// parseLlamaProps reads GET /props of a llama.cpp server. Only the file's base
// name is kept: the directory it lives in is not the page's business.
func parseLlamaProps(body []byte) ([]sourceModel, bool) {
	object, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	_, hasSettings := object["default_generation_settings"]
	_, hasSlots := object["total_slots"]
	if !hasSettings && !hasSlots {
		return nil, false
	}
	var props struct {
		ModelPath  string `json:"model_path"`
		ModelAlias string `json:"model_alias"`
		Settings   struct {
			NCtx float64 `json:"n_ctx"`
		} `json:"default_generation_settings"`
		TotalSlots float64 `json:"total_slots"`
	}
	if json.Unmarshal(body, &props) != nil {
		return nil, false
	}
	file := fileName(props.ModelPath)
	// Recent builds report the full path as the alias when none was given, so
	// an alias is trusted only after the same reduction to a file name.
	id := firstNonEmpty(strings.TrimSuffix(fileName(props.ModelAlias), ".gguf"), strings.TrimSuffix(file, ".gguf"))
	if id == "" {
		id = "modelo sem nome"
	}
	model := sourceModel{
		ID: id, Quantization: quantFromName(file), ContextLoaded: positiveInt(props.Settings.NCtx),
		Loaded: boolPtr(true),
	}
	return []sourceModel{model}, true
}

// slotState is what a llama.cpp server says about its slots.
type slotState struct {
	processing bool
	decoded    map[int64]int64
}

// parseLlamaSlots reads GET /slots. The route is optional (--no-slots turns it
// off) and its shape has moved between builds, so anything unexpected is "no
// slot information" and the activity falls back to the GPU.
func parseLlamaSlots(body []byte) *slotState {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '[' {
		return nil
	}
	var slots []struct {
		ID           int64           `json:"id"`
		IsProcessing bool            `json:"is_processing"`
		NextToken    json.RawMessage `json:"next_token"`
	}
	if json.Unmarshal(body, &slots) != nil || len(slots) == 0 {
		return nil
	}
	state := &slotState{decoded: make(map[int64]int64, len(slots))}
	for _, slot := range slots {
		if slot.IsProcessing {
			state.processing = true
		}
		state.decoded[slot.ID] = nextTokenDecoded(slot.NextToken)
	}
	return state
}

// nextTokenDecoded reads n_decoded from next_token, which is an object in
// current builds and a one-element array in older ones.
func nextTokenDecoded(raw json.RawMessage) int64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	type token struct {
		NDecoded float64 `json:"n_decoded"`
	}
	if raw[0] == '[' {
		var list []token
		if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
			return int64(list[0].NDecoded)
		}
		return 0
	}
	var single token
	if json.Unmarshal(raw, &single) == nil {
		return int64(single.NDecoded)
	}
	return 0
}

// parseOpenAIModels reads GET /v1/models. It cannot tell which listed models
// are in memory, so none is marked loaded.
func parseOpenAIModels(body []byte) ([]sourceModel, bool) {
	object, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	var list []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(object["data"], &list) != nil {
		return nil, false
	}
	models := []sourceModel{}
	for _, entry := range list {
		if entry.ID == "" {
			continue
		}
		models = append(models, sourceModel{ID: entry.ID})
		if len(models) == maxSourceModels {
			break
		}
	}
	return models, true
}

// lmStudioStateFile reads the endpoint record LM Studio's daemon writes for
// its own tooling. The file is small and the daemon's, so a large or malformed
// one is ignored.
func lmStudioStateFile(path string) (port uint16, pid uint32, ok bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > 4096 || !info.Mode().IsRegular() {
		return 0, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	var record struct {
		Host string  `json:"host"`
		PID  float64 `json:"pid"`
		Port float64 `json:"port"`
	}
	if json.Unmarshal(data, &record) != nil || record.Port < 1 || record.Port > 65535 || record.PID < 1 {
		return 0, 0, false
	}
	switch strings.ToLower(record.Host) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
	default:
		return 0, 0, false
	}
	return uint16(record.Port), uint32(record.PID), true
}
