package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

type fakeWriter struct {
	mu      sync.Mutex
	events  []evtx.WindowsEvent
	batches []int
	done    chan struct{}
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

func (f *fakeWriter) WriteBatch(ctx context.Context, events []evtx.WindowsEvent) error {
	f.mu.Lock()
	f.batches = append(f.batches, len(events))
	f.mu.Unlock()
	for i := range events {
		if err := f.WriteEvent(ctx, events[i]); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeWriter) Close() error { return nil }

func TestEnqueue(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)

	fw := &fakeWriter{done: make(chan struct{}, 1)}
	q := New(Config{Capacity: 10, Workers: 1, DrainTimeout: 5 * time.Second}, fw)
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
	q := New(Config{Capacity: 2, Workers: 1, DrainTimeout: 5 * time.Second}, fw)
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
	q := New(Config{Capacity: 10, Workers: 2, DrainTimeout: 5 * time.Second}, fw)
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
	q := New(Config{Capacity: 10, Workers: 1, DrainTimeout: 5 * time.Second}, fw)
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
	q := New(Config{Capacity: 10, Workers: 1, DrainTimeout: 5 * time.Second}, fw)
	q.Start(context.Background())
	q.Stop()
	q.Stop()
}

// ctxWriter records whether the context it was handed had already been
// cancelled. It is the only way to observe the defect: no current writer
// honours ctx, so the bug is invisible until one does.
type ctxWriter struct {
	mu        sync.Mutex
	cancelled []bool
}

func (c *ctxWriter) WriteEvent(ctx context.Context, _ evtx.WindowsEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = append(c.cancelled, ctx.Err() != nil)
	return nil
}

func (c *ctxWriter) WriteBatch(ctx context.Context, events []evtx.WindowsEvent) error {
	for i := range events {
		if err := c.WriteEvent(ctx, events[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *ctxWriter) Close() error { return nil }

// TestDrainContextSurvivesParentCancel pins the Windows SCM shutdown path:
// main.go cancels the parent context before Stop() drains, and the drain must
// still run under a live context. Without WithoutCancel the writer sees an
// already-cancelled context for every drained event.
//
// Enqueue happens AFTER cancel() — this ordering is load-bearing, not
// incidental. With Enqueue before cancel(), the single worker can race ahead
// and consume the event before cancel() runs, in which case the buggy
// (WithoutCancel-less) code would also observe a live context and the test
// would pass regardless of whether the fix is present. Enqueueing only after
// the parent is already cancelled removes that interleaving: the worker
// cannot possibly see the event before cancellation, so a regression is
// guaranteed to be caught. Do not reorder this back to Start→Enqueue→cancel;
// that reintroduces the race and silently re-inerts the test.
func TestDrainContextSurvivesParentCancel(t *testing.T) {
	cw := &ctxWriter{}
	q := New(Config{Capacity: 10, Workers: 1, DrainTimeout: 5 * time.Second}, cw)

	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)

	// Cancel the parent first, exactly as the SCM Stop path does, then
	// enqueue and drain.
	cancel()
	q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	q.Stop()

	cw.mu.Lock()
	defer cw.mu.Unlock()
	if len(cw.cancelled) != 1 {
		t.Fatalf("expected 1 drained event, got %d", len(cw.cancelled))
	}
	if cw.cancelled[0] {
		t.Error("drain ran under a cancelled context; the shutdown flush must not inherit the caller's cancellation")
	}
}

// TestBatchAccumulatesUpToMaxBatch proves the events are coalesced rather than
// written one at a time. Without this the whole change is a no-op that still
// passes every other test.
func TestBatchAccumulatesUpToMaxBatch(t *testing.T) {
	fw := &fakeWriter{done: make(chan struct{}, 16)}
	fired := make(chan time.Time) // never fires: forces the size trigger
	q := New(Config{Capacity: 100, Workers: 1, MaxBatch: 5, BatchTimeout: time.Hour}, fw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.Start(context.Background())

	for i := 0; i < 5; i++ {
		q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	}
	q.Stop()

	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.batches) != 1 || fw.batches[0] != 5 {
		t.Errorf("batches = %v, want exactly one batch of 5", fw.batches)
	}
}

// TestBatchFlushesOnTimeout drives the timer directly. pkg/queue may not use
// time.Sleep for synchronisation, which is why newTimer is injectable.
func TestBatchFlushesOnTimeout(t *testing.T) {
	fw := &fakeWriter{done: make(chan struct{}, 4)}
	fired := make(chan time.Time, 1)
	q := New(Config{Capacity: 100, Workers: 1, MaxBatch: 500, BatchTimeout: time.Hour}, fw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.Start(context.Background())

	q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	fired <- time.Now() // the batch timeout expires with one event held
	<-fw.done           // the worker wrote it

	q.Stop()

	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.batches) == 0 || fw.batches[0] != 1 {
		t.Errorf("batches = %v, want a first batch of 1 flushed on timeout", fw.batches)
	}
}

// TestPartialBatchFlushedOnStop is the silent-loss guard. A worker that
// returns on channel close without writing its in-flight batch discards up to
// MaxBatch-1 events per worker on every shutdown — 4x499 with the defaults,
// with EventsWrittenTotal never counting them and nothing in the log.
func TestPartialBatchFlushedOnStop(t *testing.T) {
	fw := &fakeWriter{done: make(chan struct{}, 8)}
	fired := make(chan time.Time) // never fires
	q := New(Config{Capacity: 100, Workers: 1, MaxBatch: 500, BatchTimeout: time.Hour}, fw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.Start(context.Background())

	for i := 0; i < 3; i++ {
		q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	}
	q.Stop() // closes the channel with a partial batch in flight

	fw.mu.Lock()
	defer fw.mu.Unlock()
	total := 0
	for _, n := range fw.batches {
		total += n
	}
	if total != 3 {
		t.Errorf("wrote %d events across %v, want all 3 flushed at Stop", total, fw.batches)
	}
}

// TestBatchMetricsAreEventCounted pins the counter semantics. Counting per
// call instead of per event would drop the observed rate ~500x and silence
// every existing threshold alert built on these series.
func TestBatchMetricsAreEventCounted(t *testing.T) {
	metrics.M.EventsWrittenTotal.Store(0)
	metrics.M.WriterBatchesTotal.Store(0)

	fw := &fakeWriter{done: make(chan struct{}, 8)}
	fired := make(chan time.Time)
	q := New(Config{Capacity: 100, Workers: 1, MaxBatch: 4, BatchTimeout: time.Hour}, fw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.Start(context.Background())

	for i := 0; i < 4; i++ {
		q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	}
	q.Stop()

	if got := metrics.M.EventsWrittenTotal.Load(); got != 4 {
		t.Errorf("EventsWrittenTotal = %d, want 4 (events, not calls)", got)
	}
	if got := metrics.M.WriterBatchesTotal.Load(); got != 1 {
		t.Errorf("WriterBatchesTotal = %d, want 1 (calls, not events)", got)
	}
}

// errWrite is the injected writer failure. Compared with errors.Is, never ==:
// .golangci.yml runs errorlint with comparison: true.
var errWrite = errors.New("injected writer failure")

// failingWriter fails every WriteBatch.
type failingWriter struct {
	batches chan int // receives len(events) for each call
}

func (f *failingWriter) WriteEvent(context.Context, evtx.WindowsEvent) error { return errWrite }

func (f *failingWriter) WriteBatch(_ context.Context, events []evtx.WindowsEvent) error {
	if f.batches != nil {
		select {
		case f.batches <- len(events):
		default:
		}
	}
	return errWrite
}

func (f *failingWriter) Close() error { return nil }

// TestBatchErrorMetricsAreEventCounted is the failure-path half of
// TestBatchMetricsAreEventCounted. The spec singles WriterErrorsTotal out as
// the counter whose meaning must not change: counting per call rather than
// per event would drop the observed error rate ~500x and silently silence
// every threshold alert already built on cee_writer_errors_total.
// WriterBatchErrorsTotal is the opposite contract — calls, not events — and
// nothing exercised it at all.
func TestBatchErrorMetricsAreEventCounted(t *testing.T) {
	metrics.M.WriterErrorsTotal.Store(0)
	metrics.M.WriterBatchErrorsTotal.Store(0)
	metrics.M.EventsWrittenTotal.Store(0)

	fw := &failingWriter{batches: make(chan int, 4)}
	fired := make(chan time.Time) // never fires: the batch fills by size
	q := New(Config{Capacity: 100, Workers: 1, MaxBatch: 4, BatchTimeout: time.Hour}, fw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.Start(context.Background())

	for range 4 {
		q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	}
	if got := <-fw.batches; got != 4 {
		t.Fatalf("writer got a batch of %d, want 4", got)
	}
	q.Stop()

	if got := metrics.M.WriterErrorsTotal.Load(); got != 4 {
		t.Errorf("WriterErrorsTotal = %d, want 4 (events, not calls)", got)
	}
	if got := metrics.M.WriterBatchErrorsTotal.Load(); got != 1 {
		t.Errorf("WriterBatchErrorsTotal = %d, want 1 (calls, not events)", got)
	}
	if got := metrics.M.EventsWrittenTotal.Load(); got != 0 {
		t.Errorf("EventsWrittenTotal = %d, want 0 — a failed batch wrote nothing", got)
	}
}

// blockingWriter parks inside WriteBatch until release is closed, so a test
// can hold a worker's batch out of the channel and inside the writer for as
// long as it needs. entered reports that the park has actually happened, which
// is what replaces a sleep.
type blockingWriter struct {
	entered chan int
	release chan struct{}
}

func (b *blockingWriter) WriteEvent(context.Context, evtx.WindowsEvent) error { return nil }

func (b *blockingWriter) WriteBatch(_ context.Context, events []evtx.WindowsEvent) error {
	select {
	case b.entered <- len(events):
	default:
	}
	<-b.release
	return nil
}

func (b *blockingWriter) Close() error { return nil }

// TestDrainTimeoutCountsInFlightBatches pins the honest drain-timeout count.
//
// undrained used to be len(q.ch) alone, which cannot see events that have left
// the channel and are sitting inside a WriteBatch call — up to workers ×
// maxBatch of them, 4 × 500 = 2000 at the defaults. A shutdown that lost all
// 2000 logged events_undrained=0 and left EventsDroppedTotal and
// WriterErrorsTotal both at zero: a silent total loss reported as a clean
// shutdown, which is the exact failure the drain-timeout branch exists to
// report.
func TestDrainTimeoutCountsInFlightBatches(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)

	bw := &blockingWriter{entered: make(chan int, 1), release: make(chan struct{})}
	defer close(bw.release)

	fired := make(chan time.Time) // never fires: the batch fills by size
	q := New(Config{
		Capacity:     100,
		Workers:      1,
		MaxBatch:     3,
		BatchTimeout: time.Hour,
		// Short enough that Stop reaches the timeout branch promptly; the
		// writer is parked, so no amount of waiting would drain it.
		DrainTimeout: 20 * time.Millisecond,
	}, bw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.drainGrace = 20 * time.Millisecond
	q.Start(context.Background())

	for range 3 {
		q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	}

	// The worker is inside WriteBatch with all three events: the channel is
	// empty and len(q.ch) reports 0, which is precisely the blind spot.
	if got := <-bw.entered; got != 3 {
		t.Fatalf("writer got a batch of %d, want 3", got)
	}
	if got := q.Len(); got != 0 {
		t.Fatalf("q.Len() = %d, want 0 — the batch must be out of the channel", got)
	}

	q.Stop()

	if got := metrics.M.EventsDroppedTotal.Load(); got != 3 {
		t.Errorf("EventsDroppedTotal = %d, want 3 — the events held inside WriteBatch are lost and must be counted", got)
	}
}

// graceWriter parks inside WriteBatch until the write context is cancelled —
// which Stop does immediately before its grace wait — then hands control back
// to the test through an unbuffered handshake before returning. The handshake
// is what makes the grace observable: while the writer is parked on proceed
// the batch is provably still in flight, so a Stop that does not wait reads
// inFlight at exactly that moment.
type graceWriter struct {
	entered   chan int
	unblocked chan struct{}
	proceed   chan struct{}
}

func (g *graceWriter) WriteEvent(context.Context, evtx.WindowsEvent) error { return nil }

func (g *graceWriter) WriteBatch(ctx context.Context, events []evtx.WindowsEvent) error {
	select {
	case g.entered <- len(events):
	default:
	}
	<-ctx.Done()
	g.unblocked <- struct{}{}
	<-g.proceed
	return nil
}

func (g *graceWriter) Close() error { return nil }

// TestDrainTimeoutGraceLetsInFlightBatchLand is the other half of
// TestDrainTimeoutCountsInFlightBatches: an in-flight write that finishes
// inside the grace period must land, and must NOT be reported as dropped.
//
// Before the grace, Stop returned the instant the drain deadline passed and
// main.go logged cee_exporter_stopped underneath a worker that was still
// writing — so the batch was killed by process exit whether or not it was
// about to succeed.
func TestDrainTimeoutGraceLetsInFlightBatchLand(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(0)
	metrics.M.EventsWrittenTotal.Store(0)

	gw := &graceWriter{
		entered:   make(chan int, 1),
		unblocked: make(chan struct{}),
		proceed:   make(chan struct{}),
	}

	fired := make(chan time.Time) // never fires: the batch fills by size
	q := New(Config{
		Capacity:     100,
		Workers:      1,
		MaxBatch:     3,
		BatchTimeout: time.Hour,
		DrainTimeout: 20 * time.Millisecond,
	}, gw)
	q.newTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fired, func() bool { return true }
	}
	q.Start(context.Background())

	for range 3 {
		q.Enqueue(evtx.WindowsEvent{EventID: 4663})
	}
	if got := <-gw.entered; got != 3 {
		t.Fatalf("writer got a batch of %d, want 3", got)
	}

	stopped := make(chan struct{})
	go func() {
		q.Stop()
		close(stopped)
	}()

	// The writer only reaches here once Stop has passed its drain deadline and
	// called writeCancel, so the timeout branch is guaranteed to have been
	// taken. The batch is still in flight at this instant.
	<-gw.unblocked
	close(gw.proceed)

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned")
	}

	if got := metrics.M.EventsWrittenTotal.Load(); got != 3 {
		t.Errorf("EventsWrittenTotal = %d, want 3 — the batch landed inside the grace period", got)
	}
	if got := metrics.M.EventsDroppedTotal.Load(); got != 0 {
		t.Errorf("EventsDroppedTotal = %d, want 0 — a batch that landed must not be reported dropped", got)
	}
}
