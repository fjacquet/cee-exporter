package queue

import (
	"context"
	"sync"
	"testing"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

type fakeWriter struct {
	mu     sync.Mutex
	events []evtx.WindowsEvent
	done   chan struct{}
}

func (f *fakeWriter) WriteEvent(_ context.Context, e evtx.WindowsEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	if f.done != nil {
		select {
		case f.done <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeWriter) Close() error { return nil }

func TestEnqueue(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)

	fw := &fakeWriter{done: make(chan struct{}, 1)}
	q := New(10, 1, fw)
	q.Start(context.Background())
	defer q.Stop()

	e := evtx.WindowsEvent{EventID: 4663, CEPAEventType: "CEPP_FILE_WRITE"}
	ok := q.Enqueue(e)
	if !ok {
		t.Fatal("enqueue returned false on non-full queue")
	}

	// Wait for the worker to process the event.
	<-fw.done

	fw.mu.Lock()
	defer fw.mu.Unlock()

	if len(fw.events) != 1 {
		t.Fatalf("expected 1 event written, got %d", len(fw.events))
	}
	if fw.events[0].EventID != 4663 {
		t.Errorf("expected EventID 4663, got %d", fw.events[0].EventID)
	}
}

func TestDropOnFull(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)

	fw := &fakeWriter{} // no done channel — queue is not started
	q := New(2, 1, fw)
	// Do NOT call q.Start() — workers not running ensures channel fills without being drained.

	// Fill the channel directly (white-box access to q.ch).
	q.ch <- evtx.WindowsEvent{EventID: 4663}
	q.ch <- evtx.WindowsEvent{EventID: 4663}

	ok := q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	if ok {
		t.Error("expected Enqueue to return false on full queue")
	}

	dropped := metrics.M.EventsDroppedTotal.Load()
	if dropped != 1 {
		t.Errorf("expected EventsDroppedTotal == 1, got %d", dropped)
	}

	// Drain the channel manually (do not call q.Stop() — no workers started).
	for len(q.ch) > 0 {
		<-q.ch
	}
}

func TestDrainOnStop(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)

	fw := &fakeWriter{done: make(chan struct{}, 3)}
	q := New(10, 2, fw)
	q.Start(context.Background())

	q.Enqueue(evtx.WindowsEvent{EventID: 4663, CEPAEventType: "CEPP_FILE_WRITE"})
	q.Enqueue(evtx.WindowsEvent{EventID: 4660, CEPAEventType: "CEPP_FILE_WRITE"})
	q.Enqueue(evtx.WindowsEvent{EventID: 4670, CEPAEventType: "CEPP_FILE_WRITE"})

	// Stop must block until all 3 events are processed.
	q.Stop()

	fw.mu.Lock()
	defer fw.mu.Unlock()

	if len(fw.events) != 3 {
		t.Errorf("expected 3 events written after Stop(), got %d", len(fw.events))
	}
}

// TestEnqueueAfterStop pins the guard that keeps a shutdown from panicking.
// main.go calls q.Stop() even when httpServer.Shutdown returned an error,
// which is exactly the case where a handler is still live and about to
// Enqueue. Without the guard this test panics with "send on closed channel"
// rather than failing — that is the intended mutation signal.
func TestEnqueueAfterStop(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)

	fw := &fakeWriter{}
	q := New(10, 1, fw)
	q.Start(context.Background())
	q.Stop()

	if ok := q.Enqueue(evtx.WindowsEvent{EventID: 4663}); ok {
		t.Fatal("Enqueue after Stop returned true; it must refuse and report false")
	}
	if got := metrics.M.EventsDroppedTotal.Load(); got != 1 {
		t.Errorf("expected the refused event counted as dropped, got EventsDroppedTotal == %d", got)
	}
}

// TestStopIsIdempotent covers the same shutdown path: TestEnqueue defers
// q.Stop() while other call sites invoke it directly, and a second close of
// the channel is a panic.
func TestStopIsIdempotent(t *testing.T) {
	fw := &fakeWriter{}
	q := New(10, 1, fw)
	q.Start(context.Background())
	q.Stop()
	q.Stop()
}
