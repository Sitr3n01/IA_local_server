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
)

type gate struct {
	slots       chan struct{}
	maxQueue    int64
	wait        time.Duration
	active      atomic.Int64
	queued      atomic.Int64
	rejected    atomic.Uint64
	timedOut    atomic.Uint64
	control     sync.Mutex
	maintenance bool
}

func newGate(maxActive, maxQueue int, wait time.Duration) *gate {
	return &gate{
		slots:    make(chan struct{}, maxActive),
		maxQueue: int64(maxQueue),
		wait:     wait,
	}
}

func (g *gate) acquire(ctx context.Context) (func(), error) {
	g.control.Lock()
	if g.maintenance {
		g.control.Unlock()
		return nil, errControlBusy
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

	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		g.control.Lock()
		g.active.Add(1)
		g.queued.Add(-1)
		g.control.Unlock()
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
	if g.maintenance || g.active.Load() != 0 || g.queued.Load() != 0 {
		return nil, false
	}
	g.maintenance = true
	return func() {
		g.control.Lock()
		g.maintenance = false
		g.control.Unlock()
	}, true
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
