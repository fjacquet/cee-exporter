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
	"sync/atomic"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// drainGracePeriod is how long Stop waits for in-flight WriteBatch calls
// after the drain timeout expires and writeCancel has fired.
//
// 5 seconds because that is pkg/evtx's writeDeadline: a socket write stalled
// against an unresponsive collector returns an error at exactly that mark, so
// a shorter grace would walk away while the write was still guaranteed to
// resolve on its own, and a longer one would only wait on a worker that has
// already been given its answer. It is not operator-tunable: drain_timeout_s
// is the policy knob for "how long may shutdown take", and this is the fixed
// cost of unwinding a write that policy interrupted.
const drainGracePeriod = 5 * time.Second

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
	// MaxBatch is the largest number of events handed to one WriteBatch call.
	// 500 is the throughput choice; it is also the duplicate blast radius,
	// because a TCP write failing after partially landing replays the whole
	// batch.
	MaxBatch int
	// BatchTimeout bounds how long a partial batch waits for more events.
	// It is also the loss window: events held here are gone on SIGKILL.
	BatchTimeout time.Duration
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
	if c.MaxBatch <= 0 {
		c.MaxBatch = 500
	}
	if c.BatchTimeout <= 0 {
		c.BatchTimeout = 200 * time.Millisecond
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
	//
	// writeCancel is currently a forward-looking hook, not an active brake:
	// no writer in pkg/evtx consults the context on its write path (GELF,
	// syslog, EVTX and Win32 all take `_ context.Context`; Beats observes it
	// only inside dial). Cancelling therefore does not shorten an in-flight
	// write today. It is kept because the plumbing must exist before any
	// writer can honour it, and because Stop's grace period below is what
	// actually bounds the wait in the meantime — do not cite writeCancel as
	// the reason a stalled write is accounted for.
	writeCtx    context.Context
	writeCancel context.CancelFunc

	maxBatch     int
	batchTimeout time.Duration

	// inFlight counts events that have left the channel and are inside a
	// WriteBatch call. Without it the drain-timeout report is blind to
	// workers × maxBatch events (4 × 500 = 2000 at the defaults) and reports
	// a total loss as events_undrained=0 with every counter at zero.
	inFlight atomic.Int64

	// newTimer returns the channel that fires after d, and a stop function.
	// Injected in tests so the batch-timeout path is driven deterministically:
	// this package's tests may not use time.Sleep for synchronisation.
	newTimer func(d time.Duration) (<-chan time.Time, func() bool)

	// drainGrace is drainGracePeriod, overridable by tests so exercising the
	// drain-timeout path does not cost five wall-clock seconds.
	drainGrace time.Duration
}

func realTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// New creates a Queue from cfg.  Call Start() to launch the workers.
func New(cfg Config, w evtx.Writer) *Queue {
	cfg = cfg.withDefaults()
	return &Queue{
		ch:           make(chan evtx.WindowsEvent, cfg.Capacity),
		writer:       w,
		workers:      cfg.Workers,
		drainTimeout: cfg.DrainTimeout,
		maxBatch:     cfg.MaxBatch,
		batchTimeout: cfg.BatchTimeout,
		newTimer:     realTimer,
		drainGrace:   drainGracePeriod,
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
	timedOut := false
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
	}

	// Cancel any write still stalled, then close. Writers serialise Close
	// against their write path with their own mutex, so this cannot tear a
	// write in progress. See the writeCancel field comment: no writer honours
	// the context on its write path today, so this does not by itself shorten
	// anything — the grace wait below is what bounds it.
	q.writeCancel()

	if timedOut {
		// One more short wait before giving up. A worker inside WriteBatch
		// holds up to maxBatch events that are in neither the channel nor the
		// writer; walking away the instant the drain deadline passes loses
		// them for the sake of a few seconds. The grace is separate from
		// drain_timeout because it is not operator policy: it is the time a
		// write already in progress needs to either land or fail.
		grace := time.NewTimer(q.drainGrace)
		select {
		case <-done:
		case <-grace.C:
		}
		grace.Stop()

		// Count and log what is still unwritten. This is deliberately measured
		// after the grace, so a batch that landed during it is not reported as
		// dropped — and deliberately includes inFlight, because len(q.ch)
		// alone cannot see the workers × maxBatch events held inside
		// WriteBatch. A shutdown that lost 2000 audit events used to log
		// events_undrained=0 with EventsDroppedTotal and WriterErrorsTotal
		// both at zero: a silent total loss reported as a clean shutdown, the
		// exact failure mode this branch exists to prevent.
		undrained := len(q.ch) + int(q.inFlight.Load())
		metrics.M.EventsDroppedTotal.Add(int64(undrained))
		slog.Error("queue_drain_timeout",
			"drain_timeout", q.drainTimeout,
			"grace_period", q.drainGrace,
			"events_undrained", undrained,
		)
	}

	if err := q.writer.Close(); err != nil {
		slog.Error("writer_close_error", "error", err)
	}
}

func (q *Queue) work(ctx context.Context, id int) {
	defer q.wg.Done()
	slog.Debug("worker_started", "worker_id", id)
	for {
		batch, more := q.nextBatch()
		if len(batch) > 0 {
			q.writeBatch(ctx, batch, id)
		}
		if !more {
			slog.Debug("worker_stopped", "worker_id", id)
			return
		}
	}
}

// nextBatch blocks for the first event, then accumulates until maxBatch is
// reached or batchTimeout expires.
//
// more=false means the channel closed. The returned batch is still valid and
// the caller MUST write it: returning early on close would discard up to
// maxBatch-1 events per worker on every shutdown, uncounted and unlogged.
func (q *Queue) nextBatch() (batch []evtx.WindowsEvent, more bool) {
	first, ok := <-q.ch
	if !ok {
		return nil, false
	}

	batch = make([]evtx.WindowsEvent, 0, q.maxBatch)
	batch = append(batch, first)

	fire, stop := q.newTimer(q.batchTimeout)
	defer stop()

	for len(batch) < q.maxBatch {
		select {
		case e, ok := <-q.ch:
			if !ok {
				return batch, false
			}
			batch = append(batch, e)
		case <-fire:
			return batch, true
		}
	}
	return batch, true
}

// writeBatch hands the batch to the writer and records the outcome.
//
// EventsWrittenTotal and WriterErrorsTotal advance by len(batch), not by one:
// they mean events, and every dashboard and alert built on them assumes so.
// The call-level counters are separate.
func (q *Queue) writeBatch(ctx context.Context, batch []evtx.WindowsEvent, id int) {
	metrics.M.SetQueueDepth(len(q.ch))
	metrics.M.WriterBatchesTotal.Add(1)

	// These events are out of the channel and not yet through the writer, so
	// they are invisible to len(q.ch). Stop's drain-timeout report reads
	// inFlight to see them; without it a shutdown that loses a whole batch
	// per worker reports events_undrained=0.
	q.inFlight.Add(int64(len(batch)))
	defer q.inFlight.Add(-int64(len(batch)))

	if err := q.writer.WriteBatch(ctx, batch); err != nil {
		// Charged whole-batch, and deliberately pessimistic. The serial
		// fallback path (writeBatchSerially) attempts every event and joins
		// the failures, so a batch that reports an error may still have
		// written most of its records — 499 of 500 in the worst case. Those
		// land in WriterErrorsTotal and never in EventsWrittenTotal, so a
		// partial failure under-reports successful writes rather than
		// over-reporting them. Err toward the alarm, not away from it.
		metrics.M.WriterErrorsTotal.Add(int64(len(batch)))
		metrics.M.WriterBatchErrorsTotal.Add(1)
		// One line per batch, not per event: a failed 500-event batch would
		// otherwise emit 500 identical lines. The first event identifies
		// where in the stream the failure sits.
		slog.Error("writer_batch_error",
			"worker_id", id,
			"batch_size", len(batch),
			"events_failed", len(batch),
			"first_event_id", batch[0].EventID,
			"first_cepa_event_type", batch[0].CEPAEventType,
			"first_file_path", batch[0].ObjectName,
			"error", err,
		)
		return
	}

	metrics.M.EventsWrittenTotal.Add(int64(len(batch)))
	metrics.M.RecordEventAt()
}
