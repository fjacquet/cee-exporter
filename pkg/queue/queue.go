// Package queue implements an async worker pool that decouples the HTTP
// handler (fast path, CEPA timeout-sensitive) from the event writer (slow
// path, I/O).
//
// Architecture:
//
//	HTTP handler → Enqueue() → buffered channel → N worker goroutines → Writer
//
// On queue-full, events are dropped and counted in metrics.EventsDroppedTotal.
// Graceful shutdown drains the queue within a configurable timeout.
package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// Config carries the queue's tunables. A struct rather than positional
// parameters because the batching fields land here next and a third and
// fourth positional int would be unreadable at the call site.
type Config struct {
	// Capacity is the channel depth. Events arriving at a full queue are
	// dropped and counted.
	Capacity int
	// Workers is the number of drain goroutines.
	Workers int
	// DrainTimeout bounds how long Stop waits for workers to finish. On
	// expiry the remaining depth is logged at ERROR; it is not silently
	// discarded.
	DrainTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Capacity <= 0 {
		c.Capacity = 100000
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = 30 * time.Second
	}
	return c
}

// Queue dispatches WindowsEvents to a Writer using a pool of workers.
type Queue struct {
	ch           chan evtx.WindowsEvent
	writer       evtx.Writer
	workers      int
	drainTimeout time.Duration
	wg           sync.WaitGroup
	// mu guards closed. Enqueue takes RLock and Stop takes Lock, so a send on
	// q.ch cannot race with close(q.ch): the send holds a read lock that the
	// close must exclude. RLock is uncontended in the normal case and costs
	// ~20 ns against an 8.6 µs parse, so the fast path is unaffected.
	mu     sync.RWMutex
	closed bool
	// writeCtx/writeCancel bound writes during the drain triggered by Stop.
	// See Start for why this is derived with context.WithoutCancel rather
	// than inherited directly from the caller's context.
	writeCtx    context.Context
	writeCancel context.CancelFunc
}

// New creates a Queue from cfg.  Call Start() to launch the workers.
func New(cfg Config, w evtx.Writer) *Queue {
	cfg = cfg.withDefaults()
	return &Queue{
		ch:           make(chan evtx.WindowsEvent, cfg.Capacity),
		writer:       w,
		workers:      cfg.Workers,
		drainTimeout: cfg.DrainTimeout,
	}
}

// Start launches the worker goroutines.
//
// Writes run under a context derived from ctx with context.WithoutCancel:
// values are kept, cancellation is not. The caller's context is already
// cancelled by the time Stop() drains on the Windows SCM Stop path
// (cmd/cee-exporter/main.go), and inheriting that cancellation would abort
// the shutdown flush itself rather than the individual writes it is meant to
// bound. Stop() cancels writeCtx once the drain is finished or its deadline
// has passed.
func (q *Queue) Start(ctx context.Context) {
	q.writeCtx, q.writeCancel = context.WithCancel(context.WithoutCancel(ctx))
	for i := range q.workers {
		q.wg.Add(1)
		go q.work(q.writeCtx, i)
	}
}

// Enqueue adds an event to the queue.  If the queue is full the event is
// dropped and the counter is incremented.  This call never blocks.
//
// After Stop it refuses and reports false rather than panicking. main.go
// reaches Stop even when httpServer.Shutdown timed out, so a live handler
// calling Enqueue after the channel closed is a reachable state, not a
// theoretical one.
func (q *Queue) Enqueue(e evtx.WindowsEvent) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.closed {
		metrics.M.EventsDroppedTotal.Add(1)
		slog.Warn("enqueue_after_stop_event_dropped",
			"events_dropped_total", metrics.M.EventsDroppedTotal.Load(),
			"cepa_event_type", e.CEPAEventType,
			"file_path", e.ObjectName,
		)
		return false
	}

	select {
	case q.ch <- e:
		metrics.M.SetQueueDepth(len(q.ch))
		return true
	default:
		metrics.M.EventsDroppedTotal.Add(1)
		slog.Warn("queue_full_event_dropped",
			"queue_depth", len(q.ch),
			"events_dropped_total", metrics.M.EventsDroppedTotal.Load(),
			"cepa_event_type", e.CEPAEventType,
			"file_path", e.ObjectName,
		)
		return false
	}
}

// Len returns the current number of events waiting in the queue.
func (q *Queue) Len() int {
	return len(q.ch)
}

// Stop closes the input channel, waits for all workers to finish draining,
// then closes the writer. It is idempotent: a second call returns
// immediately rather than closing the channel twice.
func (q *Queue) Stop() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.ch)
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(q.drainTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// Log rather than hang. The events still in the channel are lost, and
		// a silent loss at shutdown is the failure mode this whole change
		// exists to avoid, so it is counted where an operator will see it.
		slog.Error("queue_drain_timeout",
			"drain_timeout", q.drainTimeout,
			"events_undrained", len(q.ch),
		)
	}

	// Cancel any write still stalled, then close. Writers serialise Close
	// against their write path with their own mutex, so this cannot tear a
	// write in progress.
	q.writeCancel()
	if err := q.writer.Close(); err != nil {
		slog.Error("writer_close_error", "error", err)
	}
}

func (q *Queue) work(ctx context.Context, id int) {
	defer q.wg.Done()
	slog.Debug("worker_started", "worker_id", id)
	for e := range q.ch {
		metrics.M.SetQueueDepth(len(q.ch))
		if err := q.writer.WriteEvent(ctx, e); err != nil {
			metrics.M.WriterErrorsTotal.Add(1)
			slog.Error("writer_error",
				"worker_id", id,
				"event_id", e.EventID,
				"cepa_event_type", e.CEPAEventType,
				"file_path", e.ObjectName,
				"error", err,
			)
		} else {
			metrics.M.EventsWrittenTotal.Add(1)
			metrics.M.RecordEventAt()
		}
	}
	slog.Debug("worker_stopped", "worker_id", id)
}
