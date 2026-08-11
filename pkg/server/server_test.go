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
	closeOnce     sync.Once
}

func newHeaderRecorder() *headerRecorder {
	return &headerRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		headerWritten:    make(chan struct{}),
	}
}

func (r *headerRecorder) WriteHeader(code int) {
	r.ResponseRecorder.WriteHeader(code)
	// sync.Once: a handler that called WriteHeader twice must not panic on a
	// double close — it should just leave headerWritten already closed.
	r.closeOnce.Do(func() { close(r.headerWritten) })
}

// waitOrFatal blocks on ch until it is ready or a generous deadline passes.
// It replaces a bare `<-ch`, which would otherwise hang until go test's
// default 10-minute timeout if the behaviour under test regressed — turning
// a single assertion failure into a package-wide hang that buries every
// other test's result. The deadline is a bound on how long to wait for a
// real signal, not a synchronisation primitive standing in for one, so it
// does not conflict with the repo's no-time.Sleep-for-synchronisation rule.
func waitOrFatal(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}

// resetEventsReceived zeroes metrics.M.EventsReceivedTotal for the duration
// of the test and restores its prior value afterwards, so tests that assert
// on it don't permanently clobber state for whatever runs next (relevant if
// -shuffle=on is ever adopted; today's sequential, non-parallel test order
// makes it a no-op).
func resetEventsReceived(t *testing.T) {
	t.Helper()
	orig := metrics.M.EventsReceivedTotal.Load()
	metrics.M.EventsReceivedTotal.Store(0)
	t.Cleanup(func() { metrics.M.EventsReceivedTotal.Store(orig) })
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
	resetEventsReceived(t)

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

	waitOrFatal(t, done, "writer never received the enqueued event")

	events := sw.Events()
	if len(events) != 1 {
		t.Fatalf("writer received %d events, want 1", len(events))
	}
	if events[0].CEPAEventType != "CEPP_FILE_WRITE" {
		t.Errorf("CEPAEventType = %q, want CEPP_FILE_WRITE", events[0].CEPAEventType)
	}
}

// TestServeHTTP_ACKsBeforeQueueWork proves the property that actually backs
// the 3-second heartbeat budget: ServeHTTP returns an ACK'd response while
// queue work is demonstrably still in flight, rather than waiting for the
// writer to finish.
//
// This is the brief's weaker fallback, not the strict "WriteHeader precedes
// the enqueue loop" ordering. That stronger claim is not vacuous — it can be
// observed deterministically (e.g. a zero-worker queue plus sampling q.Len()
// from inside WriteHeader would catch a reordering with no goroutines or
// timing involved) — but it is not observable by a test that, like this one,
// only inspects state after ServeHTTP returns: ServeHTTP runs synchronously
// and Enqueue cannot block, so by the time ServeHTTP returns, WriteHeader
// has necessarily already run wherever the statement sits in the function
// body. Such a test cannot tell "header written before the loop" apart from
// "header written after the loop, but the loop never blocked anyway".
// (Verified directly: moving the real WriteHeader call to after the entire
// enqueue loop still passes this test.) The stronger ordering was left
// unpursued deliberately, not for lack of a way to observe it: the parse
// step already has to complete before the header regardless and dominates
// the handler's cost (~233ms of ~245ms for a 2000-event batch under -race),
// so header-after-loop would only add the loop's own ~12ms (~5%) — a real
// but small latency risk, not the load-bearing part of the 3s budget.
//
// What *is* asserted below is that the ACK was delivered before the write it
// triggered was done — via a channel-enforced happens-before chain, not a
// timing race:
//   - stubWriter's `started` signal cannot fire until the queue's worker
//     goroutine actually dequeues the event and enters WriteEvent.
//   - With `block` unclosed, WriteEvent cannot proceed past that point, so
//     `done` cannot have fired and no event can have been recorded.
//
// So by the time <-started returns, ServeHTTP has already returned (header
// written) while the write is provably still outstanding.
//
// The queue capacity (10) is sized so Enqueue itself — a non-blocking
// select — cannot block; the point under test is that queue *work* is still
// outstanding when ServeHTTP returns, not that acceptance into the channel
// is slow.
//
// ServeHTTP itself runs in a goroutine, guarded by waitOrFatal on `returned`:
// this is what catches the async guarantee breaking outright (e.g. Enqueue
// made to write synchronously) — without it, that regression would hang
// ServeHTTP itself before the test ever regained control to fail. Every
// other blocking wait below has the same bounded fallback (waitOrFatal, or
// the unconditional block release via t.Cleanup), so a regression fails with
// a message in at most a few seconds instead of hanging until go test's
// default 10-minute timeout: the two select/default checks fail immediately,
// the waitOrFatal calls fail within their 5s deadline.
func TestServeHTTP_ACKsBeforeQueueWork(t *testing.T) {
	resetEventsReceived(t)

	block := make(chan struct{})
	var releaseOnce sync.Once
	releaseBlock := func() { releaseOnce.Do(func() { close(block) }) }

	started := make(chan struct{}, 1)
	done := make(chan struct{}, 1)
	sw := &stubWriter{started: started, block: block, done: done}
	h := newTestHandler(t, sw, 10, 1)
	// Registered after newTestHandler's t.Cleanup(q.Stop): t.Cleanup runs
	// LIFO, so this releases the blocked worker before Stop() waits on it —
	// otherwise any Fatal below would deadlock Stop() for the full 10-minute
	// test timeout instead of failing immediately.
	t.Cleanup(releaseBlock)

	rec := newHeaderRecorder()
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(singleEventXML))

	// Run ServeHTTP in a goroutine rather than calling it inline: if the
	// async guarantee under test broke entirely (e.g. Enqueue started
	// writing synchronously), ServeHTTP itself would never return, and a
	// direct call would hang the test goroutine before any assertion below
	// could run. waitOrFatal bounds that instead of leaving it to go test's
	// default 10-minute timeout. Once `returned` closes, ServeHTTP has
	// provably returned — the same precondition the header check below
	// relies on — so reading `rec` afterwards is race-free.
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h.ServeHTTP(rec, req)
	}()
	waitOrFatal(t, returned, "ServeHTTP did not return while queue work was outstanding — the ACK is blocked by the writer")

	select {
	case <-rec.headerWritten:
	default:
		t.Fatal("WriteHeader was not called by the time ServeHTTP returned")
	}

	// Wait for the worker goroutine to actually dequeue and begin writing
	// the event — proving queue work exists and has started.
	waitOrFatal(t, started, "worker never started processing the enqueued event")

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

	releaseBlock()
	waitOrFatal(t, done, "writer did not finish after block was released")

	if got := len(sw.Events()); got != 1 {
		t.Fatalf("writer recorded %d events after release, want 1", got)
	}
}

// TestServeHTTP_LargeBatchACKsWellUnder3s is the VCAPS case: a single PUT
// may contain thousands of events, and ServeHTTP must still ACK immediately
// rather than waiting on the queue. The real CEPA heartbeat budget is ~3s.
//
// Margin: measured read+parse latency for this 2000-event batch is ~233ms
// under -race locally (vs ~15ms without -race — the race detector costs
// roughly 15x here). A 1s bound would leave only ~4.3x margin on this dev
// machine, and GitHub-hosted CI runners commonly run 2-4x slower
// single-core, which could land near the wire under load. 2s keeps a full
// second of headroom below the real 3s limit — enough to fail while there
// is still runway, per the brief, without flaking on a slower runner.
func TestServeHTTP_LargeBatchACKsWellUnder3s(t *testing.T) {
	resetEventsReceived(t)

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
	if elapsed >= 2*time.Second {
		t.Fatalf("ServeHTTP took %v to ACK a %d-event VCAPS batch, want < 2s (CEPA budget is ~3s)", elapsed, eventCount)
	}
	if got := metrics.M.EventsReceivedTotal.Load(); got != eventCount {
		t.Errorf("EventsReceivedTotal = %d, want %d", got, eventCount)
	}
}

// resetPeers isolates the metrics.M peer table for a test. Unlike
// resetEventsReceived, which saves and restores the prior value, this always
// resets to empty both before and after the test: no other test in this
// package reads peer state, so there is no prior value worth preserving.
func resetPeers(t *testing.T) {
	t.Helper()
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)
}

func TestPeerHost(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{"ipv4 with port", "10.0.2.250:54321", "10.0.2.250"},
		{"ipv6 with port", "[2001:db8::1]:12228", "2001:db8::1"},
		{"no port", "10.0.2.250", "10.0.2.250"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerHost(tc.addr); got != tc.want {
				t.Errorf("peerHost(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// TestServeHTTP_StampsPeerOnEveryPath is the liveness guard. The metric must
// answer "is this publisher still talking to us", so every path that
// represents a publisher reaching us — including the failures — stamps it.
// A publisher whose payloads no longer parse is broken but alive, and must
// not read as dead.
func TestServeHTTP_StampsPeerOnEveryPath(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"register request", `<RegisterRequest/>`},
		{"event batch", singleEventXML},
		{"parse error", "<not-well-formed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPeers(t)
			h := newTestHandler(t, &stubWriter{}, 10, 1)

			req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body))
			req.RemoteAddr = "10.0.2.250:54321"
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			snap := metrics.M.PeerSnapshot()
			if _, ok := snap["10.0.2.250"]; !ok {
				t.Fatalf("peer 10.0.2.250 not stamped on the %q path; snapshot = %v", tc.name, snap)
			}
			if got := snap["10.0.2.250"].LastRequestUnix; got == 0 {
				t.Errorf("LastRequestUnix = 0 on the %q path, want a real timestamp", tc.name)
			}
		})
	}
}

// TestServeHTTP_StampsPeerWithoutPort confirms the ephemeral port is stripped.
// CEE's NumberOfThreads defaults to 20, so leaving the port on would create
// up to 20 label values per publisher and grow without bound over time.
func TestServeHTTP_StampsPeerWithoutPort(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	for _, port := range []string{":54321", ":54322", ":54323"} {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`<RegisterRequest/>`))
		req.RemoteAddr = "10.0.2.250" + port
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	snap := metrics.M.PeerSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PeerSnapshot has %d entries for one host on three ports, want 1: %v", len(snap), snap)
	}
	if _, ok := snap["10.0.2.250"]; !ok {
		t.Errorf("peer keyed as %v, want the bare host 10.0.2.250", snap)
	}
}

// TestServeHTTP_CountsRegistrationsOnly confirms registrations count the
// handshake and nothing else — an event batch is not a registration.
func TestServeHTTP_CountsRegistrationsOnly(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	send := func(body string) {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		req.RemoteAddr = "10.0.2.250:54321"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	send(`<RegisterRequest/>`)
	send(`<RegisterRequest/>`)
	send(singleEventXML)

	if got := metrics.M.PeerSnapshot()["10.0.2.250"].Registrations; got != 2 {
		t.Errorf("Registrations = %d, want 2 (two handshakes; the event batch is not one)", got)
	}
}
