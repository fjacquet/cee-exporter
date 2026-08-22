# Writer batching and ingress bounding

Status: approved 2026-08-22.

Covers five items surfaced by a measurement spike on 2026-08-22 (Apple M1 Pro,
loopback sinks, throwaway benchmarks since deleted). Ordered by the sequencing
in the last section, not by the numbering, which follows the spike report.

## Problem

The pipeline is not I/O bound. It is mutex bound, and one request can allocate
a quarter of a gigabyte.

### Measured

```
parse (UTF-8, 1000-event batch)   8.6 µs/event   73 allocs   4.0 KB/event
parse (UTF-16LE)                 10.9 µs/event   73 allocs  14.6 KB/event
buildGELF alone                   5.1 µs         63 allocs   2.9 KB

GELF UDP   1 worker 11.4 µs → 16 workers  7.9 µs   (~127k eps ceiling)
GELF TCP   1 worker 12.0 µs → 16 workers  7.6 µs
EVTX       1 worker 80.9 µs → 16 workers 82.2 µs   (~12.5k eps, no scaling)
```

Sixteen times the workers buys 1.4× on GELF and nothing on EVTX. The
`workers = 4` in `config.toml` is close to decorative on the write side: only
the payload build runs outside the lock, and every wire write serialises
through one mutex. Throughput ceiling is therefore `1 / lock-hold-time`, and
lock-hold-time is a network write.

On loopback that lock is held ~7 µs. Against a real Graylog with 1 ms of write
latency, the same mutex caps the process near 1000 eps regardless of worker
count — well inside the range a VCAPS burst produces, at which point
`pkg/queue` starts dropping.

EVTX is the one genuinely I/O-bound path: a CPU profile shows 450 ms of samples
across 1.11 s of wall clock (~40% on-CPU), dominated by `pthread_cond_wait`,
`pthread_cond_signal` and `runtime.fcntl`. go-evtx fsyncs per chunk
(`evtx.go:736`). That ceiling is not reachable from this repository.

### Memory

`readBody` caps request bodies at 64 MiB (`pkg/server/server.go:507`).
Measured on a full-size body: 190k events, **269 MiB live heap, 920 MiB total
allocated, 495 MiB RSS** — per request. `http.Server` (`main.go:325`) sets
timeouts only; there is no connection cap. A handful of concurrent full-size
PUTs is an OOM. The same request also pushes 190k events at a 100k-capacity
queue, dropping 90k on the spot.

For scale: a realistic 1000-event batch is ~350 KB. 64 MiB is ~190k events,
far past anything CEPA documents.

### Thread safety

Safe today, with soft spots. `Writer` requires concurrency safety
(`pkg/evtx/writer.go:69`) and the queue runs N workers against one shared
writer (`pkg/queue/queue.go:83`), so the requirement is load-bearing.

- GELF, syslog, beats: `sync.Mutex` over conn write and reconnect. Verified
  clean under `-race` with 32 goroutines × 200 writes. **No such test exists in
  the repository** — nothing fails if the mutex is deleted.
- `BinaryEvtxWriter`: local `mu` guards `closed`/`closeErr` only; the write
  path delegates to go-evtx's own mutex. `TestBinaryEvtxWriter_Concurrent`
  covers `WriteEvent` alone; `Rotate` interleaved with `WriteEvent` is
  uncovered, as the file itself states.
- `Win32EventLogWriter`: no mutex. `eventlog.Log` holds a bare handle and
  `ReportEvent` is thread-safe, so this is correct — but it is asserted
  nowhere in code and the `windows` CI job never exercises it concurrently.
- `MultiWriter`, `metrics.Store`: correct as written.

## Design

### 1. `WriteBatch` is mandatory on `Writer`

```go
type Writer interface {
    WriteEvent(ctx context.Context, e WindowsEvent) error
    WriteBatch(ctx context.Context, events []WindowsEvent) error
    Close() error
}
```

Mandatory, never type-asserted. With an optional `BatchWriter` interface,
`MultiWriter` failing to implement it degrades silently to the per-event loop —
the identical shape to the `Rotate` bug documented at
`pkg/evtx/writer_multi.go:47`, which was silent for real users running
`type = "multi"`. Made mandatory, that mistake is a compile error.

A shared helper `writeBatchSerially(ctx, w, events)` covers the two backends
where a batch genuinely is a loop, so EVTX and Win32 cost one line each.

### 2. Batching lives in the queue worker loop

Not in a decorator. A decorator needs its own input channel and flusher
goroutine to avoid a buffer mutex, which puts two channels in series —
`pkg/queue` is already a channel plus workers, and the second hop earns
nothing.

`q.work` becomes — the accumulation after the first event is a *blocking*
select against the timer, not a non-blocking drain. A non-blocking drain would
return immediately and the timer would never fire, collapsing every batch to
whatever happened to be resident at that instant:

```
e, ok := <-q.ch                    // block for the first event
if !ok { return }
batch := []WindowsEvent{e}
timer := newTimer(batch_timeout)
for len(batch) < max_batch {
    select {
    case e, ok := <-q.ch:
        if !ok {                   // channel closed: flush, do not discard
            writer.WriteBatch(ctx, batch)
            return
        }
        batch = append(batch, e)
    case <-timer.C:
        break                      // timeout: flush what we have
    }
}
writer.WriteBatch(ctx, batch)
```

The 200 ms timer lives naturally in each worker's own select loop. Writers keep
building payloads outside their lock, for K events instead of one, then take
the lock once and issue one wire write.

New config under `[queue]`:

```toml
max_batch        = 500
batch_timeout_ms = 200
drain_timeout_s  = 30   # shutdown drain deadline, see Shutdown correctness
```

Accepted trade-off: batching policy now lives in `pkg/queue` rather than in a
writer-agnostic wrapper, so a future non-queue caller of a writer gets no
batching. No such caller exists, and `WriteBatch` stays public if one appears.

Accepted loss window: up to `batch_timeout_ms` of events are in memory and lost
on SIGKILL or crash. Chosen over a size-only trigger, which strands the last
events of a quiet period indefinitely — unacceptable for an audit trail.

### 3. Per-writer batch semantics

| Writer | Batch behaviour | Syscalls per K events |
|---|---|---|
| GELF TCP | concat K null-terminated frames, one `Write` | K → 1 |
| GELF UDP | **no concatenation** — one datagram per event, looped inside one lock acquisition | K → K (lock K → 1) |
| Syslog TCP | concat K octet-counted frames, one `Write` | 2K → 1 |
| Syslog UDP | one datagram per message, looped inside one lock | K → K (lock K → 1) |
| Beats | `client.Send` already takes `[]any` — one window of K | K → 1, native |
| EVTX | `writeBatchSerially` | unchanged |
| Win32 | `writeBatchSerially` | unchanged |
| Multi | fans the whole batch to each backend, `errors.Join` | n/a |

Syslog TCP is the largest single win and was invisible before the spike:
`pkg/evtx/writer_syslog.go:78-84` issues `fmt.Fprintf` for the length prefix
and then `Write` for the payload — two syscalls per event, both under the lock.

UDP does not batch, by protocol. A GELF datagram is one message; concatenating
produces garbage. GELF's chunked-datagram format (`0x1e 0x0f` magic) is not
implemented here and is not worth implementing for this. UDP still takes the
lock-acquisition win, roughly half its per-event cost by the measurements
above.

### 4. Duplicate window widens — accepted

`sendWithRetry` reconnects and replays on failure. Today a failed write replays
one event; with batches, a TCP write that fails *after partially landing*
replays up to `max_batch` events and the receiver sees duplicates. Delivery
stays at-least-once — it already was — but the blast radius per incident goes
from 1 to 500.

The alternatives are sequence numbers the receivers do not support, or dropping
the retry and losing events instead. For an audit trail, duplicates beat gaps.

### 5. Metrics

Both existing counters stay **event**-counted, so nothing built on them
changes meaning:

- `EventsWrittenTotal += len(batch)` on success
- `WriterErrorsTotal += len(batch)` on failure

Counting per call instead would drop the observed error rate ~500× and quietly
silence any existing threshold alert.

Two new call-level counters:

- `cee_writer_batches_total` — one per `WriteBatch` call, success or failure
- `cee_writer_batch_errors_total` — one per failed call

`EventsWrittenTotal / cee_writer_batches_total` is the mean batch size. That
series is the falsifiability guard for this entire change: without it, a
`batch_timeout_ms` set too low, or a traffic shape delivering events one at a
time, degrades to batch=1 while every observable stays green and the ceiling is
exactly what it was before.

Logging: one ERROR line per failed batch, not per event — otherwise one bad
write emits 500 lines. The line carries `batch_size` plus the first event's
`event_id` and `file_path`, and states that all `batch_size` events failed.

Partial failure across backends (`MultiWriter` succeeds on GELF, fails on EVTX)
counts the whole batch failed. Same behaviour as today, scaled from 1 to K.
Per-backend accounting is a separate change; today's counters cannot express it
either.

`SetQueueDepth` moves from per-event to per-batch. It still reads `len(q.ch)`,
so it stays accurate, merely sampled less often. `/health` and the scrape both
read it independently.

### 6. Ingress bounding

Two changes in `pkg/server`, both configurable so an operator meeting a
genuinely larger batch has an escape hatch without a rebuild.

**Body cap 64 MiB → 8 MiB.** `server.go:507`, new `[server] max_body_mb = 8`.
8 MiB is ~23k events against a documented VCAPS batch of "thousands". Measured
scaling is ~4.2× body size in live heap, so one request caps at ~34 MiB.

**Concurrency semaphore, 8 slots.** New `[server] max_concurrent_requests = 8`.
A buffered channel in `Handler`, acquired **before** `readBody` and released at
the end of `ServeHTTP` — the slot must cover parse, not just the read, because
parse is where the 4.2× lives. Worst case becomes ~270 MiB live heap instead of
unbounded. Eight rather than thirty-two: at ~200 ms per 8 MiB request, eight in
flight is far past what a handful of arrays produce, and thirty-two slots would
put the ceiling back near 1.1 GB.

**On saturation, block; do not reject.** Acquire waits, bounded by the existing
10 s `ReadTimeout`. `readBody` runs *before* the ACK, so rejecting means no ACK
and the publisher may retry forever or mark this consumer unavailable. A
blocked publisher misses its 3 s ACK and degrades — bad, but it is one
publisher, and it retries. An OOM kills every publisher's stream at once and
loses the queue with it.

**The semaphore covers the CEPA handler only.** `/health` and `/metrics` are
separate mux entries and stay unthrottled. Putting observability behind the
thing being saturated is how a saturation event becomes invisible.

**New counter `cee_requests_throttled_total`**, incremented whenever acquire has
to wait. Without it the semaphore is a silent behaviour change that surfaces
only as unexplained publisher timeouts.

Not addressed here: the 14× allocation churn (920 MiB total-alloc on a 64 MiB
body). That is GC pressure, not a heap ceiling, and belongs to the parser item.

### 7. Shutdown correctness

Three defects — one pre-existing, two created by the batching change.

**Panic on `Enqueue` after `Stop`.** `main.go:386` logs `http_shutdown_error`
and falls through to `q.Stop()` at 390, which does `close(q.ch)`. If `Shutdown`
timed out, a handler is still live and its next `q.Enqueue` is a send on a
closed channel: panic, during shutdown, with the queue still holding events.

Fix: `sync.RWMutex` in `Queue`. `Enqueue` takes `RLock`, checks a `closed`
flag, sends. `Stop` takes `Lock`, sets `closed`, closes. Uncontended `RLock` is
~20 ns against an 8.6 µs parse.

**Partial batches lost at shutdown** — new. The worker's drain loop must flush
its in-flight batch when the channel closes, not simply return. Without it,
every worker silently discards up to `max_batch - 1` events on every shutdown:
4 workers × 499 ≈ 2000 audit events, with `EventsWrittenTotal` never counting
them and nothing in the log. This is the easiest way for this change to make
the product quietly worse.

**Drain runs under a cancelled context on Windows.** `queueCtx` derives from
`ctx` (`main.go:269`). The SIGTERM path leaves `ctx` alive, but the `ctx.Done()`
path — Windows SCM Stop, `main.go:377` — has already cancelled it before
`q.Stop()` drains. Latent today, because GELF, syslog and EVTX all ignore the
context. It stops being latent the moment any writer honours it, and then the
shutdown drain fails silently on Windows only — the platform this repository's
own notes record as the one local runs and reviews do not catch.

Fix: `Stop` drains under its own `context.WithTimeout(context.Background(), …)`
rather than the inherited one, bounded by `[queue] drain_timeout_s`. Cancellation
should abort a stalled write, not the shutdown flush itself.

## Testing

Stdlib only, table-driven, white-box, no `time.Sleep` for synchronisation.

**The race tests need a sharper target than "write concurrently".** `net.Conn`
is documented safe for concurrent use, so a test that fires N goroutines at
`WriteEvent` and passes under `-race` proves nothing — delete the mutex and it
still passes. The unsynchronised state is `w.conn` itself: `connect()` assigns
the field while `send()` reads it, on the reconnect path. The test must force a
reconnect (sink closes the connection mid-run) while other goroutines write.
Each of the three network writers' race tests must be validated by removing the
mutex and observing the failure before the test counts as a guard.

Batching tests, each mutation-checked:

- K events queued → exactly one `WriteBatch` of K, not K calls of 1
- one event with nothing following → flushed within `batch_timeout_ms`
- partial batch flushed on `Stop`
- `Enqueue` after `Stop` does not panic (remove the guard → must panic)

The timeout test requires the timer to be injectable rather than slept on: the
worker takes a timer-factory field defaulting to `time.NewTimer`, and tests
drive it directly.

Framing tests, per writer. Highest risk is syslog TCP octet-counting: one wrong
length prefix in a concatenated batch desynchronises the receiver for every
subsequent message, and the failure appears at the collector, not here. The
test asserts a sink parses K frames back out intact. Same for GELF TCP
null-termination. UDP asserts K **datagrams**, not one — the assertion that
catches someone "optimising" UDP into a concatenation later.

`MultiWriter` batch fan-out is compile-enforced but still tested: the batch
reaches every backend, errors join.

Ingress: the semaphore blocks the N+1th request, `cee_requests_throttled_total`
increments, `/health` still answers while saturated.

**Benchmarks become permanent but stay on demand.** The repository has none
today; every number in this document came from throwaway files, since deleted.
`BenchmarkWriteBatch` per writer plus the parser benchmarks are added for real,
reachable through a new `make bench`. They are **not** in `make ci` and **not**
triggered on push or pull request — benchmarks are too noisy for a gate. The
only CI trigger is `workflow_dispatch`, runnable from the Actions tab.

## Deferred

**Parser allocations.** 73 allocs and 4.0 KB per event from `encoding/xml`.
Today parse (8.6 µs) sits under the GELF write (11.4 µs), so it is not the
bottleneck; after batching drops the per-event write cost toward the ~5 µs
`buildGELF` alone costs, parse becomes dominant. Measure first: if
`EventsWrittenTotal / cee_writer_batches_total` shows real batching and a
profile puts `encoding/xml` on top, proceed. If not, **drop the item.**

If it proceeds, scope is a hand-rolled attribute scanner for
`ParseCheckEventRequestDecoded` only — that dialect is flat, attribute-only
elements, the one shape worth hand-parsing. The OneFS path stays on
`encoding/xml`. Two non-negotiable guards: a differential test asserting the
hand-rolled parser produces identical `[]CEPAEvent` against `encoding/xml` over
a corpus, and an explicit fallback to `encoding/xml` on anything the scanner
does not recognise — logged, never silent. Hand-rolled XML is a well-known
footgun and this input arrives from the network.

The UTF-16LE path is worse than UTF-8 (14.6 KB/event against 4.0) because the
transcode allocates a second whole copy of the body. Noted, not scheduled.

**go-evtx fsync cadence.** Separate issue against
`github.com/fjacquet/go-evtx`: make fsync cadence configurable. `evtx.go:736`
fsyncs per chunk, which is the entire 12.5k eps ceiling and the ~60% blocked
time measured here. Per ADR-014 that belongs upstream. This repository does not
wait on it, and EVTX and Win32 throughput are explicitly unchanged by this
design.

## Sequencing

Guards land before the risky change.

1. Shutdown guard — smallest, removes a live panic
2. Race tests for GELF/syslog/beats — establishes the mutation discipline the
   rest relies on
3. Ingress bounding — independent, closes the OOM hole
4. Batching — lands on a base that already has guards
5. Parser — only if step 4's numbers justify it

## Documentation

`docs/PROMISES.md`: every throughput claim maps to a verifying job or is marked
Unverified. Benchmarks are on demand only, so **the throughput improvement
ships as Unverified.** Publishing "10× faster" behind nothing but a local
benchmark is the class of claim the `feat/v4.1.3-truth` work existed to remove.

`config.toml` gains `max_batch`, `batch_timeout_ms`, `drain_timeout_s`,
`max_body_mb` and `max_concurrent_requests`, each with the reasoning above in a
comment.

`CLAUDE.md`: the queue description changes from "buffered channel + worker
goroutines" to note the batching drain, and the `Writer` interface description
gains `WriteBatch`.
