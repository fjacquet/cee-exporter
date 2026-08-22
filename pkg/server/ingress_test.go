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
// concurrency slot — without a sleep: reading the body is the first thing
// ServeHTTP does after acquiring the slot.
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

		req := httptest.NewRequest(http.MethodPut, "/", body)
		go func() { h.ServeHTTP(httptest.NewRecorder(), req) }()

		// The slot is taken once ServeHTTP has reached the body read.
		select {
		case <-body.reading:
		case <-time.After(5 * time.Second):
			t.Fatal("ServeHTTP never reached the body read; the slot was never taken")
		}
	}

	// One more request. It must not get through.
	admitted := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`<RegisterRequest/>`))
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
// asserted: a hardcoded 1<<40 accepts the oversized body and answers 200, and
// a hardcoded 8<<20 accepts it too, because the cap under test is 1 MiB.
func TestServeHTTPRejectsOversizedBody(t *testing.T) {
	const capMB = 1

	cases := []struct {
		name     string
		bodyLen  int
		wantCode int
	}{
		{"one byte over the cap", (capMB << 20) + 1, http.StatusBadRequest},
		{"one byte under the cap", (capMB << 20) - 1, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPeers(t)
			h := newLimitedHandler(t, LimitsConfig{MaxBodyMB: capMB})

			req := httptest.NewRequest(http.MethodPut, "/",
				bytes.NewReader(bytes.Repeat([]byte("x"), tc.bodyLen)))
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("ServeHTTP on a %d-byte body under a %d MiB cap returned %d, want %d",
					tc.bodyLen, capMB, rec.Code, tc.wantCode)
			}
		})
	}
}
