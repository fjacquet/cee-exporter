package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
	"github.com/fjacquet/cee-exporter/pkg/queue"
)

// The tests in this file drive ingress bounding through ServeHTTP.
//
// TestSemaphoreBoundsConcurrency and TestBodyCapRejectsOversized next door
// exercise the primitives — acquireSlot on a hand-built Handler, readBody with
// a literal cap. They prove the primitives work; they do not prove ServeHTTP
// uses them. Three mutations used to survive the whole pkg/server suite:
//
//	a) delete h.acquireSlot(); defer h.releaseSlot() from ServeHTTP
//	b) slots: make(chan struct{}, 1000000) in NewHandler
//	c) readBody(w, r, 1<<40) in ServeHTTP
//
// Each is exactly the edit that silently removes the OOM guard this whole
// task exists to provide. Every handler below is therefore built with
// NewHandler from a LimitsConfig, never with a struct literal, so the
// config -> behaviour link is part of what is asserted.

// blockingBody is a request body whose first Read parks until release is
// closed. It is how a test pins one request inside ServeHTTP — holding its
// concurrency slot — without a sleep.
//
// It must be reached *after* the slot is taken, which means it has to sit
// behind more than handshakeProbeBytes of padding: ServeHTTP now reads a
// bounded prefix before acquiring anything, so a bare blockingBody parks in
// that probe read and holds no slot at all. Use slotHolder rather than
// wiring one up by hand.
type blockingBody struct {
	reading chan struct{} // closed on the first Read
	release chan struct{} // closed by the test to let the read finish
	closed  bool
}

func (b *blockingBody) Read(p []byte) (int, error) {
	if !b.closed {
		b.closed = true
		close(b.reading)
		<-b.release
	}
	return 0, io.EOF
}

func (b *blockingBody) Close() error { return nil }

// newLimitedHandler builds a real Handler over a real queue with the given
// limits, via NewHandler — so slots is sized from MaxConcurrentRequests and
// the body cap comes from MaxBodyMB.
func newLimitedHandler(t *testing.T, limits LimitsConfig) *Handler {
	t.Helper()
	q := queue.New(queue.Config{Capacity: 100, Workers: 1, DrainTimeout: 5 * time.Second}, &stubWriter{})
	q.Start(context.Background())
	t.Cleanup(q.Stop)
	return NewHandler(q, "test-host", RegistrationConfig{}, limits)
}

// TestServeHTTPBoundsConcurrentRequests fires MaxConcurrentRequests+1 requests
// at ServeHTTP and asserts the last one is held until a slot frees.
//
// This is the claim §Testing actually asks for — "the semaphore blocks the
// N+1th request" — as opposed to the N+1th acquireSlot() call, which is what
// TestSemaphoreBoundsConcurrency checks. Without an assertion at this level,
// removing the semaphore from ServeHTTP, or sizing slots at a million, leaves
// the suite green and the process one burst away from the 269 MiB-per-request
// live heap that motivated the limit.
func TestServeHTTPBoundsConcurrentRequests(t *testing.T) {
	const slots = 1

	resetPeers(t)
	orig := metrics.M.RequestsThrottledTotal.Load()
	metrics.M.RequestsThrottledTotal.Store(0)
	t.Cleanup(func() { metrics.M.RequestsThrottledTotal.Store(orig) })

	h := newLimitedHandler(t, LimitsConfig{MaxConcurrentRequests: slots})

	// Occupy every slot. Each holder parks inside readBody, so it is holding
	// the slot for real rather than by construction.
	holders := make([]*blockingBody, slots)
	for i := range holders {
		body := &blockingBody{reading: make(chan struct{}), release: make(chan struct{})}
		holders[i] = body

		req := httptest.NewRequest(http.MethodPut, "/", slotHolder(body))
		go func() { h.ServeHTTP(httptest.NewRecorder(), req) }()

		// The slot is taken once ServeHTTP has reached the body read.
		select {
		case <-body.reading:
		case <-time.After(5 * time.Second):
			t.Fatal("ServeHTTP never reached the body read; the slot was never taken")
		}
	}

	// One more request. It must not get through.
	//
	// It carries an event payload, deliberately. A handshake body would be
	// admitted immediately and correctly — liveness traffic is exempt from the
	// semaphore, see handshakeProbeBytes — so using one here would assert the
	// opposite of what this test is named for.
	admitted := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(eventPayload))
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(admitted)
	}()

	// A negative assertion needs a bound to conclude against; this is a
	// deadline, not synchronisation standing in for a signal.
	select {
	case <-admitted:
		t.Fatal("ServeHTTP admitted request N+1 while every concurrency slot was held")
	case <-time.After(200 * time.Millisecond):
	}

	// The wait must be counted: a silent one is indistinguishable from a
	// network fault at the publisher.
	if got := metrics.M.RequestsThrottledTotal.Load(); got != 1 {
		t.Errorf("RequestsThrottledTotal = %d, want 1", got)
	}

	// Free the slots; the held request must then complete.
	for _, b := range holders {
		close(b.release)
	}
	select {
	case <-admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP never admitted request N+1 after the slots were freed")
	}
}

// TestServeHTTPRejectsOversizedBody drives the body cap through ServeHTTP with
// a non-default MaxBodyMB, so both halves of the config -> behaviour link are
// asserted: a hardcoded 1<<40 or 8<<20 as the cap lets the oversized body
// through and drops nothing, because the cap under test is 1 MiB.
//
// Both sizes answer 200. The status code is not what separates them — an
// oversized batch is acknowledged rather than 400'd, or Dell CEE retries it
// forever (see answerUnreadableBody) — so the discriminator is the drop
// counter and the fact that the oversized body never reaches the queue.
func TestServeHTTPRejectsOversizedBody(t *testing.T) {
	const capMB = 1

	cases := []struct {
		name     string
		bodyLen  int
		wantDrop int64
	}{
		{"one byte over the cap", (capMB << 20) + 1, 1},
		{"one byte under the cap", (capMB << 20) - 1, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPeers(t)
			metrics.M.EventsDroppedTotal.Store(0)
			h := newLimitedHandler(t, LimitsConfig{MaxBodyMB: capMB})

			req := httptest.NewRequest(http.MethodPut, "/",
				bytes.NewReader(bytes.Repeat([]byte("x"), tc.bodyLen)))
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			// Every publisher-facing reply must be a well-formed response
			// document; a bare error body stalls CEE indefinitely.
			if rec.Code != http.StatusOK {
				t.Errorf("ServeHTTP on a %d-byte body returned %d, want %d — an unparseable reply makes CEE retry forever",
					tc.bodyLen, rec.Code, http.StatusOK)
			}
			if got := metrics.M.EventsDroppedTotal.Load(); got != tc.wantDrop {
				t.Errorf("EventsDroppedTotal = %d, want %d on a %d-byte body under a %d MiB cap",
					got, tc.wantDrop, tc.bodyLen, capMB)
			}
		})
	}
}

// eventPayload is a minimal but real CheckEventRequest. Tests that need a
// request the semaphore must gate use this rather than a handshake document.
const eventPayload = `<CheckEventRequest><EventList count="1">` +
	`<Event event="0x8" path="\\\\nas01\\CHECK$\\FS01\\p.txt" flag="0x2" ` +
	`server="10.26.1.224" share="/FS01" clientIP="10.26.1.222" serverIP="10.26.1.224" ` +
	`timeStamp="0x6a7f7c090008765f" protocol="1">` +
	`<EventExt inode="9450" userId="0" ownerId="0"/>` +
	`</Event></EventList></CheckEventRequest>`

// TestLivenessAnswersWhileEverySlotIsHeld is the CEPA deadline guard.
//
// acquireSlot blocks with no bound of its own, so before liveness traffic was
// exempted a heartbeat arriving during a burst waited behind every event
// request in flight. CEPA gives 3 seconds; past it the publisher marks this
// consumer OFFLINE and stops sending events, so the semaphore protecting
// memory would have cost the entire stream it was protecting.
//
// Each case must answer 200 with every slot occupied. Deleting the liveness
// exemption from ServeHTTP hangs all three until the deadline below fires.
func TestLivenessAnswersWhileEverySlotIsHeld(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"PowerStore heartbeat", `<HeartBeatRequest />`},
		{"register handshake", `<RegisterRequest/>`},
		// The action lives on <Args>, not the root, and 9 is the heartbeat.
		// This is the shape measured on the wire from a real cluster.
		{"OneFS heartbeat", `<CheckFileRequest><Args action="9" sourceIP="10.26.1.150" sourceID="2" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/></CheckFileRequest>`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPeers(t)
			h := newLimitedHandler(t, LimitsConfig{MaxConcurrentRequests: 1})

			// Occupy the only slot with an event request parked in its body read.
			held := &blockingBody{reading: make(chan struct{}), release: make(chan struct{})}
			go func() {
				h.ServeHTTP(httptest.NewRecorder(),
					httptest.NewRequest(http.MethodPut, "/", slotHolder(held)))
			}()
			select {
			case <-held.reading:
			case <-time.After(5 * time.Second):
				t.Fatal("the event request never took the slot")
			}
			defer close(held.release)

			answered := make(chan int, 1)
			go func() {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body)))
				answered <- rec.Code
			}()

			select {
			case code := <-answered:
				if code != http.StatusOK {
					t.Errorf("liveness request answered %d, want %d", code, http.StatusOK)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("liveness request did not answer within CEPA's 3-second budget while a slot was held")
			}
		})
	}
}

// TestOversizedLivenessStillTakesASlot pins the exemption's boundary. A body
// that does not end within handshakeProbeBytes is not classifiable without
// reading the rest of it, so it is treated as an event payload no matter what
// its root element turns out to be — otherwise the exemption would be a way to
// bypass the memory bound by prefixing a large body with a handshake tag.
func TestOversizedLivenessStillTakesASlot(t *testing.T) {
	resetPeers(t)
	h := newLimitedHandler(t, LimitsConfig{MaxConcurrentRequests: 1})

	held := &blockingBody{reading: make(chan struct{}), release: make(chan struct{})}
	go func() {
		h.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodPut, "/", slotHolder(held)))
	}()
	select {
	case <-held.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("the event request never took the slot")
	}
	defer close(held.release)

	// A RegisterRequest padded past the probe with a comment: still a
	// handshake by root element, but not knowable as one from the prefix.
	padded := `<RegisterRequest/><!--` + strings.Repeat("p", handshakeProbeBytes) + `-->`
	admitted := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/", strings.NewReader(padded)))
		close(admitted)
	}()

	select {
	case <-admitted:
		t.Fatal("a body larger than the probe bypassed the semaphore")
	case <-time.After(200 * time.Millisecond):
	}
}

// slotHolder wraps a blockingBody in enough padding to outrun the handshake
// probe, so the request is classified as an event payload, takes a concurrency
// slot, and only then parks in the body read that the test controls.
func slotHolder(b *blockingBody) io.Reader {
	return io.MultiReader(strings.NewReader(strings.Repeat("x", handshakeProbeBytes+1)), b)
}
