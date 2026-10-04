package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sitr3n01/local-ai-provider/internal/adminpipe"
)

const (
	// sampleInterval is the page's refresh rate and the history's resolution.
	sampleInterval = time.Second
	// historyLength is two minutes of samples: enough to see a request start,
	// run and finish on one sparkline.
	historyLength = 120
	// statusInterval is slower than the sample: the status makes the edge ask
	// the router what is running, and none of it moves second to second.
	statusInterval   = 2 * time.Second
	inferenceTimeout = 800 * time.Millisecond
	statusTimeout    = 1800 * time.Millisecond
)

// Activity phases, in the order the page reasons about them.
const (
	phaseOffline    = "offline"    // the edge did not answer
	phaseDraining   = "draining"   // maintenance: new requests are refused
	phaseLoading    = "loading"    // the router is starting a model process
	phasePrompt     = "prompt"     // a streamed request before its first token
	phaseWorking    = "working"    // a request the edge cannot see into
	phaseGenerating = "generating" // tokens are arriving
	phaseQueued     = "queued"     // requests wait for the admission slot
	phaseReady      = "ready"      // a model is loaded and nothing runs
	phaseIdle       = "idle"       // nothing is loaded

	// Phases that come from a tool other than the edge. The edge leads whenever
	// it is doing something; these describe the machine when it is not.
	phaseExternal      = "external"       // another tool is using the GPU for a model
	phaseExternalReady = "external_ready" // another tool holds a model and is idle
)

// energyGap is the longest pause between two power readings that is still
// integrated. A longer one means the machine slept or the monitor was stopped,
// and multiplying the last wattage by it would invent energy.
const energyGap = 5 * time.Second

// Options is what a collector needs to know about its deployment. The URLs are
// validated by the caller; ControlURL is the only one the monitor contacts over
// HTTP, and Control.Pipe the only administrative channel it may use.
type Options struct {
	Version     string
	Environment string
	ControlURL  *url.URL
	DataURL     *url.URL
	Control     ControlOptions
	// Log receives metadata-only events: an operation's ID, verb, model and
	// state. Nil discards them.
	Log func(event string, fields map[string]any)
}

type snapshot struct {
	GeneratedAt string            `json:"generated_at"`
	Monitor     monitorInfo       `json:"monitor"`
	Activity    activity          `json:"activity"`
	Control     controlView       `json:"control"`
	Edge        edgeView          `json:"edge"`
	Hardware    hardwareSample    `json:"hardware"`
	Machine     machineInfo       `json:"machine"`
	Energy      energyView        `json:"energy"`
	Sources     []inferenceSource `json:"sources"`
	Coverage    coverageView      `json:"coverage"`
	Requests    []meteredRequest  `json:"requests"`
	// ExternalActivity is what other tools did while the monitor watched, newest
	// first (bursts.go).
	ExternalActivity []externalRecord `json:"external_activity"`
	History          historyView      `json:"history"`
}

// coverageView counts only sources detected on this machine. It cannot assert
// that an arbitrary client outside the edge is measured; a source without a
// per-request log remains visible as activity only.
type coverageView struct {
	Edge                 string `json:"edge"`
	ExternalMeasured     int    `json:"external_measured"`
	ExternalActivityOnly int    `json:"external_activity_only"`
}

func coverageOf(telemetry string, sources []inferenceSource) coverageView {
	view := coverageView{Edge: telemetry}
	for _, source := range sources {
		if source.Metering == "server-log" {
			view.ExternalMeasured++
		} else {
			view.ExternalActivityOnly++
		}
	}
	return view
}

// energyView is the electricity the GPU has drawn since the monitor started,
// integrated from its power readings. ObservedSeconds is how much of that time
// had a reading, so a session average is honest about what it averages.
type energyView struct {
	SessionWh       *float64 `json:"session_wh"`
	ObservedSeconds float64  `json:"observed_seconds"`
	Since           string   `json:"since"`
}

type monitorInfo struct {
	Version     string `json:"version"`
	Environment string `json:"environment"`
	StartedAt   string `json:"started_at"`
	IntervalMS  int64  `json:"interval_ms"`
	DataURL     string `json:"data_url"`
	ControlURL  string `json:"control_url"`
}

// edgeView carries the last status even when it is old, with its age: the
// roster and the release identity are still worth showing while the edge
// restarts, as long as the page can say they are stale.
type edgeView struct {
	Reachable   bool           `json:"reachable"`
	Telemetry   string         `json:"telemetry"`
	StatusAgeMS *int64         `json:"status_age_ms"`
	Status      *edgeStatus    `json:"status"`
	Inference   *edgeInference `json:"inference"`
}

type activity struct {
	Phase  string    `json:"phase"`
	Model  string    `json:"model,omitempty"`
	Queued int64     `json:"queued"`
	Live   *edgeLive `json:"live,omitempty"`
	// External is the tool other than the edge that is using or holding a
	// model, when there is one. It is set beside the edge's own phase, so the
	// page can say both.
	External *externalView `json:"external,omitempty"`
}

type externalView struct {
	SourceID        string   `json:"source_id"`
	Kind            string   `json:"kind"`
	Label           string   `json:"label"`
	Model           string   `json:"model,omitempty"`
	Activity        string   `json:"activity"`
	Basis           string   `json:"basis,omitempty"`
	TokensPerSecond *float64 `json:"tokens_per_second"`
}

type historyView struct {
	TokensPerSecond    []*float64 `json:"tokens_per_second"`
	GPUUtil            []*float64 `json:"gpu_util"`
	VRAMDedicatedMiB   []*float64 `json:"vram_dedicated_mib"`
	VRAMSharedMiB      []*float64 `json:"vram_shared_mib"`
	CPUUtil            []*float64 `json:"cpu_util"`
	RAMUsedBytes       []*float64 `json:"ram_used_bytes"`
	CommitUsedBytes    []*float64 `json:"commit_used_bytes"`
	DiskBytesPerSecond []*float64 `json:"disk_bytes_per_second"`
	PowerW             []*float64 `json:"power_w"`
	GPUTempC           []*float64 `json:"gpu_temp_c"`
}

// series is a fixed ring. It never grows, so a monitor left open for a month
// holds the same two minutes it held after two minutes.
type series struct {
	values [historyLength]*float64
	next   int
	filled int
}

func (s *series) push(value *float64) {
	s.values[s.next] = value
	s.next = (s.next + 1) % historyLength
	if s.filled < historyLength {
		s.filled++
	}
}

// ordered returns the samples oldest first.
func (s *series) ordered() []*float64 {
	out := make([]*float64, 0, s.filled)
	start := (s.next - s.filled + historyLength) % historyLength
	for index := 0; index < s.filled; index++ {
		out = append(out, s.values[(start+index)%historyLength])
	}
	return out
}

// Collector samples once a second and keeps the encoded result, so every open
// page is served the same bytes and a second viewer costs nothing.
type Collector struct {
	options  Options
	edge     *edgeClient
	hardware hardwareSampler
	now      func() time.Time
	started  time.Time
	machine  machineInfo

	statusMu sync.Mutex
	status   *edgeStatus
	statusAt time.Time

	// Owned by the sampling goroutine.
	history struct {
		tokens, gpu, vram, shared, cpu, ram, commit, disk, power, temp series
	}
	bursts   burstTracker
	powerLog []timedPower
	energy   struct {
		wh       float64
		observed time.Duration
		at       time.Time
		seen     bool
	}

	// sources finds the tools other than the edge; gpuProcs is the sampler's
	// latest view of who holds the adapter, which is what it looks from.
	sources  *discoverer
	gpuMu    sync.Mutex
	gpuProcs []gpuProcess

	control *controller
	encoded atomic.Pointer[[]byte]
}

// NewCollector prepares a collector; Run starts it.
func NewCollector(options Options) *Collector {
	collector := newCollector(options, newHardwareSampler(), time.Now)
	collector.control = newController(options, collector)
	collector.sources = newDiscoverer(newHostView(), edgePorts(options), uint32(os.Getpid()))
	collector.control.sources = collector.sources.snapshot
	collector.control.terminator = newTerminator()
	collector.control.selfPID = uint32(os.Getpid())
	return collector
}

// edgePorts are the ports the edge answers on. The monitor already reads the
// control one and never contacts the data one, so discovery leaves both alone.
func edgePorts(options Options) []uint16 {
	var ports []uint16
	for _, address := range []*url.URL{options.ControlURL, options.DataURL} {
		if address == nil {
			continue
		}
		if port, err := strconv.ParseUint(address.Port(), 10, 16); err == nil {
			ports = append(ports, uint16(port))
		}
	}
	return ports
}

func newCollector(options Options, hardware hardwareSampler, now func() time.Time) *Collector {
	return &Collector{
		options:  options,
		edge:     newEdgeClient(options.ControlURL),
		hardware: hardware,
		now:      now,
		started:  now(),
		machine:  hardware.info(),
	}
}

// newController binds the controls to this collector's view of the edge: the
// roster a request is checked against is the status the page is showing.
func newController(options Options, collector *Collector) *controller {
	log := options.Log
	if log == nil {
		log = func(string, map[string]any) {}
	}
	control := &controller{
		environment: options.Environment,
		approver:    newApprover(),
		status: func() *edgeStatus {
			status, _ := collector.lastStatus()
			return status
		},
		refresh: func() { collector.refreshStatus(context.Background()) },
		now:     collector.now,
		log:     log,
	}
	switch {
	case options.Control.Pipe == "":
		control.reason = "disabled"
	case runtime.GOOS != "windows":
		control.reason = "unsupported"
	default:
		client, err := adminpipe.NewClient(adminpipe.DialOptions{
			Name:               options.Control.Pipe,
			Timeout:            operationTimeout,
			ExpectedServerPath: options.Control.Server,
		})
		if err != nil {
			control.reason = "misconfigured"
		} else {
			control.executor = client
		}
	}
	return control
}

// Run samples until ctx ends. The first sample is taken at once, so a page
// opened immediately after start is not left empty for a second.
func (c *Collector) Run(ctx context.Context) {
	defer c.hardware.close()
	go c.pollStatus(ctx)
	go c.sources.run(ctx, c.gpuProcesses)
	c.refreshStatus(ctx)
	c.tick(ctx)
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

// Snapshot returns the latest encoded snapshot, or nil before the first one.
func (c *Collector) Snapshot() []byte {
	if encoded := c.encoded.Load(); encoded != nil {
		return *encoded
	}
	return nil
}

func (c *Collector) gpuProcesses() []gpuProcess {
	c.gpuMu.Lock()
	defer c.gpuMu.Unlock()
	return c.gpuProcs
}

func (c *Collector) pollStatus(ctx context.Context) {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshStatus(ctx)
		}
	}
}

func (c *Collector) refreshStatus(ctx context.Context) {
	requestCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	status, err := c.edge.status(requestCtx)
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if err == nil {
		c.status = status
		c.statusAt = c.now()
	}
}

func (c *Collector) lastStatus() (*edgeStatus, time.Time) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	return c.status, c.statusAt
}

// tick takes one sample: the edge's live numbers and the machine's, side by
// side, so the page never pairs a GPU reading with a request from another
// second.
func (c *Collector) tick(ctx context.Context) {
	status, statusAt := c.lastStatus()

	var inference *edgeInference
	var inferenceErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		requestCtx, cancel := context.WithTimeout(ctx, inferenceTimeout)
		defer cancel()
		inference, inferenceErr = c.edge.inference(requestCtx)
	}()
	preferred := ""
	if status != nil {
		preferred = status.GPUMemory.Adapter
	}
	sample := c.hardware.sample(preferred)
	<-done

	now := c.now()
	view := edgeView{Status: status, Inference: inference}
	switch {
	case inferenceErr == nil:
		view.Reachable, view.Telemetry = true, "ok"
	case errors.Is(inferenceErr, errTelemetryUnavailable):
		// An older edge: reachable, without per-request numbers.
		view.Reachable, view.Telemetry = true, "unavailable"
	default:
		view.Telemetry = "error"
	}
	if status != nil {
		age := now.Sub(statusAt).Milliseconds()
		view.StatusAgeMS = &age
	}

	c.gpuMu.Lock()
	c.gpuProcs = sample.GPUProcesses
	c.gpuMu.Unlock()
	sources := c.sources.snapshot()
	if sources == nil {
		sources = []inferenceSource{}
	}
	if sample.GPUProcesses == nil {
		sample.GPUProcesses = []gpuProcess{}
	}

	c.bursts.observe(now, sources, sample.PowerW)
	current := deriveActivity(view.Reachable, status, inference, sources)
	c.record(current, sample, now)

	external := c.externalActivity()
	encoded, err := json.Marshal(snapshot{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Monitor: monitorInfo{
			Version:     c.options.Version,
			Environment: c.options.Environment,
			StartedAt:   c.started.UTC().Format(time.RFC3339Nano),
			IntervalMS:  sampleInterval.Milliseconds(),
			DataURL:     urlString(c.options.DataURL),
			ControlURL:  urlString(c.options.ControlURL),
		},
		Activity:         current,
		Control:          c.control.view(),
		Edge:             view,
		Hardware:         sample,
		Machine:          c.machine,
		Energy:           c.energyView(),
		Sources:          sources,
		Coverage:         coverageOf(view.Telemetry, sources),
		Requests:         combineMeasuredRequests(inference, c.sources.loggedRecords()),
		ExternalActivity: external,
		History: historyView{
			TokensPerSecond:    c.history.tokens.ordered(),
			GPUUtil:            c.history.gpu.ordered(),
			VRAMDedicatedMiB:   c.history.vram.ordered(),
			VRAMSharedMiB:      c.history.shared.ordered(),
			CPUUtil:            c.history.cpu.ordered(),
			RAMUsedBytes:       c.history.ram.ordered(),
			CommitUsedBytes:    c.history.commit.ordered(),
			DiskBytesPerSecond: c.history.disk.ordered(),
			PowerW:             c.history.power.ordered(),
			GPUTempC:           c.history.temp.ordered(),
		},
	})
	if err == nil {
		c.encoded.Store(&encoded)
	}
}

func (c *Collector) record(current activity, sample hardwareSample, now time.Time) {
	c.history.power.push(sample.PowerW)
	c.logPower(sample.PowerW, now)
	c.history.temp.push(sample.GPUTempC)
	c.integrateEnergy(sample.PowerW, now)
	c.history.tokens.push(generationRate(current))
	c.history.gpu.push(sample.GPUUtil)
	c.history.vram.push(sample.VRAMDedicatedMiB)
	c.history.shared.push(sample.VRAMSharedMiB)
	c.history.cpu.push(sample.CPUUtil)
	c.history.ram.push(sample.RAMUsedBytes)
	c.history.commit.push(sample.CommitUsedBytes)
	var disk *float64
	if sample.DiskReadBPS != nil || sample.DiskWriteBPS != nil {
		total := 0.0
		if sample.DiskReadBPS != nil {
			total += *sample.DiskReadBPS
		}
		if sample.DiskWriteBPS != nil {
			total += *sample.DiskWriteBPS
		}
		disk = &total
	}
	c.history.disk.push(disk)
}

// generationRate is what the speed sparkline plots: the live decode rate while
// tokens arrive, zero while nothing is being generated - which is true, not a
// gap - and nothing at all when the edge could not be asked.
func generationRate(current activity) *float64 {
	switch {
	case current.Phase == phaseOffline:
		return nil
	case current.Phase == phaseGenerating && current.Live != nil:
		return current.Live.TokensPerSecond
	case current.Phase == phaseExternal && current.External != nil:
		// Another tool's rate is known only when its server reports slots;
		// otherwise it is unknown, which is not the same as zero.
		return current.External.TokensPerSecond
	default:
		return ptr(0)
	}
}

// deriveActivity names what the server is doing, from the most specific
// evidence to the least. A request in flight outranks the router's state, and
// the queue is reported beside it rather than hiding it.
func deriveActivity(reachable bool, status *edgeStatus, inference *edgeInference, sources []inferenceSource) activity {
	current := deriveEdgeActivity(reachable, status, inference)
	external := pickExternal(sources)
	if external == nil {
		return current
	}
	view := externalOf(external)
	current.External = &view
	switch current.Phase {
	case phaseGenerating, phasePrompt, phaseLoading, phaseWorking, phaseQueued, phaseDraining:
		// The edge is doing something and leads; the other tool is noted.
		return current
	}
	switch {
	case view.Activity == activityGenerating:
		current.Phase, current.Model = phaseExternal, view.Model
	case current.Phase == phaseOffline || current.Phase == phaseIdle:
		// The edge has nothing to say and another tool holds a model.
		current.Phase, current.Model = phaseExternalReady, view.Model
	}
	return current
}

func deriveEdgeActivity(reachable bool, status *edgeStatus, inference *edgeInference) activity {
	if !reachable {
		return activity{Phase: phaseOffline}
	}
	gate := edgeGate{}
	switch {
	case inference != nil:
		gate = inference.Gate
	case status != nil:
		gate = status.Gate
	}
	current := activity{Queued: gate.Queued}
	if status != nil && status.Maintenance.Draining {
		current.Phase = phaseDraining
		return current
	}
	if inference != nil && len(inference.Live) > 0 {
		live := inference.Live[0]
		current.Live = &live
		current.Model = live.Model
		switch {
		case live.Phase == "generating":
			current.Phase = phaseGenerating
		case processStarting(status, live.Model):
			current.Phase = phaseLoading
		case !live.Stream:
			current.Phase = phaseWorking
		default:
			current.Phase = phasePrompt
		}
		return current
	}
	if model := startingModel(status); model != "" {
		current.Phase, current.Model = phaseLoading, model
		return current
	}
	switch {
	case gate.Active > 0:
		// Only an edge without telemetry gets here: something is admitted and
		// the edge cannot say what.
		current.Phase = phaseWorking
	case gate.Queued > 0:
		current.Phase = phaseQueued
	case status != nil && status.ActiveModel != "":
		current.Phase, current.Model = phaseReady, status.ActiveModel
	default:
		current.Phase = phaseIdle
	}
	return current
}

func processStarting(status *edgeStatus, model string) bool {
	if status == nil {
		return false
	}
	for _, entry := range status.ModelStatuses {
		if entry.ID == model {
			return entry.ProcessState == "starting"
		}
	}
	return false
}

func startingModel(status *edgeStatus) string {
	if status == nil {
		return ""
	}
	for _, entry := range status.ModelStatuses {
		if entry.ProcessState == "starting" {
			return entry.ID
		}
	}
	return ""
}

func urlString(value *url.URL) string {
	if value == nil {
		return ""
	}
	return value.String()
}

// integrateEnergy adds the interval since the last reading at the power just
// read. The rectangle rule is what a one-second sampler can honestly claim.
func (c *Collector) integrateEnergy(power *float64, now time.Time) {
	previous := c.energy.at
	c.energy.at = now
	if power == nil || previous.IsZero() {
		return
	}
	elapsed := now.Sub(previous)
	if elapsed <= 0 || elapsed > energyGap {
		return
	}
	c.energy.seen = true
	c.energy.observed += elapsed
	c.energy.wh += *power * elapsed.Hours()
}

func (c *Collector) energyView() energyView {
	view := energyView{Since: c.started.UTC().Format(time.RFC3339Nano), ObservedSeconds: c.energy.observed.Seconds()}
	if c.energy.seen {
		view.SessionWh = ptr(c.energy.wh)
	}
	return view
}

// pickExternal chooses the source that best describes what the machine is doing
// outside the edge: one that is working, else one that holds a model.
func pickExternal(sources []inferenceSource) *inferenceSource {
	var holding *inferenceSource
	for index := range sources {
		source := &sources[index]
		if source.Activity == activityGenerating {
			return source
		}
		if holding == nil && holdsModel(source) {
			holding = source
		}
	}
	return holding
}

// holdsModel is true for a source that reported a model, and also for a tool
// whose API could not be read but which holds real GPU memory: a chat app with
// a protected server has a model loaded whether or not it will say which.
func holdsModel(source *inferenceSource) bool {
	if len(source.Models) > 0 {
		return true
	}
	return source.Status != sourceOK && source.VRAMDedicatedMiB != nil && *source.VRAMDedicatedMiB >= sourceMinVRAMMiB
}

func externalOf(source *inferenceSource) externalView {
	view := externalView{
		SourceID: source.ID, Kind: source.Kind, Label: source.Label, Activity: source.Activity,
		Basis: source.ActivityBasis, TokensPerSecond: source.TokensPerSecond,
	}
	if len(source.Models) > 0 {
		view.Model = source.Models[0].ID
	}
	return view
}

// timedPower is one reading of the board's power with the time it was taken.
type timedPower struct {
	at    time.Time
	watts float64
}

// powerLogLength is thirty minutes of one-second readings: long enough to give
// energy to a request read from a server log a little after it happened.
const powerLogLength = 1800

func (c *Collector) logPower(power *float64, now time.Time) {
	if power == nil {
		return
	}
	c.powerLog = append(c.powerLog, timedPower{at: now, watts: *power})
	if len(c.powerLog) > powerLogLength {
		c.powerLog = c.powerLog[len(c.powerLog)-powerLogLength:]
	}
}

// energyDuring integrates the readings that fall inside a request's interval. It
// is the whole board's energy in that time, and says nothing if no reading falls
// in it.
func (c *Collector) energyDuring(start, end time.Time) (wh, peak float64, ok bool) {
	for _, reading := range c.powerLog {
		if reading.at.Before(start) || reading.at.After(end) {
			continue
		}
		ok = true
		wh += reading.watts * sampleInterval.Hours()
		if reading.watts > peak {
			peak = reading.watts
		}
	}
	return wh, peak, ok
}

// externalActivity is what the page lists as other tools' activity: the requests
// read from server logs, which carry exact numbers, and the GPU-only estimates
// for tools that leave no log. An estimate that overlaps a logged request of the
// same source is the same request and is dropped.
func (c *Collector) externalActivity() []externalRecord {
	logged := c.sources.loggedRecords()
	estimates := c.bursts.records()
	merged := make([]externalRecord, 0, len(logged)+len(estimates))
	for _, record := range logged {
		if record.EnergyWh == nil {
			start := record.startTime()
			if wh, peak, ok := c.energyDuring(start, start.Add(time.Duration(record.DurationMS)*time.Millisecond)); ok {
				record.EnergyWh, record.PeakPowerW = ptr(wh), ptr(peak)
			}
		}
		merged = append(merged, record)
	}
	for _, estimate := range estimates {
		if !overlapsLogged(estimate, logged) {
			merged = append(merged, estimate)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].startTime().After(merged[j].startTime()) })
	if len(merged) > maxBursts {
		merged = merged[:maxBursts]
	}
	return merged
}

const overlapSlack = 3 * time.Second

func overlapsLogged(estimate externalRecord, logged []externalRecord) bool {
	start := estimate.startTime()
	end := start.Add(time.Duration(estimate.DurationMS) * time.Millisecond)
	for _, record := range logged {
		if record.SourceID != estimate.SourceID {
			continue
		}
		recordStart := record.startTime()
		recordEnd := recordStart.Add(time.Duration(record.DurationMS) * time.Millisecond)
		if start.Before(recordEnd.Add(overlapSlack)) && end.After(recordStart.Add(-overlapSlack)) {
			return true
		}
	}
	return false
}
