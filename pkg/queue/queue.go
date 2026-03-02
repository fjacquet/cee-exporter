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

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// Queue dispatches WindowsEvents to a Writer using a pool of workers.
type Queue struct {
	ch      chan evtx.WindowsEvent
	writer  evtx.Writer
	workers int
	wg      sync.WaitGroup
}

// New creates a Queue with the given channel capacity and number of workers.
// Call Start() to launch the workers.
func New(capacity, workers int, w evtx.Writer) *Queue {
	return &Queue{
		ch:      make(chan evtx.WindowsEvent, capacity),
		writer:  w,
		workers: workers,
	}
}

// Start launches the worker goroutines.  The provided context is used to
// cancel long-running write operations; the queue itself drains only when
// Stop() is called.
func (q *Queue) Start(ctx context.Context) {
	for i := range q.workers {
		q.wg.Add(1)
		go q.work(ctx, i)
	}
}

// Enqueue adds an event to the queue.  If the queue is full the event is
// dropped and the counter is incremented.  This call never blocks.
func (q *Queue) Enqueue(e evtx.WindowsEvent) bool {
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
// then closes the writer.
func (q *Queue) Stop() {
	close(q.ch)
	q.wg.Wait()
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
