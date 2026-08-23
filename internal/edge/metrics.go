package edge

import (
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
	"time"
)

// metrics holds every process counter exposed on /metrics. Names are part of
// the operational contract and must stay stable; nothing here is labelled by
// request ID, prompt, path, or any other unbounded value.
type metrics struct {
	requests         atomic.Uint64
	authFailures     atomic.Uint64
	invalidRequests  atomic.Uint64
	upstreamFailures atomic.Uint64

	// httpAdminMutations counts administrative mutations that still arrived
	// over the deprecated HTTP control plane. It is the migration signal for
	// retiring that surface once every client speaks the named pipe.
	httpAdminMutations atomic.Uint64

	modelLoads        atomic.Uint64
	modelUnloads      atomic.Uint64
	modelSwitches     atomic.Uint64
	modelLoadFailures atomic.Uint64

	inferenceDuration *histogram
	modelLoadDuration *histogram
}

func newMetrics() *metrics {
	return &metrics{
		// Sized for the observed generation envelope: a 32768-token completion
		// on this adapter is minutes, not seconds, so the tail buckets matter
		// more than sub-second resolution.
		inferenceDuration: newHistogram(0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800, 3600),
		modelLoadDuration: newHistogram(1, 5, 10, 30, 60, 120, 300, 600),
	}
}

func (m *metrics) recordModelOperation(operation string, elapsed time.Duration, ok bool) {
	switch operation {
	case "load":
		m.modelLoads.Add(1)
		m.modelLoadDuration.observe(elapsed)
	case "unload":
		m.modelUnloads.Add(1)
	case "switch":
		m.modelSwitches.Add(1)
		m.modelLoadDuration.observe(elapsed)
	}
	if !ok && operation != "unload" {
		m.modelLoadFailures.Add(1)
	}
}

// histogram is a fixed-bucket, lock-free Prometheus histogram. Bounds are
// compiled in, so the exported series count is constant for the process.
type histogram struct {
	bounds  []float64
	buckets []atomic.Uint64
	sumNS   atomic.Uint64
	count   atomic.Uint64
}

func newHistogram(bounds ...float64) *histogram {
	return &histogram{bounds: bounds, buckets: make([]atomic.Uint64, len(bounds))}
}

func (h *histogram) observe(elapsed time.Duration) {
	if h == nil {
		return
	}
	if elapsed < 0 {
		elapsed = 0
	}
	seconds := elapsed.Seconds()
	for index, bound := range h.bounds {
		if seconds <= bound {
			h.buckets[index].Add(1)
		}
	}
	h.count.Add(1)
	h.sumNS.Add(uint64(elapsed.Nanoseconds()))
}

func (h *histogram) write(w io.Writer, name, help string) {
	if h == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	for index, bound := range h.bounds {
		_, _ = fmt.Fprintf(w, "%s_bucket{le=\"%s\"} %d\n", name, strconv.FormatFloat(bound, 'g', -1, 64), h.buckets[index].Load())
	}
	total := h.count.Load()
	_, _ = fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", name, total)
	_, _ = fmt.Fprintf(w, "%s_sum %s\n", name, strconv.FormatFloat(float64(h.sumNS.Load())/1e9, 'f', 6, 64))
	_, _ = fmt.Fprintf(w, "%s_count %d\n", name, total)
}

func writeCounter(w io.Writer, name, help string, value uint64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
}

func writeGauge(w io.Writer, name, help string, value int64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, value)
}

func writeFloatGauge(w io.Writer, name, help, format string, value float64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s "+format+"\n", name, help, name, name, value)
}

// writeReleaseInfo emits one constant series naming the installed release. The
// value is always 1; the identity lives in the labels, all of which are
// validated at load and reduced by metricLabel, so the series count is exactly
// one for the lifetime of the process.
func writeReleaseInfo(w io.Writer, release *ReleaseInfo) {
	if release == nil {
		return
	}
	const name = "cia_edge_release_info"
	_, _ = fmt.Fprintf(w, "# HELP %s Installed release identity. Always 1; the identity is in the labels.\n# TYPE %s gauge\n", name, name)
	_, _ = fmt.Fprintf(w, "%s{environment=%q,release=%q,version=%q,commit=%q,status=%q} 1\n",
		name,
		metricLabel(release.Environment),
		metricLabel(release.Release),
		metricLabel(release.Version),
		metricLabel(release.Commit),
		metricLabel(release.Status),
	)
}

// metricLabel keeps a label value inside a conservative, escape-free character
// set. Release identifiers and versions already satisfy it; anything that does
// not is reduced rather than emitted raw, so a malformed value can never break
// the exposition format.
func metricLabel(value string) string {
	const maxLabelBytes = 128
	if len(value) > maxLabelBytes {
		value = value[:maxLabelBytes]
	}
	result := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		c := value[index]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			result = append(result, c)
		default:
			result = append(result, '_')
		}
	}
	return string(result)
}
