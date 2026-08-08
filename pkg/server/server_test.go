package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
	"github.com/fjacquet/cee-exporter/pkg/queue"
)

// TestReadBodyOversized proves that readBody does not panic when a request body
// exceeds 64 MiB and that it returns a non-nil error in that case.
func TestReadBodyOversized(t *testing.T) {
	big := bytes.Repeat([]byte("x"), (64<<20)+1)
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(big))
	rec := httptest.NewRecorder()

	_, err := readBody(rec, req)
	if err == nil {
		t.Error("expected error for oversized body, got nil")
	}
}

// TestReadBodyNormal proves that normal payloads are read correctly.
func TestReadBodyNormal(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 1024)
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	got, err := readBody(rec, req)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(got) != 1024 {
		t.Errorf("body len = %d, want 1024", len(got))
	}
}

// ----------------------------------------------------------------------------
// CEPA protocol guarantee tests
//
// These cover ServeHTTP directly: the RegisterRequest handshake (CEPA-01),
// the deliberate ACK-on-parse-error behaviour, event enqueueing, and the
// ACK-before-queue-work ordering that the 3-second heartbeat budget depends
// on. See server.go:8-13 for the guarantees themselves.
// ----------------------------------------------------------------------------

// stubWriter is a minimal evtx.Writer for these tests. It records every
// event it receives and, when its channel fields are set, lets a test
// observe queue work happening concurrently without polling or sleeping:
//
//   - started fires (non-blocking) the instant WriteEvent is entered.
//   - block, if non-nil, is read from before the event is recorded — this
//     lets a test hold a worker goroutine mid-write on demand.
//   - done fires (non-blocking) after the event is recorded.
type stubWriter struct {
	mu     sync.Mutex
	events []evtx.WindowsEvent

	started chan struct{}
	block   <-chan struct{}
	done    chan struct{}
}

func (w *stubWriter) WriteEvent(_ context.Context, e evtx.WindowsEvent) error {
	if w.started != nil {
		select {
		case w.started <- struct{}{}:
		default:
		}
	}
	if w.block != nil {
		<-w.block
	}
	w.mu.Lock()
	w.events = append(w.events, e)
	w.mu.Unlock()
	if w.done != nil {
		select {
		case w.done <- struct{}{}:
		default:
		}
	}
	return nil
}

func (w *stubWriter) Close() error { return nil }

func (w *stubWriter) Events() []evtx.WindowsEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]evtx.WindowsEvent, len(w.events))
	copy(out, w.events)
	return out
}

// headerRecorder wraps httptest.ResponseRecorder to capture the fact that
// WriteHeader was called, so a test can prove ordering relative to other
// events (e.g. queue work) using a channel instead of a wall-clock check.
// The embedded *ResponseRecorder still supplies Flush, so ServeHTTP's
// `w.(http.Flusher)` type assertion continues to succeed.
type headerRecorder struct {
	*httptest.ResponseRecorder
	headerWritten chan struct{}
}

func newHeaderRecorder() *headerRecorder {
	return &headerRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		headerWritten:    make(chan struct{}),
	}
}

func (r *headerRecorder) WriteHeader(code int) {
	r.ResponseRecorder.WriteHeader(code)
	close(r.headerWritten)
}

// singleEventXML is one well-formed CEEEvent payload, valid input for
// parser.Parse.
const singleEventXML = `<CEEEvent>` +
	`<EventType>CEPP_FILE_WRITE</EventType>` +
	`<Timestamp>2026-08-08T12:00:00Z</Timestamp>` +
	`<FilePath>/mnt/share/file.txt</FilePath>` +
	`<Username>alice</Username>` +
	`<Domain>CORP</Domain>` +
	`<UserSID>S-1-5-21-1</UserSID>` +
	`<ClientAddress>10.0.0.5</ClientAddress>` +
	`</CEEEvent>`

// batchXML builds a VCAPS-style <EventBatch> containing n CEEEvent elements.
func batchXML(n int) string {
	var b strings.Builder
	b.WriteString("<EventBatch>")
	for range n {
		b.WriteString(singleEventXML)
	}
	b.WriteString("</EventBatch>")
	return b.String()
}

// newTestHandler builds a Handler backed by a real queue.Queue and stops the
// queue (draining it, closing the writer) via t.Cleanup.
func newTestHandler(t *testing.T, w evtx.Writer, capacity, workers int) *Handler {
	t.Helper()
	q := queue.New(capacity, workers, w)
	q.Start(context.Background())
	t.Cleanup(q.Stop)
	return NewHandler(q, "test-host")
}

// TestServeHTTP_RegisterRequest_EmptyBody is the CEPA-01 guard: the
// RegisterRequest handshake must get HTTP 200 with a response body of
// exactly zero bytes. Any XML in the response — even a single stray
// newline — is a fatal parse error on the PowerStore side, so this asserts
// on byte length, not string equality.
func TestServeHTTP_RegisterRequest_EmptyBody(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bare RegisterRequest", `<RegisterRequest/>`},
		{"with XML declaration", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<RegisterRequest/>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t, &stubWriter{}, 1, 1)

			req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if n := rec.Body.Len(); n != 0 {
				t.Fatalf("response body = %d bytes, want exactly 0 (CEPA-01: any body is a fatal parse error on PowerStore)", n)
			}
		})
	}
}

// TestServeHTTP_RejectsNonPut confirms every non-PUT method gets 405.
func TestServeHTTP_RejectsNonPut(t *testing.T) {
	h := newTestHandler(t, &stubWriter{}, 1, 1)

	methods := []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodHead}
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/", nil)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
		})
	}
}

// TestServeHTTP_ParseErrorStillACKs confirms the deliberate behaviour
// documented at server.go:80 — "Still ACK so CEPA doesn't mark us
// unreachable." A malformed, non-RegisterRequest body must still get 200,
// not a 4xx/5xx. A future "fix" that turns this into a 400 would silently
// degrade the integration.
func TestServeHTTP_ParseErrorStillACKs(t *testing.T) {
	h := newTestHandler(t, &stubWriter{}, 1, 1)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader("<not-well-formed"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (parse errors must still ACK; see server.go:80)", rec.Code)
	}
}

// TestServeHTTP_ValidBatchEnqueues confirms a valid single-event payload is
// counted in metrics and reaches the queue's writer.
func TestServeHTTP_ValidBatchEnqueues(t *testing.T) {
	metrics.M.EventsReceivedTotal.Store(0)

	done := make(chan struct{}, 1)
	sw := &stubWriter{done: done}
	h := newTestHandler(t, sw, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(singleEventXML))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := metrics.M.EventsReceivedTotal.Load(); got != 1 {
		t.Errorf("EventsReceivedTotal = %d, want 1", got)
	}

	<-done // wait for the worker to consume the enqueued event

	events := sw.Events()
	if len(events) != 1 {
		t.Fatalf("writer received %d events, want 1", len(events))
	}
	if events[0].CEPAEventType != "CEPP_FILE_WRITE" {
		t.Errorf("CEPAEventType = %q, want CEPP_FILE_WRITE", events[0].CEPAEventType)
	}
}

// TestServeHTTP_ACKsBeforeQueueWork proves the mechanism behind the
// 3-second heartbeat budget: server.go writes the response header (:95)
// before the enqueue loop (:107) even starts, so a slow or full queue can
// never delay the ACK.
//
// This asserts the strict ordering, not the weaker fallback the brief
// allows: it is achievable deterministically because stubWriter's `started`
// signal cannot fire until the queue's worker goroutine dequeues the event,
// and (with `block` unclosed) WriteEvent cannot proceed past that point.
// So by the time <-started returns, we know (a) ServeHTTP already returned
// — meaning WriteHeader already ran — and (b) the write is provably still
// in flight, because `done` cannot have fired without `block` being
// released first. That is a happens-before relationship enforced by
// channel operations, not a timing race.
//
// The queue capacity (10) is sized so Enqueue itself — a non-blocking
// select — cannot block; the point under test is that queue *work* is still
// outstanding when ServeHTTP returns, not that acceptance into the channel
// is slow.
func TestServeHTTP_ACKsBeforeQueueWork(t *testing.T) {
	metrics.M.EventsReceivedTotal.Store(0)

	block := make(chan struct{})
	started := make(chan struct{}, 1)
	done := make(chan struct{}, 1)
	sw := &stubWriter{started: started, block: block, done: done}
	h := newTestHandler(t, sw, 10, 1)

	rec := newHeaderRecorder()
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(singleEventXML))

	h.ServeHTTP(rec, req)

	select {
	case <-rec.headerWritten:
	default:
		t.Fatal("WriteHeader was not called by the time ServeHTTP returned")
	}

	// Block until the worker goroutine actually dequeues and begins writing
	// the event — proving queue work exists and has started.
	<-started

	// The writer cannot have finished: `block` has not been released yet,
	// and WriteEvent cannot pass its <-w.block read without it.
	select {
	case <-done:
		t.Fatal("writer reported done before block was released — ordering broken")
	default:
	}
	if got := len(sw.Events()); got != 0 {
		t.Fatalf("writer recorded %d events before block was released, want 0", got)
	}

	close(block) // release the worker
	<-done       // wait for it to finish recording the event

	if got := len(sw.Events()); got != 1 {
		t.Fatalf("writer recorded %d events after release, want 1", got)
	}
}

// TestServeHTTP_LargeBatchACKsWellUnder3s is the VCAPS case: a single PUT
// may contain thousands of events, and ServeHTTP must still ACK immediately
// rather than waiting on the queue. The real CEPA heartbeat budget is ~3s;
// this asserts against a 1s margin deliberately, so the test fails while
// there is still headroom rather than at the moment the integration
// actually breaks.
func TestServeHTTP_LargeBatchACKsWellUnder3s(t *testing.T) {
	metrics.M.EventsReceivedTotal.Store(0)

	const eventCount = 2000
	h := newTestHandler(t, &stubWriter{}, eventCount, 8)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(batchXML(eventCount)))
	rec := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if elapsed >= time.Second {
		t.Fatalf("ServeHTTP took %v to ACK a %d-event VCAPS batch, want < 1s (CEPA budget is ~3s)", elapsed, eventCount)
	}
	if got := metrics.M.EventsReceivedTotal.Load(); got != eventCount {
		t.Errorf("EventsReceivedTotal = %d, want %d", got, eventCount)
	}
}
