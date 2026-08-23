# Writer Batching and Ingress Bounding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift the writer throughput ceiling by batching wire writes, bound the unbounded per-request memory, and close three shutdown defects.

**Architecture:** `WriteBatch` becomes a mandatory method on the `evtx.Writer` interface, so a backend that forgets it is a compile error rather than a silent degradation. The queue worker loop accumulates events (500 or 200 ms, whichever first) and calls `WriteBatch` once per batch, turning K wire writes under one mutex into one. `pkg/server` gains a body cap and a concurrency semaphore.

**Tech Stack:** Go 1.26.6, stdlib only in the event path plus `github.com/fjacquet/go-evtx` and `github.com/prometheus/client_golang`. Tests are stdlib only — no testify.

**Spec:** `docs/superpowers/specs/2026-08-22-writer-batching-and-ingress-bounding-design.md`

## Global Constraints

- Go 1.26.6 (`go.mod`). `make ci` = `lint test build vuln` is the gate.
- **Never** name a file `*_linux.go`. Non-Windows files use `_notwindows.go` with `//go:build !windows`; Windows files use `_windows.go` with `//go:build windows`.
- Tests: white-box (same package as the code under test), stdlib only, table-driven with `t.Run` for multi-case, **no `time.Sleep` for synchronisation** — use channel signals or injected timers.
- `errorlint` runs with `comparison: true`: never compare errors with `==`/`!=`, including in tests. Use `errors.Is`.
- `nilerr`: returning `nil` on a path where `err != nil` is a build failure.
- `errcheck` applies to non-test code. `bytes.Buffer.Write` returns an error — this plan uses `append` on `[]byte` instead, everywhere, to avoid the exposure entirely.
- `make format` rewrites; `make lint` only reports. Run `make format` before committing.
- Every writer must be safe for concurrent use (`pkg/evtx/writer.go:69`).
- Benchmarks are **on demand only**: `make bench`, plus a `workflow_dispatch`-only CI job. Never on push or pull request, never in `make ci`.
- Throughput improvements ship as **Unverified** in `docs/PROMISES.md`.

---

## File Structure

**Modified:**
- `pkg/queue/queue.go` — `Config` struct, `Enqueue`/`Stop` guard, batching drain loop
- `pkg/queue/queue_test.go` — fake writer gains `WriteBatch`; new guard and batching tests
- `pkg/evtx/writer.go` — `WriteBatch` on the interface, `writeBatchSerially` helper
- `pkg/evtx/writer_gelf.go` — `sendRaw`/`retrySend` split, `WriteBatch`
- `pkg/evtx/writer_syslog.go` — same split, `WriteBatch`
- `pkg/evtx/writer_beats.go` — `WriteBatch` over the native window
- `pkg/evtx/writer_evtx_notwindows.go` — `WriteBatch` via `writeBatchSerially`
- `pkg/evtx/writer_windows.go` — same
- `pkg/evtx/writer_multi.go` — `WriteBatch` fan-out
- `pkg/evtx/writer_multi_test.go` — fake writer gains `WriteBatch`
- `pkg/server/server.go` — `LimitsConfig`, semaphore, `readBody` cap parameter
- `pkg/server/server_test.go` — stub writer gains `WriteBatch`; `readBody` call sites
- `pkg/metrics/metrics.go` — three new counters
- `pkg/prometheus/handler.go` — three new series
- `cmd/cee-exporter/main.go` — `[server]` config section, `queue.Config` wiring
- `config.toml`, `CLAUDE.md`, `docs/PROMISES.md`, `Makefile`, `.github/workflows/ci.yml`

**Created:**
- `pkg/evtx/writer_race_test.go` — reconnect-race guards for the three network writers
- `pkg/evtx/writer_bench_test.go` — permanent per-writer batch benchmarks
- `pkg/parser/parser_bench_test.go` — permanent parse benchmarks

---

## Phase 1 — Shutdown correctness

### Task 1: Guard `Enqueue` against `Stop`

**Files:**
- Modify: `pkg/queue/queue.go:22-28` (struct), `:50-66` (Enqueue), `:74-81` (Stop)
- Test: `pkg/queue/queue_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `Queue.Enqueue(e evtx.WindowsEvent) bool` returns `false` instead of panicking after `Stop`. `Queue.Stop()` becomes idempotent.

Background: `cmd/cee-exporter/main.go:386` logs `http_shutdown_error` when `httpServer.Shutdown` times out and then falls through to `q.Stop()` at line 390, which does `close(q.ch)`. A handler still in flight then sends on a closed channel and the process panics during shutdown.

- [ ] **Step 1: Write the failing test**

Add to `pkg/queue/queue_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/queue/ -run 'TestEnqueueAfterStop|TestStopIsIdempotent' -v`

Expected: FAIL — a panic, `send on closed channel` for the first and `close of closed channel` for the second. A panic here is the pass condition for "the test detects the bug"; it must not be mistaken for a broken test.

- [ ] **Step 3: Write the implementation**

In `pkg/queue/queue.go`, add to the `Queue` struct:

```go
	// mu guards closed. Enqueue takes RLock and Stop takes Lock, so a send on
	// q.ch cannot race with close(q.ch): the send holds a read lock that the
	// close must exclude. RLock is uncontended in the normal case and costs
	// ~20 ns against an 8.6 µs parse, so the fast path is unaffected.
	mu     sync.RWMutex
	closed bool
```

Replace `Enqueue`:

```go
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
```

Replace `Stop`:

```go
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

	q.wg.Wait()
	if err := q.writer.Close(); err != nil {
		slog.Error("writer_close_error", "error", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/queue/ -race -count=1 -v`

Expected: PASS, all tests including the existing `TestEnqueue`, `TestDropOnFull`, `TestDrainOnStop`.

- [ ] **Step 5: Verify the guard by mutation**

Temporarily delete the `if q.closed { ... }` block from `Enqueue`, re-run
`go test ./pkg/queue/ -run TestEnqueueAfterStop`, and confirm it panics.
Restore the block. A guard that cannot fail is not a guard — this repository's
own notes record several that were only discovered to be inert when run.

- [ ] **Step 6: Commit**

```bash
make format
git add pkg/queue/queue.go pkg/queue/queue_test.go
git commit -m "fix(queue): refuse Enqueue after Stop instead of panicking

main.go calls q.Stop() even when httpServer.Shutdown returned an error,
which is precisely when a handler is still live. Its next Enqueue was a
send on a closed channel. Stop is idempotent for the same reason."
```

---

### Task 2: Drain under a context the queue owns

**Files:**
- Modify: `pkg/queue/queue.go` (New signature, Start, Stop), `cmd/cee-exporter/main.go:268`
- Modify: `pkg/queue/queue_test.go` (all four `New` call sites)
- Test: `pkg/queue/queue_test.go`

**Interfaces:**
- Consumes: Task 1's `closed`/`mu` guard.
- Produces: `queue.Config{Capacity int, Workers int, DrainTimeout time.Duration}` and `queue.New(cfg Config, w evtx.Writer) *Queue`. Task 12 adds `MaxBatch` and `BatchTimeout` to the same struct.

Background: `queueCtx` derives from the caller's `ctx` (`main.go:269`). On the SIGTERM path `ctx` is still alive, but the `ctx.Done()` path — Windows SCM Stop, `main.go:377` — has already cancelled it before `q.Stop()` drains. Latent today because GELF, syslog and EVTX all ignore the context; it becomes a silent Windows-only drain failure the moment any writer honours it.

- [ ] **Step 1: Write the failing test**

Add to `pkg/queue/queue_test.go`:

```go
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

func (c *ctxWriter) Close() error { return nil }

// TestDrainContextSurvivesParentCancel pins the Windows SCM shutdown path:
// main.go cancels the parent context before Stop() drains, and the drain must
// still run under a live context. Without WithoutCancel the writer sees an
// already-cancelled context for every drained event.
func TestDrainContextSurvivesParentCancel(t *testing.T) {
	cw := &ctxWriter{}
	q := New(Config{Capacity: 10, Workers: 1, DrainTimeout: 5 * time.Second}, cw)

	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)

	q.Enqueue(evtx.WindowsEvent{EventID: 4663})

	// Cancel the parent first, exactly as the SCM Stop path does, then drain.
	cancel()
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/queue/ -run TestDrainContextSurvivesParentCancel -v`

Expected: FAIL to compile — `Config` does not exist yet. That is the first signal; after Step 3's struct lands but before `WithoutCancel`, it fails with "drain ran under a cancelled context".

- [ ] **Step 3: Write the implementation**

In `pkg/queue/queue.go`, add the config struct and change `New`:

```go
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
```

Add `drainTimeout time.Duration`, `writeCtx context.Context` and `writeCancel context.CancelFunc` to the `Queue` struct, and replace `Start`:

```go
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
```

Replace the tail of `Stop` (after the `close`) with the bounded wait:

```go
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
```

- [ ] **Step 4: Update every `New` call site**

`pkg/queue/queue_test.go` has six after Task 1: the four original
(`New(10, 1, fw)` ×2, `New(2, 1, fw)`, `New(10, 2, fw)`) plus the two Task 1
added in `TestEnqueueAfterStop` and `TestStopIsIdempotent`. Rewrite each as,
for example:

```go
	q := New(Config{Capacity: 10, Workers: 1, DrainTimeout: 5 * time.Second}, fw)
```

`cmd/cee-exporter/main.go:268` becomes:

```go
	q := queue.New(queue.Config{
		Capacity:     cfg.Queue.Capacity,
		Workers:      cfg.Queue.Workers,
		DrainTimeout: time.Duration(cfg.Queue.DrainTimeoutS) * time.Second,
	}, w)
```

Add to `QueueConfig` in `main.go:135`:

```go
	// DrainTimeoutS bounds how long shutdown waits for the queue to drain.
	// Default 30 (set in defaultConfig).
	DrainTimeoutS int `toml:"drain_timeout_s"`
```

and `DrainTimeoutS: 30,` to the `Queue:` block in `defaultConfig` (`main.go:171`).

- [ ] **Step 5: Run the tests**

Run: `go test ./pkg/queue/ ./cmd/... -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make format
git add pkg/queue/queue.go pkg/queue/queue_test.go cmd/cee-exporter/main.go
git commit -m "fix(queue): drain under a context the queue owns

The Windows SCM Stop path cancels the caller's context before Stop()
drains, so the shutdown flush inherited a dead context. Latent only
because no writer honours ctx today. Adds queue.Config and a bounded,
logged drain deadline."
```

---

## Phase 2 — Race guards

### Task 3: Reconnect-race tests for the three network writers

**Files:**
- Create: `pkg/evtx/writer_race_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing consumed by later tasks. This task exists to make Phase 4's edits to the same mutexes falsifiable.

Background from the spec: `net.Conn` is documented safe for concurrent use, so a test that fires N goroutines at `WriteEvent` and passes under `-race` **proves nothing** — delete the mutex and it still passes. The genuinely unsynchronised state is the `w.conn` field, assigned by `connect()` and read by `send()` on the reconnect path. The test must force reconnects while other goroutines write.

- [ ] **Step 1: Write the test**

Create `pkg/evtx/writer_race_test.go`:

```go
package evtx

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// raceEvent is the payload used by every writer race test.
func raceEvent() WindowsEvent {
	return WindowsEvent{
		EventID:       4663,
		ProviderName:  DefaultProviderName,
		Computer:      "NAS01",
		Channel:       DefaultChannel,
		TimeCreated:   time.Now().UTC(),
		ObjectType:    "File",
		ObjectName:    `\\NAS01\fs01\dir7\race.txt`,
		AccessMask:    "0x100106",
		CEPAEventType: "CEPP_FILE_WRITE",
	}
}

// flakySink accepts TCP connections and closes each one after a few reads,
// which forces the writer through connect() while other goroutines are inside
// send(). That interleaving — a write to w.conn racing a read of w.conn — is
// the only thing the mutex actually protects. A test that merely writes
// concurrently passes with the mutex deleted, because net.Conn is itself safe
// for concurrent use.
func flakySink(t *testing.T, readsBeforeClose int) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				buf := make([]byte, 4096)
				for i := 0; i < readsBeforeClose; i++ {
					if _, err := c.Read(buf); err != nil {
						break
					}
					select {
					case <-done:
						break
					default:
					}
				}
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().String(), func() {
		close(done)
		_ = ln.Close()
		wg.Wait()
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

// hammer runs write concurrently from many goroutines. Errors are tolerated —
// the sink is deliberately breaking connections — because the assertion under
// test is the race detector, not delivery.
func hammer(t *testing.T, write func() error) {
	t.Helper()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = write()
			}
		}()
	}
	wg.Wait()
}

func TestGELFWriterReconnectRace(t *testing.T) {
	addr, stop := flakySink(t, 3)
	defer stop()
	host, port := splitHostPort(t, addr)

	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	hammer(t, func() error { return w.WriteEvent(context.Background(), raceEvent()) })
}

func TestSyslogWriterReconnectRace(t *testing.T) {
	addr, stop := flakySink(t, 3)
	defer stop()
	host, port := splitHostPort(t, addr)

	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	hammer(t, func() error { return w.WriteEvent(context.Background(), raceEvent()) })
}

func TestBeatsWriterReconnectRace(t *testing.T) {
	// The Beats client speaks Lumberjack and expects an ACK, so a bare
	// byte-sink is enough to make Send fail and drive the reconnect path,
	// which is the path under test.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()
	host, port := splitHostPort(t, ln.Addr().String())

	w, err := NewBeatsWriter(BeatsConfig{Host: host, Port: port})
	if err != nil {
		t.Skipf("beats writer could not dial the stub sink: %v", err)
	}
	defer func() { _ = w.Close() }()

	hammer(t, func() error { return w.WriteEvent(context.Background(), raceEvent()) })
}
```

- [ ] **Step 2: Run the tests and confirm they pass**

Run: `go test ./pkg/evtx/ -run 'ReconnectRace' -race -count=1 -v`

Expected: PASS, no race reports. If `NewSyslogWriter` or `NewBeatsWriter` take a differently-named config struct, fix the call to match — read `pkg/evtx/writer_syslog.go` and `pkg/evtx/writer_beats.go` for the exact type names rather than guessing.

- [ ] **Step 3: Validate each test by mutation — this step is the deliverable**

For each of the three writers in turn: comment out the `w.mu.Lock()` and
`defer w.mu.Unlock()` lines in that writer's `WriteEvent`, re-run
`go test ./pkg/evtx/ -run <that test> -race -count=1`, and confirm the race
detector reports a data race on the `conn` field. Restore the lines.

If a mutation does **not** produce a race report, the test is not a guard:
increase the goroutine count or lower `readsBeforeClose` until it does, then
re-check. Do not proceed with a test that passes both with and without the
mutex — that is the exact failure mode this task exists to prevent.

- [ ] **Step 4: Commit**

```bash
make format
git add pkg/evtx/writer_race_test.go
git commit -m "test(evtx): reconnect-race guards for gelf, syslog and beats

net.Conn is safe for concurrent use, so writing concurrently passes with
the mutex deleted and guards nothing. These drive reconnects while other
goroutines write, which races connect()'s assignment of w.conn against
send()'s read of it. Each validated by removing the mutex."
```

---

## Phase 3 — Ingress bounding

### Task 4: Body cap and concurrency semaphore

**Files:**
- Modify: `pkg/server/server.go:144-159` (Handler), `:184-192` (NewHandler), `:217+` (ServeHTTP), `:506-510` (readBody)
- Modify: `pkg/server/server_test.go:28,40` (readBody call sites)
- Modify: `pkg/metrics/metrics.go` (new counter), `pkg/prometheus/handler.go` (new series)
- Modify: `cmd/cee-exporter/main.go` (config wiring)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `server.LimitsConfig{MaxBodyMB int, MaxConcurrentRequests int}` with toml tags; `server.NewHandler(q *queue.Queue, hostname string, reg RegistrationConfig, limits LimitsConfig) *Handler`; `metrics.M.RequestsThrottledTotal atomic.Int64`.

Background: `readBody` caps at 64 MiB. Measured, a full-size body costs 269 MiB live heap and 495 MiB RSS, per request, with no connection cap anywhere. A realistic 1000-event batch is ~350 KB.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/server/server_test.go`:

```go
// TestBodyCapRejectsOversized pins the 8 MiB default. The old 64 MiB cap
// admitted ~190k events at ~4.2x body size in live heap — 269 MiB measured,
// per request, with no concurrency limit behind it.
func TestBodyCapRejectsOversized(t *testing.T) {
	limits := LimitsConfig{}.withDefaults()
	if got := limits.maxBodyBytes(); got != 8<<20 {
		t.Fatalf("default body cap = %d, want %d", got, 8<<20)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(make([]byte, (8<<20)+1)))
	if _, err := readBody(rec, req, limits.maxBodyBytes()); err == nil {
		t.Fatal("readBody accepted a body over the cap")
	}
}

// TestSemaphoreBoundsConcurrency proves the slot count is enforced and that
// waiting is counted. A silent wait surfaces only as unexplained publisher
// timeouts, which is indistinguishable from a network fault.
func TestSemaphoreBoundsConcurrency(t *testing.T) {
	metrics.M.RequestsThrottledTotal.Store(0)

	h := &Handler{limits: LimitsConfig{MaxConcurrentRequests: 1}.withDefaults()}
	h.slots = make(chan struct{}, 1)

	h.acquireSlot() // takes the only slot

	acquired := make(chan struct{})
	go func() {
		h.acquireSlot()
		close(acquired)
	}()

	// The second acquire must not complete while the slot is held.
	select {
	case <-acquired:
		t.Fatal("acquireSlot admitted a second request past the limit")
	case <-time.After(50 * time.Millisecond):
	}

	h.releaseSlot()
	<-acquired
	h.releaseSlot()

	if got := metrics.M.RequestsThrottledTotal.Load(); got != 1 {
		t.Errorf("RequestsThrottledTotal = %d, want 1", got)
	}
}

// TestHealthUnaffectedBySaturation pins that the semaphore covers the CEPA
// handler alone. Putting observability behind the thing being saturated is
// how a saturation event becomes invisible: /health is what an operator
// reaches for precisely when requests are piling up.
func TestHealthUnaffectedBySaturation(t *testing.T) {
	h := &Handler{limits: LimitsConfig{MaxConcurrentRequests: 1}.withDefaults()}
	h.slots = make(chan struct{}, 1)
	h.acquireSlot() // saturate; never released in this test
	defer h.releaseSlot()

	health := NewHealthHandler(HealthConfig{StartTime: time.Now()})

	rec := httptest.NewRecorder()
	health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("/health returned %d while the CEPA handler was saturated, want 200", rec.Code)
	}
}
```

`HealthConfig` has more fields than `StartTime` — read `pkg/server/health.go`
and populate whatever `NewHealthHandler` requires for a 200; the assertion is
only that saturation does not reach it.

Note: the 50 ms here is an assertion that something does **not** happen within a window, not a synchronisation sleep. That is the one form the no-`time.Sleep` rule permits, and it is why the negative case is written with `select`/`time.After` rather than `time.Sleep` followed by a check.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/server/ -run 'TestBodyCap|TestSemaphore' -v`

Expected: FAIL to compile — `LimitsConfig`, `h.limits`, `h.slots`, `acquireSlot`, `releaseSlot` and `metrics.M.RequestsThrottledTotal` do not exist.

- [ ] **Step 3: Add the metric**

In `pkg/metrics/metrics.go`, add to the `Store` struct next to the other scalar counters:

```go
	// RequestsThrottledTotal counts requests that had to wait for a
	// concurrency slot in pkg/server. Non-zero means max_concurrent_requests
	// is binding, which shows up at the publisher as a missed 3-second ACK
	// and nowhere else without this counter.
	RequestsThrottledTotal atomic.Int64
```

In `pkg/prometheus/handler.go`, add to the `newRegistry` `MustRegister` list, following the existing `NewCounterFunc` shape:

```go
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_requests_throttled_total",
				Help: "Total CEPA requests that had to wait for a concurrency " +
					"slot before their body was read. Non-zero means " +
					"max_concurrent_requests is binding: publishers are being " +
					"held past their 3-second ACK budget and will mark this " +
					"consumer unavailable. Raise the limit only after checking " +
					"that live heap has room — each slot is worth roughly " +
					"4x max_body_mb.",
			},
			func() float64 { return float64(metrics.M.RequestsThrottledTotal.Load()) },
		),
```

- [ ] **Step 4: Implement the limits in pkg/server**

In `pkg/server/server.go`, add the config type:

```go
// LimitsConfig bounds what one request may cost. Both fields exist because a
// single 64 MiB body measured 269 MiB of live heap and 495 MiB of RSS, and
// nothing capped how many could be in flight at once.
type LimitsConfig struct {
	// MaxBodyMB caps the request body. Default 8, which is ~23k events —
	// well past any documented VCAPS batch, where a realistic 1000-event
	// batch is ~350 KB.
	MaxBodyMB int `toml:"max_body_mb"`

	// MaxConcurrentRequests bounds request bodies in flight. Default 8, so
	// worst-case live heap is ~270 MiB rather than unbounded. Raising it
	// raises that ceiling roughly linearly.
	MaxConcurrentRequests int `toml:"max_concurrent_requests"`
}

func (c LimitsConfig) withDefaults() LimitsConfig {
	if c.MaxBodyMB <= 0 {
		c.MaxBodyMB = 8
	}
	if c.MaxConcurrentRequests <= 0 {
		c.MaxConcurrentRequests = 8
	}
	return c
}

func (c LimitsConfig) maxBodyBytes() int64 { return int64(c.MaxBodyMB) << 20 }
```

Add to the `Handler` struct:

```go
	limits LimitsConfig

	// slots bounds concurrent request bodies in flight. Acquired before
	// readBody and released at the end of ServeHTTP: the slot must cover
	// parse, not just the read, because parse is where the ~4.2x body-size
	// live heap actually lives.
	slots chan struct{}
```

Update `NewHandler`:

```go
func NewHandler(q *queue.Queue, hostname string, reg RegistrationConfig, limits LimitsConfig) *Handler {
	reg = reg.withDefaults()
	limits = limits.withDefaults()
	return &Handler{
		q:             q,
		hostname:      hostname,
		reg:           reg,
		registerReply: reg.registrationResponseXML(),
		limits:        limits,
		slots:         make(chan struct{}, limits.MaxConcurrentRequests),
	}
}
```

Add the slot helpers:

```go
// acquireSlot takes a concurrency slot, waiting if none is free.
//
// It blocks rather than rejecting. readBody runs before the ACK, so a
// rejection means no ACK at all and the publisher may retry forever or mark
// this consumer unavailable. A blocked publisher misses its 3-second ACK and
// degrades — bad, but it is one publisher and it retries. An OOM takes every
// publisher's stream down at once and loses the queue with it. The wait is
// bounded by the server's existing 10s ReadTimeout.
func (h *Handler) acquireSlot() {
	select {
	case h.slots <- struct{}{}:
		return
	default:
	}
	metrics.M.RequestsThrottledTotal.Add(1)
	h.slots <- struct{}{}
}

func (h *Handler) releaseSlot() { <-h.slots }
```

In `ServeHTTP`, after the `metrics.M.RecordPeerRequestAt(peer, start)` line and
before `defer func() { _ = r.Body.Close() }()`:

```go
	// After the peer stamp, before the body is read: the stamp must record a
	// publisher that is alive even when the request goes on to fail, and the
	// slot must cover everything that allocates.
	h.acquireSlot()
	defer h.releaseSlot()
```

Change `readBody` to take the cap:

```go
// readBody reads up to maxBody bytes from the request body. MaxBytesReader
// enforces the cap; any excess returns an error that the caller maps to
// HTTP 400.
func readBody(w http.ResponseWriter, r *http.Request, maxBody int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	return io.ReadAll(r.Body)
}
```

and its call site in `ServeHTTP`:

```go
	body, err := readBody(w, r, h.limits.maxBodyBytes())
```

- [ ] **Step 5: Update the existing test call sites**

`pkg/server/server_test.go:28` and `:40` call `readBody(rec, req)`. Both become
`readBody(rec, req, 8<<20)`. Read the surrounding test bodies first — one of
them (`TestReadBodyOversized`) asserts on the oversize path and may need its
fixture size adjusted to sit above 8 MiB rather than 64 MiB.

- [ ] **Step 6: Wire the config in main.go**

Add to the top-level `Config` struct, beside the existing `CEPA` field:

```go
	Server server.LimitsConfig `toml:"server"`
```

Add to `defaultConfig()`:

```go
		Server: server.LimitsConfig{
			MaxBodyMB:             8,
			MaxConcurrentRequests: 8,
		},
```

And update the handler construction (`main.go:275`):

```go
	mux.Handle("/", server.NewHandler(q, hostname, cfg.CEPA, cfg.Server))
```

- [ ] **Step 7: Run the tests**

Run: `go test ./pkg/server/ ./pkg/metrics/ ./pkg/prometheus/ ./cmd/... -race -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
make format
git add pkg/server/server.go pkg/server/server_test.go pkg/metrics/metrics.go \
        pkg/prometheus/handler.go cmd/cee-exporter/main.go
git commit -m "feat(server): cap request bodies at 8 MiB and bound concurrency

One 64 MiB body measured 269 MiB live heap and 495 MiB RSS, with no
connection cap behind it. 8 MiB is ~23k events against a documented VCAPS
batch of thousands. Saturation blocks rather than rejects: readBody runs
before the ACK, so a rejection loses the publisher entirely."
```

---

## Phase 4 — Batching

### Task 5: `WriteBatch` on the `Writer` interface

**Files:**
- Modify: `pkg/evtx/writer.go:66-79` (interface), add `writeBatchSerially`
- Modify: `pkg/evtx/writer_evtx_notwindows.go`, `pkg/evtx/writer_windows.go`, `pkg/evtx/writer_multi.go`
- Modify: `pkg/evtx/writer_multi_test.go:21`, `pkg/queue/queue_test.go:18`, `pkg/server/server_test.go:75` (fakes)

**Interfaces:**
- Consumes: nothing.
- Produces: `Writer.WriteBatch(ctx context.Context, events []WindowsEvent) error` on the interface; `writeBatchSerially(ctx context.Context, w Writer, events []WindowsEvent) error`. Tasks 6–8 implement `WriteBatch` per network writer; Task 10 calls it from the queue.

The method is mandatory on the interface, not an optional asserted one. With an optional interface, `MultiWriter` failing to implement it degrades silently to the per-event loop — the identical shape to the `Rotate` bug documented at `writer_multi.go:47`, which was silent for real users running `type = "multi"`. Mandatory makes that mistake a compile error.

- [ ] **Step 1: Write the failing test**

Add to `pkg/evtx/writer_multi_test.go`:

```go
// TestMultiWriterWriteBatch pins the fan-out. MultiWriter implementing
// WriteBatch is compile-enforced now, but "reaches every backend with every
// event" is not — and the Rotate precedent at writer_multi.go:47 is exactly
// a fan-out method that silently reached nothing.
func TestMultiWriterWriteBatch(t *testing.T) {
	a, b := &fakeWriter{}, &fakeWriter{}
	m := NewMultiWriter(a, b)

	batch := []WindowsEvent{{EventID: 4663}, {EventID: 4660}, {EventID: 4670}}
	if err := m.WriteBatch(context.Background(), batch); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	for name, f := range map[string]*fakeWriter{"a": a, "b": b} {
		if f.batched != 3 {
			t.Errorf("backend %s received %d events, want 3", name, f.batched)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/evtx/ -run TestMultiWriterWriteBatch -v`

Expected: FAIL to compile — `WriteBatch` is not defined on `MultiWriter` and `fakeWriter` has no `batched` field.

- [ ] **Step 3: Change the interface and add the helper**

In `pkg/evtx/writer.go`, replace the `Writer` interface:

```go
// Writer is the output backend interface.  All writers must be safe for
// concurrent use from multiple goroutines.
type Writer interface {
	// WriteEvent writes a single Windows event to the backend.
	// Implementations must be non-blocking from the caller's perspective
	// (i.e. they should not hold up the HTTP handler goroutine for more
	// than a few milliseconds).
	WriteEvent(ctx context.Context, e WindowsEvent) error

	// WriteBatch writes several events as one unit. For a backend with a
	// framed wire protocol this is one lock acquisition and one write for the
	// whole batch, which is the entire throughput win: measured, sixteen
	// workers against the per-event path bought 1.4x, because every write
	// serialised through one mutex.
	//
	// Mandatory rather than an optional interface the caller type-asserts.
	// An optional one lets MultiWriter silently fall back to the per-event
	// loop when it forgets to implement it, which is precisely how Rotate
	// silently did nothing for `type = "multi"` (see writer_multi.go). A
	// missing method must be a compile error.
	//
	// Backends whose underlying API takes one record at a time should
	// implement this as writeBatchSerially(ctx, w, events).
	WriteBatch(ctx context.Context, events []WindowsEvent) error

	// Close flushes any pending events and releases resources.
	Close() error
}

// writeBatchSerially writes each event in turn through w.WriteEvent. It is the
// WriteBatch implementation for backends with no batch API of their own: the
// Win32 ReportEvent call and go-evtx's WriteRecord both take one record.
// Their throughput is unchanged by batching and the spec says so — EVTX's
// ceiling is fsync inside go-evtx, which this repository cannot batch away.
//
// The first error stops the batch. The events after it are the caller's to
// re-send, and continuing would report a partial write as a whole-batch
// failure either way.
func writeBatchSerially(ctx context.Context, w Writer, events []WindowsEvent) error {
	for i := range events {
		if err := w.WriteEvent(ctx, events[i]); err != nil {
			return fmt.Errorf("batch event %d/%d: %w", i+1, len(events), err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Implement it on the loop backends**

`pkg/evtx/writer_evtx_notwindows.go`, after `WriteEvent`:

```go
// WriteBatch writes each record in turn. go-evtx's WriteRecord takes one
// record, so there is nothing to coalesce; the ceiling here is its per-chunk
// fsync, tracked as a separate issue upstream (ADR-014).
func (b *BinaryEvtxWriter) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	return writeBatchSerially(ctx, b, events)
}
```

`pkg/evtx/writer_windows.go`, after `WriteEvent`:

```go
// WriteBatch writes each event in turn. Win32 ReportEvent takes one event and
// offers no batch form, so this exists to satisfy the interface rather than to
// gain throughput.
func (w *Win32EventLogWriter) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	return writeBatchSerially(ctx, w, events)
}
```

`pkg/evtx/writer_multi.go`, after `WriteEvent`:

```go
// WriteBatch sends the whole batch to every backend.  All targets are called
// even if an earlier one errors.  All errors are joined.
//
// Fan-out, not a per-event loop: passing the batch through intact is what lets
// each backend take its lock once. Splitting it here would hand every backend
// K single-event writes and undo the change entirely.
func (m *MultiWriter) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	var errs []error
	for _, w := range m.writers {
		if err := w.WriteBatch(ctx, events); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 5: Update the three test fakes**

`pkg/evtx/writer_multi_test.go` — add a counter and the method to `fakeWriter`:

```go
func (f *fakeWriter) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	f.batched += len(events)
	return writeBatchSerially(ctx, f, events)
}
```

plus a `batched int` field on the struct.

`pkg/queue/queue_test.go` — add to `fakeWriter`:

```go
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
```

plus a `batches []int` field — Task 10's tests assert on it. Also add the same
method to `ctxWriter` from Task 2:

```go
func (c *ctxWriter) WriteBatch(ctx context.Context, events []evtx.WindowsEvent) error {
	for i := range events {
		if err := c.WriteEvent(ctx, events[i]); err != nil {
			return err
		}
	}
	return nil
}
```

`pkg/server/server_test.go` — add to `stubWriter`:

```go
func (w *stubWriter) WriteBatch(ctx context.Context, events []evtx.WindowsEvent) error {
	for i := range events {
		if err := w.WriteEvent(ctx, events[i]); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 6: Run the full test suite**

Run: `go test ./... -race -count=1`

Expected: PASS. Then `GOOS=windows go build ./...` to confirm the Windows-tagged
files still compile — CI's Linux jobs never see `writer_windows.go`, and this
task edits it.

- [ ] **Step 7: Commit**

```bash
make format
git add pkg/evtx/ pkg/queue/queue_test.go pkg/server/server_test.go
git commit -m "feat(evtx): add mandatory WriteBatch to the Writer interface

Mandatory rather than an optional asserted interface: an optional one lets
MultiWriter fall back to the per-event loop silently, which is how Rotate
did nothing at all for type = \"multi\". EVTX and Win32 use
writeBatchSerially — their APIs take one record and their throughput is
unchanged."
```

---

### Task 6: GELF `WriteBatch`

**Files:**
- Modify: `pkg/evtx/writer_gelf.go:107-145` (WriteEvent, send)
- Test: `pkg/evtx/writer_gelf_test.go`

**Interfaces:**
- Consumes: `Writer.WriteBatch` from Task 5.
- Produces: `(*GELFWriter).WriteBatch`; internal `sendRaw(b []byte) error` and `retrySend(send func() error) error`.

- [ ] **Step 1: Write the failing test**

Add to `pkg/evtx/writer_gelf_test.go`:

```go
// TestGELFWriteBatchTCPFraming reads the batch back off the wire. TCP GELF
// frames are null-terminated and concatenated into ONE write; a framing bug
// here is invisible locally and shows up as unparseable messages at Graylog.
func TestGELFWriteBatchTCPFraming(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type result struct {
		frames [][]byte
		writes int
	}
	results := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var all []byte
		writes := 0
		buf := make([]byte, 65536)
		for {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := c.Read(buf)
			if n > 0 {
				writes++
				all = append(all, buf[:n]...)
			}
			if err != nil {
				break
			}
			if bytes.Count(all, []byte{0x00}) >= 3 {
				break
			}
		}
		frames := bytes.Split(bytes.TrimRight(all, "\x00"), []byte{0x00})
		results <- result{frames: frames, writes: writes}
	}()

	host, port := splitHostPort(t, ln.Addr().String())
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := []WindowsEvent{
		{EventID: 4663, Computer: "NAS01", ObjectName: "/a", CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 4660, Computer: "NAS01", ObjectName: "/b", CEPAEventType: "CEPP_DELETE_FILE"},
		{EventID: 4670, Computer: "NAS01", ObjectName: "/c", CEPAEventType: "CEPP_SETACL_FILE"},
	}
	if err := w.WriteBatch(context.Background(), batch); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	got := <-results
	if len(got.frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(got.frames))
	}
	for i, f := range got.frames {
		var m map[string]any
		if err := json.Unmarshal(f, &m); err != nil {
			t.Fatalf("frame %d is not valid JSON: %v", i, err)
		}
		if m["version"] != "1.1" {
			t.Errorf("frame %d version = %v, want 1.1", i, m["version"])
		}
	}
}

// TestGELFWriteBatchUDPIsOneDatagramPerEvent is the assertion that stops
// someone "optimising" UDP into a concatenation later. A GELF datagram is one
// message; concatenating produces garbage at the collector, and the chunked
// format (0x1e 0x0f magic) is not implemented here.
func TestGELFWriteBatchUDPIsOneDatagramPerEvent(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()

	count := make(chan int, 1)
	go func() {
		buf := make([]byte, 65535)
		n := 0
		for n < 3 {
			_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := pc.ReadFrom(buf); err != nil {
				break
			}
			n++
		}
		count <- n
	}()

	host, port := splitHostPort(t, pc.LocalAddr().String())
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "udp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := []WindowsEvent{{EventID: 4663}, {EventID: 4660}, {EventID: 4670}}
	if err := w.WriteBatch(context.Background(), batch); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	if got := <-count; got != 3 {
		t.Errorf("received %d datagrams, want 3 — UDP must not concatenate", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/evtx/ -run TestGELFWriteBatch -v`

Expected: FAIL to compile — `(*GELFWriter).WriteBatch` is undefined.

- [ ] **Step 3: Refactor send and add WriteBatch**

In `pkg/evtx/writer_gelf.go`, replace `send` with two helpers and add the batch
method. `WriteEvent` keeps its behaviour, now expressed through them:

```go
// sendRaw writes b with a deadline and adds no framing. The caller has already
// framed the payload, which is what lets the batch path concatenate K frames
// into a single Write.
func (w *GELFWriter) sendRaw(b []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := w.conn.Write(b)
	return err
}

// retrySend runs send, reconnecting and retrying once on failure. Caller must
// hold w.mu.
func (w *GELFWriter) retrySend(send func() error) error {
	return sendWithRetry(send, func() error {
		slog.Warn("gelf_reconnect")
		return w.connect()
	})
}

// WriteEvent serialises the event as GELF JSON and sends it.
func (w *GELFWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	payload, err := buildGELF(e)
	if err != nil {
		return fmt.Errorf("gelf build: %w", err)
	}
	if w.cfg.Protocol == "tcp" {
		payload = append(payload, 0x00)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.retrySend(func() error { return w.sendRaw(payload) }); err != nil {
		return fmt.Errorf("gelf %w", err)
	}

	slog.Debug("gelf_event_sent",
		"event_id", e.EventID,
		"file_path", e.ObjectName,
		"cepa_event_type", e.CEPAEventType,
	)
	return nil
}

// WriteBatch sends every event in one lock acquisition.
//
// On TCP that is a single Write carrying K null-terminated frames, which is
// where the throughput comes from: the per-event path took the mutex K times
// and issued K writes, and measured, sixteen workers against it bought 1.4x.
//
// On UDP the events go as K separate datagrams, by protocol — a GELF datagram
// is one message. The win there is the lock, taken once instead of K times,
// which is roughly half the per-event cost since buildGELF already runs
// outside it.
func (w *GELFWriter) WriteBatch(_ context.Context, events []WindowsEvent) error {
	if len(events) == 0 {
		return nil
	}

	// Build before locking: this is the half of the cost that parallelises,
	// and holding the lock across it would give the batch back nothing.
	payloads := make([][]byte, 0, len(events))
	total := 0
	for i := range events {
		p, err := buildGELF(events[i])
		if err != nil {
			return fmt.Errorf("gelf build event %d/%d: %w", i+1, len(events), err)
		}
		payloads = append(payloads, p)
		total += len(p) + 1
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cfg.Protocol == "tcp" {
		// append on a preallocated slice rather than bytes.Buffer: Buffer.Write
		// returns an error that errcheck requires handling, for a write that
		// cannot fail.
		frame := make([]byte, 0, total)
		for _, p := range payloads {
			frame = append(frame, p...)
			frame = append(frame, 0x00)
		}
		if err := w.retrySend(func() error { return w.sendRaw(frame) }); err != nil {
			return fmt.Errorf("gelf batch %w", err)
		}
		slog.Debug("gelf_batch_sent", "events", len(events), "bytes", len(frame))
		return nil
	}

	for i, p := range payloads {
		if err := w.retrySend(func() error { return w.sendRaw(p) }); err != nil {
			return fmt.Errorf("gelf batch event %d/%d: %w", i+1, len(events), err)
		}
	}
	slog.Debug("gelf_batch_sent", "events", len(events), "datagrams", len(events))
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/evtx/ -race -count=1`

Expected: PASS, including `TestGELFWriterReconnectRace` from Task 3.

- [ ] **Step 5: Commit**

```bash
make format
git add pkg/evtx/writer_gelf.go pkg/evtx/writer_gelf_test.go
git commit -m "feat(evtx): batch GELF writes

TCP concatenates K null-terminated frames into one Write; UDP stays one
datagram per event because a GELF datagram is one message. Both take the
mutex once per batch instead of once per event."
```

---

### Task 7: Syslog `WriteBatch`

**Files:**
- Modify: `pkg/evtx/writer_syslog.go:74-115`
- Test: `pkg/evtx/writer_syslog_test.go`

**Interfaces:**
- Consumes: `Writer.WriteBatch` from Task 5.
- Produces: `(*SyslogWriter).WriteBatch`; internal `sendRaw`/`retrySend` mirroring the GELF split.

This is the largest single win in the plan and it was invisible before measurement: `writer_syslog.go:78-84` issues `fmt.Fprintf` for the RFC 6587 length prefix and **then** `Write` for the payload — two syscalls per event, both under the lock. A batch of 500 goes from 1000 writes to one.

- [ ] **Step 1: Write the failing test**

Add to `pkg/evtx/writer_syslog_test.go`:

```go
// TestSyslogWriteBatchOctetCounting reads the frames back out. One wrong
// length prefix in a concatenated batch desynchronises the receiver for every
// subsequent message, and the failure appears at the collector, not here.
func TestSyslogWriteBatchOctetCounting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	msgs := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		br := bufio.NewReader(c)

		var out []string
		for i := 0; i < 3; i++ {
			// RFC 6587 §3.4.1: "<decimal length> <message>"
			lenStr, err := br.ReadString(' ')
			if err != nil {
				break
			}
			n, err := strconv.Atoi(strings.TrimSpace(lenStr))
			if err != nil {
				break
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(br, payload); err != nil {
				break
			}
			out = append(out, string(payload))
		}
		msgs <- out
	}()

	host, port := splitHostPort(t, ln.Addr().String())
	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := []WindowsEvent{
		{EventID: 4663, Computer: "NAS01", ObjectName: "/a", CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 4660, Computer: "NAS01", ObjectName: "/b", CEPAEventType: "CEPP_DELETE_FILE"},
		{EventID: 4670, Computer: "NAS01", ObjectName: "/c", CEPAEventType: "CEPP_SETACL_FILE"},
	}
	if err := w.WriteBatch(context.Background(), batch); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	got := <-msgs
	if len(got) != 3 {
		t.Fatalf("recovered %d framed messages, want 3 — octet counting is wrong", len(got))
	}
	for i, m := range got {
		if !strings.HasPrefix(m, "<") {
			t.Errorf("message %d does not start with an RFC 5424 PRI: %q", i, m)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/evtx/ -run TestSyslogWriteBatch -v`

Expected: FAIL to compile — `(*SyslogWriter).WriteBatch` is undefined.

- [ ] **Step 3: Implement**

Replace `send` in `pkg/evtx/writer_syslog.go` with the same split as GELF, and
add `WriteBatch`:

```go
// sendRaw writes b with a deadline, adding no framing — the caller frames.
func (w *SyslogWriter) sendRaw(b []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := w.conn.Write(b)
	return err
}

// retrySend runs send, reconnecting and retrying once. Caller must hold w.mu.
func (w *SyslogWriter) retrySend(send func() error) error {
	return sendWithRetry(send, func() error {
		slog.Warn("syslog_reconnect")
		return w.connect()
	})
}

// frameTCP prepends the RFC 6587 §3.4.1 octet count to payload.
func frameTCP(dst, payload []byte) []byte {
	dst = strconv.AppendInt(dst, int64(len(payload)), 10)
	dst = append(dst, ' ')
	return append(dst, payload...)
}

// WriteEvent serialises the event as RFC 5424 syslog and sends it.
func (w *SyslogWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	payload, err := buildSyslog5424(e, w.cfg.AppName)
	if err != nil {
		return fmt.Errorf("syslog build: %w", err)
	}
	if w.cfg.Protocol == "tcp" {
		payload = frameTCP(make([]byte, 0, len(payload)+8), payload)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.retrySend(func() error { return w.sendRaw(payload) }); err != nil {
		return fmt.Errorf("syslog %w", err)
	}

	slog.Debug("syslog_event_sent", "event_id", e.EventID)
	return nil
}

// WriteBatch sends every event in one lock acquisition.
//
// On TCP that is one Write carrying K octet-counted frames. The per-event path
// issued TWO syscalls per event — fmt.Fprintf for the length prefix, then
// Write for the payload — so 500 events go from 1000 writes to one.
//
// UDP stays one datagram per message (RFC 5426); only the lock is shared.
func (w *SyslogWriter) WriteBatch(_ context.Context, events []WindowsEvent) error {
	if len(events) == 0 {
		return nil
	}

	payloads := make([][]byte, 0, len(events))
	total := 0
	for i := range events {
		p, err := buildSyslog5424(events[i], w.cfg.AppName)
		if err != nil {
			return fmt.Errorf("syslog build event %d/%d: %w", i+1, len(events), err)
		}
		payloads = append(payloads, p)
		total += len(p) + 8 // payload + decimal length + space
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cfg.Protocol == "tcp" {
		frame := make([]byte, 0, total)
		for _, p := range payloads {
			frame = frameTCP(frame, p)
		}
		if err := w.retrySend(func() error { return w.sendRaw(frame) }); err != nil {
			return fmt.Errorf("syslog batch %w", err)
		}
		slog.Debug("syslog_batch_sent", "events", len(events), "bytes", len(frame))
		return nil
	}

	for i, p := range payloads {
		if err := w.retrySend(func() error { return w.sendRaw(p) }); err != nil {
			return fmt.Errorf("syslog batch event %d/%d: %w", i+1, len(events), err)
		}
	}
	slog.Debug("syslog_batch_sent", "events", len(events), "datagrams", len(events))
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/evtx/ -race -count=1`

Expected: PASS. The existing syslog tests assert the single-event framing and
must still pass — `WriteEvent` now frames through `frameTCP` instead of
`fmt.Fprintf`, and the bytes on the wire must be identical.

- [ ] **Step 5: Commit**

```bash
make format
git add pkg/evtx/writer_syslog.go pkg/evtx/writer_syslog_test.go
git commit -m "feat(evtx): batch syslog writes

The per-event TCP path issued two syscalls per event: fmt.Fprintf for the
RFC 6587 length prefix, then Write for the payload, both under the lock.
A 500-event batch now goes from 1000 writes to one. UDP stays one
datagram per message per RFC 5426."
```

---

### Task 8: Beats `WriteBatch`

**Files:**
- Modify: `pkg/evtx/writer_beats.go:90-115`
- Test: `pkg/evtx/writer_beats_test.go`

**Interfaces:**
- Consumes: `Writer.WriteBatch` from Task 5.
- Produces: `(*BeatsWriter).WriteBatch`.

The Lumberjack client already takes a slice — `w.client.Send([]any{event})` at
`writer_beats.go:99` wraps a single event in a one-element slice. Batching here
is native and is the cheapest of the three.

- [ ] **Step 1: Write the failing test**

`BeatsWriter.client` is a concrete `*lumberv2.SyncClient` (`writer_beats.go:32`),
not an interface, so "one window of K" cannot be observed by stubbing the
client — it has to be read off the wire. A Lumberjack v2 window frame is
`'2' 'W' <uint32 big-endian count>`, and it is the first thing on the
connection, so the first six bytes settle the question without parsing the
payload frames that follow.

Add to `pkg/evtx/writer_beats_test.go`:

```go
// TestBeatsWriteBatchSendsOneWindow reads the Lumberjack window header off the
// wire. Lumberjack ACKs per window, so K windows of one event costs K
// round-trips — exactly the cost batching exists to remove. The window count
// in the header separates "one window of 3" from "3 windows of 1"; nothing
// observable inside the process does, because the client is a concrete type.
func TestBeatsWriteBatchSendsOneWindow(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	header := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		hdr := make([]byte, 6)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		header <- hdr
		// Deliberately no ACK: the client's Send will fail and WriteBatch will
		// return an error, which this test tolerates. Speaking enough
		// Lumberjack to ACK correctly would mean parsing the payload frames,
		// and the window header alone already answers the question.
		_, _ = io.Copy(io.Discard, c)
	}()

	addr := ln.Addr().(*net.TCPAddr)
	w, err := NewBeatsWriter(BeatsConfig{Host: addr.IP.String(), Port: addr.Port})
	if err != nil {
		t.Fatalf("NewBeatsWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	batch := []WindowsEvent{{EventID: 4663}, {EventID: 4660}, {EventID: 4670}}
	// Error tolerated: the sink never ACKs. The assertion is what went out.
	_ = w.WriteBatch(context.Background(), batch)

	select {
	case hdr := <-header:
		if hdr[0] != '2' || hdr[1] != 'W' {
			t.Fatalf("first frame = %q, want a version-2 window frame", hdr[:2])
		}
		if got := binary.BigEndian.Uint32(hdr[2:6]); got != 3 {
			t.Errorf("window size = %d, want 3 — the batch was split into separate windows", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Lumberjack window frame reached the sink")
	}
}
```

Imports needed: `encoding/binary`, `io`, `net`, `time`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/evtx/ -run TestBeatsWriteBatch -v`

Expected: FAIL to compile — `(*BeatsWriter).WriteBatch` is undefined.

- [ ] **Step 3: Implement**

Add to `pkg/evtx/writer_beats.go` after `WriteEvent`:

```go
// WriteBatch sends the whole batch as one Lumberjack window.
//
// The client's Send already takes a slice — the per-event path wrapped each
// event in a one-element slice — so this is the one backend where batching is
// native. Lumberjack ACKs per window, so K separate Sends cost K round-trips.
func (w *BeatsWriter) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	if len(events) == 0 {
		return nil
	}

	batch := make([]any, 0, len(events))
	for i := range events {
		batch = append(batch, buildBeatsEvent(events[i]))
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	send := func() error {
		_, err := w.client.Send(batch)
		return err
	}
	if err := sendWithRetry(send, func() error {
		slog.Warn("beats_reconnect")
		_ = w.client.Close()
		return w.dial(ctx)
	}); err != nil {
		return fmt.Errorf("beats batch %w", err)
	}

	slog.Debug("beats_batch_sent", "events", len(events))
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/evtx/ -race -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make format
git add pkg/evtx/writer_beats.go pkg/evtx/writer_beats_test.go
git commit -m "feat(evtx): batch beats writes as one Lumberjack window

client.Send already took a slice; the per-event path wrapped each event in
a one-element slice and paid a window ACK round-trip per event."
```

---

### Task 9: Batch metrics

**Files:**
- Modify: `pkg/metrics/metrics.go` (two counters), `pkg/prometheus/handler.go` (two series)
- Test: `pkg/prometheus/handler_test.go` if one exists, otherwise `pkg/metrics/metrics_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `metrics.M.WriterBatchesTotal atomic.Int64` and `metrics.M.WriterBatchErrorsTotal atomic.Int64`, consumed by Task 10.

These land before the worker loop because Task 10 writes to them.

- [ ] **Step 1: Add the counters**

In `pkg/metrics/metrics.go`, in the `Store` struct beside `WriterErrorsTotal`:

```go
	// WriterBatchesTotal counts WriteBatch calls, success or failure.
	// EventsWrittenTotal / WriterBatchesTotal is the mean batch size, which is
	// the only way to tell that batching is actually happening: a
	// batch_timeout_ms set too low, or a traffic shape delivering events one
	// at a time, degrades to batch=1 while every other observable stays green
	// and the throughput ceiling is exactly what it was before.
	WriterBatchesTotal atomic.Int64

	// WriterBatchErrorsTotal counts failed WriteBatch calls. WriterErrorsTotal
	// stays event-counted (it advances by len(batch)) so that anything built
	// on it keeps its meaning; this is the call-level companion.
	WriterBatchErrorsTotal atomic.Int64
```

- [ ] **Step 2: Register the series**

In `pkg/prometheus/handler.go`, in the `newRegistry` `MustRegister` list:

```go
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_writer_batches_total",
				Help: "Total WriteBatch calls issued to the output writer, " +
					"success or failure. Divide cee_events_written_total by " +
					"this for the mean batch size: a value near 1 means " +
					"batching is not happening and the writer is back at its " +
					"per-event throughput ceiling, which no other series " +
					"reveals.",
			},
			func() float64 { return float64(metrics.M.WriterBatchesTotal.Load()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_writer_batch_errors_total",
				Help: "Total failed WriteBatch calls. cee_writer_errors_total " +
					"counts the events in those batches and keeps its original " +
					"event-level meaning; this counts the calls.",
			},
			func() float64 { return float64(metrics.M.WriterBatchErrorsTotal.Load()) },
		),
```

- [ ] **Step 3: Verify the scrape**

Run: `go test ./pkg/prometheus/ ./pkg/metrics/ -race -count=1`

Expected: PASS. If `pkg/prometheus` has a test asserting the exact set of
exposed series names, add the two new names to it.

- [ ] **Step 4: Commit**

```bash
make format
git add pkg/metrics/metrics.go pkg/prometheus/handler.go
git commit -m "feat(metrics): add writer batch counters

events_written / batches is the mean batch size. Without it, batching
degrading to one event per call is invisible: every other series reads
identically."
```

---

### Task 10: Queue worker batching loop

**Files:**
- Modify: `pkg/queue/queue.go` (Config, Queue struct, work), `cmd/cee-exporter/main.go` (config)
- Test: `pkg/queue/queue_test.go`

**Interfaces:**
- Consumes: `Writer.WriteBatch` (Task 5), `metrics.M.WriterBatchesTotal` / `WriterBatchErrorsTotal` (Task 9), `queue.Config` (Task 2).
- Produces: `Config.MaxBatch int` and `Config.BatchTimeout time.Duration`; the queue calls `WriteBatch`, never `WriteEvent`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/queue/queue_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/queue/ -run 'TestBatch|TestPartialBatch' -v`

Expected: FAIL to compile — `Config.MaxBatch`, `Config.BatchTimeout`,
`q.newTimer` and `fakeWriter.batches` do not exist. (`fakeWriter.batches` was
added in Task 5.)

- [ ] **Step 3: Extend Config**

```go
	// MaxBatch is the largest number of events handed to one WriteBatch call.
	// 500 is the throughput choice; it is also the duplicate blast radius,
	// because a TCP write failing after partially landing replays the whole
	// batch.
	MaxBatch int
	// BatchTimeout bounds how long a partial batch waits for more events.
	// It is also the loss window: events held here are gone on SIGKILL.
	BatchTimeout time.Duration
```

and in `withDefaults`:

```go
	if c.MaxBatch <= 0 {
		c.MaxBatch = 500
	}
	if c.BatchTimeout <= 0 {
		c.BatchTimeout = 200 * time.Millisecond
	}
```

- [ ] **Step 4: Implement the drain loop**

Add to the `Queue` struct:

```go
	maxBatch     int
	batchTimeout time.Duration

	// newTimer returns the channel that fires after d, and a stop function.
	// Injected in tests so the batch-timeout path is driven deterministically:
	// this package's tests may not use time.Sleep for synchronisation.
	newTimer func(d time.Duration) (<-chan time.Time, func() bool)
```

Set them in `New` (`maxBatch: cfg.MaxBatch`, `batchTimeout: cfg.BatchTimeout`,
`newTimer: realTimer`) and add:

```go
func realTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}
```

Replace `work` and add the two helpers:

```go
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

	if err := q.writer.WriteBatch(ctx, batch); err != nil {
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
```

- [ ] **Step 5: Wire the config**

Add to `QueueConfig` in `cmd/cee-exporter/main.go`:

```go
	// MaxBatch is the largest number of events written in one call.
	// Default 500 (set in defaultConfig).
	MaxBatch int `toml:"max_batch"`
	// BatchTimeoutMS bounds how long a partial batch waits for more events.
	// Default 200 (set in defaultConfig). Also the crash loss window.
	BatchTimeoutMS int `toml:"batch_timeout_ms"`
```

`defaultConfig`: `MaxBatch: 500, BatchTimeoutMS: 200,`. And the construction:

```go
	q := queue.New(queue.Config{
		Capacity:     cfg.Queue.Capacity,
		Workers:      cfg.Queue.Workers,
		DrainTimeout: time.Duration(cfg.Queue.DrainTimeoutS) * time.Second,
		MaxBatch:     cfg.Queue.MaxBatch,
		BatchTimeout: time.Duration(cfg.Queue.BatchTimeoutMS) * time.Millisecond,
	}, w)
```

Add `"queue_max_batch", cfg.Queue.MaxBatch` to the startup log at `main.go:233`.

- [ ] **Step 6: Run the full suite**

Run: `go test ./... -race -count=1`

Expected: PASS, including the pre-existing `TestDrainOnStop`.

- [ ] **Step 7: Verify the shutdown-flush guard by mutation**

In `nextBatch`, temporarily change `return batch, false` (the channel-closed
branch inside the select) to `return nil, false`. Re-run
`go test ./pkg/queue/ -run TestPartialBatchFlushedOnStop` and confirm it fails.
Restore. This is the defect most likely to ship silently.

- [ ] **Step 8: Commit**

```bash
make format
git add pkg/queue/queue.go pkg/queue/queue_test.go cmd/cee-exporter/main.go
git commit -m "feat(queue): batch events into one WriteBatch per drain

Workers accumulate up to max_batch events or batch_timeout_ms, whichever
comes first, and issue one write. Event counters stay event-counted so
existing alerts keep their meaning; batches_total is the call-level
companion that makes batch=1 degradation visible."
```

---

### Task 11: Permanent benchmarks, on demand only

**Files:**
- Create: `pkg/evtx/writer_bench_test.go`, `pkg/parser/parser_bench_test.go`
- Modify: `Makefile`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `WriteBatch` on every writer.
- Produces: `make bench`.

Every number in the spec came from throwaway files that were deleted. Nothing
in the repository reproduces them, and the repository has no benchmarks at all.

- [ ] **Step 1: Write the writer benchmarks**

Create `pkg/evtx/writer_bench_test.go`:

```go
package evtx

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"

	goevtx "github.com/fjacquet/go-evtx"
)

// benchSinkTCP accepts connections and discards everything, never closing
// them — the opposite of flakySink, which exists to force reconnects.
func benchSinkTCP(b *testing.B) (host string, port int) {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func benchSinkUDP(b *testing.B) (host string, port int) {
	b.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	addr := pc.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), addr.Port
}

func benchBatch(size int) []WindowsEvent {
	batch := make([]WindowsEvent, size)
	for i := range batch {
		batch[i] = raceEvent()
	}
	return batch
}

// Read these as ns/op DIVIDED BY the batch size. The spec's table is
// per-event (GELF TCP measured 11.4 µs/event on the per-event path), and
// comparing a batch=500 ns/op against that figure directly will look like a
// 500x regression when it is a ~500x speedup per event.
func BenchmarkGELFWriteBatch(b *testing.B) {
	for _, proto := range []string{"tcp", "udp"} {
		for _, size := range []int{1, 10, 100, 500} {
			b.Run(fmt.Sprintf("%s/batch=%d", proto, size), func(b *testing.B) {
				var host string
				var port int
				if proto == "tcp" {
					host, port = benchSinkTCP(b)
				} else {
					host, port = benchSinkUDP(b)
				}
				w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: proto})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = w.Close() })

				batch := benchBatch(size)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if err := w.WriteBatch(context.Background(), batch); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkGELFWriteEvent is the baseline the batch numbers are measured
// against. Without it there is nothing to compare to and the improvement
// claim rests on a figure recorded in a document.
func BenchmarkGELFWriteEvent(b *testing.B) {
	host, port := benchSinkTCP(b)
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = w.Close() })

	e := raceEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := w.WriteEvent(context.Background(), e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSyslogWriteBatch(b *testing.B) {
	for _, size := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("tcp/batch=%d", size), func(b *testing.B) {
			host, port := benchSinkTCP(b)
			w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = w.Close() })

			batch := benchBatch(size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := w.WriteBatch(context.Background(), batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSyslogWriteEvent(b *testing.B) {
	host, port := benchSinkTCP(b)
	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = w.Close() })

	e := raceEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := w.WriteEvent(context.Background(), e); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEvtxWriteBatch is expected to show NO improvement over the
// per-event path: go-evtx's WriteRecord takes one record and fsyncs per
// chunk. It is here so that stays visible rather than assumed.
func BenchmarkEvtxWriteBatch(b *testing.B) {
	for _, size := range []int{1, 100} {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			w, err := NewBinaryEvtxWriter(
				filepath.Join(b.TempDir(), "bench.evtx"), goevtx.RotationConfig{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = w.Close() })

			batch := benchBatch(size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := w.WriteBatch(context.Background(), batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
```

`BenchmarkEvtxWriteBatch` lives in a `_test.go` file with no build tag, so it
compiles on Linux and macOS where `BinaryEvtxWriter` exists. If the package's
Windows build breaks on it, move it to `writer_bench_notwindows_test.go` with
`//go:build !windows` — **never** a `_linux.go` suffix.

- [ ] **Step 2: Write the parser benchmarks**

Create `pkg/parser/parser_bench_test.go`:

```go
package parser

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// benchBatch builds a <CheckEventRequest> of n events from the attribute list
// pinned in checkevent_test.go, which was recovered from
// CCheckEventRequest::GetXmlRequest() in the vendored CEE 9.2.0.0 rpm. Paths
// vary per event so the parser is not measured against one cached string.
func benchBatch(n int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<CheckEventRequest><EventList count="%d">`, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<Event event="0x00000008" path="\\NAS01\fs01\dir%d\file%d.txt" flag="0x0" `+
			`server="NAS01" share="fs01" clientIP="10.26.1.222" serverIP="10.26.1.224" `+
			`timeStamp="1786735002" userSid="S-1-5-21-1-2-3-1001" ownerSid="S-1-5-21-1-2-3-513" `+
			`fileSize="0x400" newName="" desiredAccess="0x100106" createDispo="0x3" `+
			`ntStatus="0x0" relativePath="\dir%d\file%d.txt"/>`, i%50, i, i%50, i)
	}
	b.WriteString(`</EventList></CheckEventRequest>`)
	return []byte(b.String())
}

// Divide ns/op by the event count for the per-event figure. Measured on an
// M1 Pro before this work: 8.6 µs/event UTF-8 and 10.9 µs/event UTF-16LE at
// 1000 events, 73 allocs/event in both.
func BenchmarkParseCheckEventRequest(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		body := benchBatch(n)
		b.Run(fmt.Sprintf("utf8/events=%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				_, decoded, err := Classify(body)
				if err != nil {
					b.Fatal(err)
				}
				ev, err := ParseCheckEventRequestDecoded(decoded, time.Now())
				if err != nil {
					b.Fatal(err)
				}
				if len(ev) != n {
					b.Fatalf("parsed %d events, want %d", len(ev), n)
				}
			}
		})
	}
}

// The UTF-16LE path allocates a second whole copy of the body during
// transcode — 14.6 KB/event against 4.0 measured — and CEE sends UTF-16LE to
// an unregistered partner, so it is not a rare path.
func BenchmarkParseCheckEventRequestUTF16(b *testing.B) {
	const n = 1000
	body := EncodeUTF16LE(benchBatch(n))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		_, decoded, err := Classify(body)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := ParseCheckEventRequestDecoded(decoded, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
```

- [ ] **Step 3: Add the Makefile target**

```makefile
bench:
	go test -run '^$$' -bench . -benchmem ./...
```

Add `bench` to `.PHONY`. Do **not** add it to `ci`.

- [ ] **Step 4: Add the on-demand CI job**

In `.github/workflows/ci.yml`, add a job whose **only** trigger is
`workflow_dispatch`:

```yaml
  bench:
    if: github.event_name == 'workflow_dispatch'
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      # Benchmarks are advisory and deliberately not a gate: shared CI runners
      # are too noisy for a threshold, and a flaky gate gets disabled, which
      # is worse than no gate.
      - run: make bench
```

Ensure `workflow_dispatch:` is present under the workflow's `on:` key. Verify
the `if:` guard actually holds — a job added without it runs on every push,
which is the one thing this task must not do.

- [ ] **Step 5: Run and record**

Run: `make bench 2>&1 | tee /tmp/bench-after.txt`

Compare the per-event GELF TCP and syslog TCP numbers against the spec's
per-event table. Record the actual figures in the commit message. If batching
did **not** improve them, stop and report rather than committing a claim the
numbers do not support.

- [ ] **Step 6: Commit**

```bash
make format
git add pkg/evtx/writer_bench_test.go pkg/parser/parser_bench_test.go Makefile .github/workflows/ci.yml
git commit -m "test(bench): permanent writer and parser benchmarks

On demand only: make bench, plus a workflow_dispatch-only CI job. Never on
push or PR and never in make ci — shared runners are too noisy for a gate,
and a flaky gate gets disabled.

Measured per-event, GELF TCP batch=500 vs WriteEvent: A us -> B us.
Syslog TCP: C us -> D us. EVTX unchanged, as expected."
```

Substitute A, B, C and D with the figures from Step 5 before committing. Do not
commit the letters, and do not commit figures you did not just measure — a
number in a commit message is a claim, and this repository has a documented
history of published claims that no run supported.

---

### Task 12: Documentation

**Files:**
- Modify: `config.toml`, `CLAUDE.md`, `docs/PROMISES.md`

**Interfaces:**
- Consumes: every config key added by Tasks 2, 4 and 10.
- Produces: nothing.

- [ ] **Step 1: Update config.toml**

Extend the `[queue]` block and add a `[server]` block:

```toml
[queue]
capacity = 100000  # max events buffered before dropping
workers  = 4       # concurrent writer goroutines

# max_batch is how many events go into one write. It is also the duplicate
# blast radius: a TCP write that fails after partially landing replays the
# whole batch, and delivery is at-least-once. Lower it if the collector
# handles duplicates badly.
max_batch = 500

# batch_timeout_ms bounds how long a partial batch waits for more events.
# It is also the crash loss window — events held here are gone on SIGKILL.
batch_timeout_ms = 200

# drain_timeout_s bounds how long shutdown waits for the queue to drain.
drain_timeout_s = 30

[server]
# max_body_mb caps the request body. 8 MiB is ~23k events; a realistic
# 1000-event batch is ~350 KB. The previous 64 MiB admitted ~190k events at
# ~4.2x body size in live heap — 269 MiB measured, per request.
max_body_mb = 8

# max_concurrent_requests bounds request bodies in flight, so worst-case live
# heap is roughly max_body_mb * 4.2 * this. Saturation makes requests WAIT,
# not fail: the body is read before the CEPA ACK, so rejecting loses the
# publisher entirely. Watch cee_requests_throttled_total.
max_concurrent_requests = 8
```

- [ ] **Step 2: Update CLAUDE.md**

In the Architecture list, change the `pkg/queue` line to:

```
- **`pkg/queue`** — buffered channel + worker goroutines. Workers accumulate
  up to `max_batch` events or `batch_timeout_ms` and issue one `WriteBatch`
  per drain; a partial batch is flushed when the channel closes. Drops events
  with WARN log when full, and refuses `Enqueue` after `Stop` rather than
  panicking; exposes depth via `/health`.
```

and add to the `pkg/evtx` bullet:

```
  - `Writer` requires `WriteBatch` as well as `WriteEvent`. It is mandatory
    rather than an optional asserted interface precisely so `MultiWriter`
    cannot silently fall back to the per-event loop the way `Rotate` once did.
```

- [ ] **Step 3: Update docs/PROMISES.md**

Read the file's existing Status vocabulary and row format first — `docs-lint.yml`
fails CI if a Status outside that vocabulary is used or a cited test is not a
real function. Add rows for:

- the batching throughput improvement — **Unverified**, because benchmarks are
  on demand and no job gates them
- `Enqueue` after `Stop` does not panic — verified by
  `TestEnqueueAfterStop` (`pkg/queue/queue_test.go`)
- partial batches flushed at shutdown — verified by
  `TestPartialBatchFlushedOnStop` (`pkg/queue/queue_test.go`)
- request bodies capped and concurrency bounded — verified by
  `TestBodyCapRejectsOversized` and `TestSemaphoreBoundsConcurrency`
  (`pkg/server/server_test.go`)

Every cited name must match a real function exactly.

- [ ] **Step 4: Verify the docs build and the docs lint**

Run: `make docs` and `go test ./... -count=1`

Expected: `mkdocs build --strict` passes — a single broken internal link fails
it. If `docs-lint.yml` can be run locally, run it; otherwise re-read PROMISES.md
against its own stated vocabulary before committing.

- [ ] **Step 5: Commit**

```bash
make format
git add config.toml CLAUDE.md docs/PROMISES.md
git commit -m "docs: record batching, ingress limits and their verification status

Throughput ships Unverified: benchmarks are on demand and nothing gates
them. The shutdown and ingress guarantees cite the tests that hold them."
```

---

## Phase 5 — Parser (conditional)

### Task 13: Measure, then decide

**Files:**
- None guaranteed. This task may correctly end with no code change.

**Interfaces:**
- Consumes: Task 11's benchmarks, Task 9's `cee_writer_batches_total`.
- Produces: either a decision to stop, or a scoped follow-up.

The spec defers this deliberately. Parse measured 8.6 µs/event against a GELF
write of 11.4 µs, so it was not the bottleneck; after batching it may be.

- [ ] **Step 1: Measure**

Run `make bench` and compare the per-event GELF TCP batch cost against
`BenchmarkParseCheckEventRequest`'s per-event cost.

- [ ] **Step 2: Profile if and only if parse now dominates**

```bash
go test ./pkg/parser/ -run '^$' -bench BenchmarkParseCheckEventRequest \
  -benchtime=2000x -cpuprofile=/tmp/parse.prof -o /tmp/parse.test
go tool pprof -top -nodecount=20 /tmp/parse.test /tmp/parse.prof
```

- [ ] **Step 3: Decide, and write the decision down**

If parse is **not** dominant, or `encoding/xml` is not on top of the profile:
**drop this item.** Record that in `docs/superpowers/specs/` as a one-paragraph
addendum to the design document and stop. Dropping is the expected outcome for
a deferred item that measurement does not justify, not a failure.

If it **is** dominant, open a new brainstorming pass for it rather than
extending this plan. Scope, fixed by the spec and not renegotiable here: a
hand-rolled attribute scanner for `ParseCheckEventRequestDecoded` **only**; the
OneFS path stays on `encoding/xml`; a differential test asserting identical
`[]CEPAEvent` against `encoding/xml` over a corpus; an explicit logged fallback
to `encoding/xml` on anything unrecognised. Hand-rolled XML on network input
has no safe silent-failure mode.

- [ ] **Step 4: Commit the decision**

```bash
git add docs/superpowers/specs/
git commit -m "docs(spec): record the parser-optimisation decision after measurement"
```

---

## Out of scope, tracked elsewhere

**go-evtx fsync cadence.** File an issue against
`github.com/fjacquet/go-evtx`: make fsync cadence configurable. `evtx.go:736`
fsyncs per chunk, which is the whole 12.5k eps EVTX ceiling and the ~60%
blocked time measured. Per ADR-014 format and durability behaviour belong
upstream. This plan does not wait on it, and EVTX and Win32 throughput are
explicitly unchanged by every task above.
