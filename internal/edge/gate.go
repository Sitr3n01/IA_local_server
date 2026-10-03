package edge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errQueueFull    = errors.New("inference queue is full")
	errQueueTimeout = errors.New("timed out waiting for inference capacity")
	errControlBusy  = errors.New("model control operation is in progress")
	errDraining     = errors.New("provider is draining for maintenance")
)

// Maintenance lifecycle. A drain refuses *new* admissions only: an admitted
// request keeps its slot and an already queued request keeps waiting for one,
// so the queue drains instead of being discarded.
const (
	maintenanceRunning     = "running"
	maintenanceDraining    = "draining"
	maintenanceDrained     = "maintenance"
	maintenanceRetryAfterS = "30"
)

type gate struct {
	slots    chan struct{}
	maxQueue int64
	wait     time.Duration
	active   atomic.Int64
	queued   atomic.Int64
	rejected atomic.Uint64
	timedOut atomic.Uint64
	// drainRejected counts admissions refused because the operator drained the
	// provider. It is deliberately separate from rejected/timedOut: a drain
	// refusal is a planned maintenance event, not queue pressure.
	drainRejected atomic.Uint64
	// queueWait records only admissions that actually waited for a slot.
	queueWait *histogram
	control   sync.Mutex
	// controlActive marks an in-progress model load/unload/switch. It excludes
	// inference for the duration of that one operation and is unrelated to the
	// operator-driven maintenance drain below.
	controlActive bool
	draining      bool
	drainSince    time.Time
}

func newGate(maxActive, maxQueue int, wait time.Duration) *gate {
	return &gate{
		slots:     make(chan struct{}, maxActive),
		maxQueue:  int64(maxQueue),
		wait:      wait,
		queueWait: newHistogram(0.05, 0.1, 0.5, 1, 5, 15, 30, 60, 120, 300),
	}
}

func (g *gate) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.control.Lock()
	if err := g.admissionError(); err != nil {
		g.control.Unlock()
		return nil, err
	}
	select {
	case g.slots <- struct{}{}:
		g.active.Add(1)
		g.control.Unlock()
		return g.release, nil
	default:
	}

	if !g.reserveQueueSlot() {
		g.rejected.Add(1)
		g.control.Unlock()
		return nil, errQueueFull
	}
	g.control.Unlock()

	queuedAt := time.Now()
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		g.control.Lock()
		g.active.Add(1)
		g.queued.Add(-1)
		g.control.Unlock()
		g.queueWait.observe(time.Since(queuedAt))
		return g.release, nil
	case <-ctx.Done():
		g.control.Lock()
		g.queued.Add(-1)
		g.control.Unlock()
		return nil, ctx.Err()
	case <-timer.C:
		g.timedOut.Add(1)
		g.control.Lock()
		g.queued.Add(-1)
		g.control.Unlock()
		return nil, errQueueTimeout
	}
}

// admissionError requires control to be locked by the caller.
func (g *gate) admissionError() error {
	if g.draining {
		g.drainRejected.Add(1)
		return errDraining
	}
	if g.controlActive {
		return errControlBusy
	}
	return nil
}

func (g *gate) reserveQueueSlot() bool {
	for {
		queued := g.queued.Load()
		if queued >= g.maxQueue {
			return false
		}
		if g.queued.CompareAndSwap(queued, queued+1) {
			return true
		}
	}
}

func (g *gate) release() {
	g.control.Lock()
	<-g.slots
	g.active.Add(-1)
	g.control.Unlock()
}

func (g *gate) beginControl() (func(), bool) {
	g.control.Lock()
	defer g.control.Unlock()
	if g.controlActive || g.active.Load() != 0 || g.queued.Load() != 0 {
		return nil, false
	}
	g.controlActive = true
	return func() {
		g.control.Lock()
		g.controlActive = false
		g.control.Unlock()
	}, true
}

// drain stops admitting new inference. It never cancels an admitted request and
// never discards a queued one; both are allowed to finish. Calling it while
// already draining preserves the original start time so the reported duration
// stays honest.
func (g *gate) drain(now time.Time) maintenanceSnapshot {
	g.control.Lock()
	if !g.draining {
		g.draining = true
		g.drainSince = now.UTC()
	}
	g.control.Unlock()
	return g.maintenance(now)
}

// resume returns the gate to RUNNING. Maintenance is process-local state: a
// restarted edge always comes back running, which keeps the edge stateless and
// makes a crash during maintenance recoverable without an operator.
func (g *gate) resume() maintenanceSnapshot {
	g.control.Lock()
	g.draining = false
	g.drainSince = time.Time{}
	g.control.Unlock()
	return g.maintenance(time.Now())
}

func (g *gate) maintenance(now time.Time) maintenanceSnapshot {
	g.control.Lock()
	draining := g.draining
	since := g.drainSince
	g.control.Unlock()

	active := g.active.Load()
	queued := g.queued.Load()
	snapshot := maintenanceSnapshot{
		State:    maintenanceRunning,
		Draining: draining,
		Active:   active,
		Queued:   queued,
		Rejected: g.drainRejected.Load(),
	}
	if !draining {
		return snapshot
	}
	snapshot.Drained = active == 0 && queued == 0
	snapshot.State = maintenanceDraining
	if snapshot.Drained {
		snapshot.State = maintenanceDrained
	}
	if !since.IsZero() {
		snapshot.Since = since.Format(time.RFC3339)
		seconds := int64(now.UTC().Sub(since) / time.Second)
		if seconds < 0 {
			seconds = 0
		}
		snapshot.Seconds = &seconds
	}
	return snapshot
}

func (g *gate) snapshot() gateSnapshot {
	return gateSnapshot{
		Active:      g.active.Load(),
		Queued:      g.queued.Load(),
		MaxActive:   cap(g.slots),
		MaxQueue:    int(g.maxQueue),
		WaitSeconds: int(g.wait / time.Second),
		Rejected:    g.rejected.Load(),
		TimedOut:    g.timedOut.Load(),
	}
}

type gateSnapshot struct {
	Active      int64  `json:"active"`
	Queued      int64  `json:"queued"`
	MaxActive   int    `json:"max_active"`
	MaxQueue    int    `json:"max_queue"`
	WaitSeconds int    `json:"wait_timeout_seconds"`
	Rejected    uint64 `json:"rejected_total"`
	TimedOut    uint64 `json:"timed_out_total"`
}

// maintenanceSnapshot is the operator-facing view of the drain lifecycle. It
// carries counts and timing only; nothing here identifies a request.
type maintenanceSnapshot struct {
	State    string `json:"state"`
	Draining bool   `json:"draining"`
	Drained  bool   `json:"drained"`
	Active   int64  `json:"active"`
	Queued   int64  `json:"queued"`
	Since    string `json:"since,omitempty"`
	Seconds  *int64 `json:"draining_seconds,omitempty"`
	Rejected uint64 `json:"rejected_total"`
}

func maintenanceLevel(state string) int {
	switch state {
	case maintenanceDraining:
		return 1
	case maintenanceDrained:
		return 2
	default:
		return 0
	}
}
