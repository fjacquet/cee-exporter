# ADR-013: Write-on-Close Model and Checkpoint-Write Semantics

**Status:** Accepted (superseded by Phase 10 open-handle model when complete)
**Date:** 2026-03-04
**Phase:** 9 — Goroutine Scaffolding and fsync

## Context

go-evtx `v0.1.0` used a write-on-close model: events were buffered in memory (`w.records []byte`) and written atomically to disk only when `Close()` was called via `os.WriteFile`. This is simple and correct for short-lived sessions, but provides no intermediate durability — a process crash between startup and `Close()` loses all buffered events.

Phase 9 extends this with a **checkpoint-write pattern**: a background goroutine calls `flushToFile()` (which calls `os.WriteFile`) on every `FlushIntervalSec` interval. This produces a complete, parseable EVTX file at each checkpoint.

## Decision

Retain the write-on-close model for Phase 9 with checkpoint-write extension. Accept the documented limitations. Plan Phase 10 to supersede with an open-handle model.

## Current Model: Checkpoint-Write

### What it does

1. On every ticker interval, `backgroundLoop()` acquires `w.mu`, checks `len(w.records) > 0`, and calls `w.flushToFile()`.
2. `flushToFile()` calls `os.WriteFile(w.path, fileBytes, 0o644)`, which atomically replaces the output file with the current in-memory snapshot.
3. On `Close()`, a final `flushToFile()` is called after the goroutine exits.

### What it guarantees

- **Bounded loss window:** At most `FlushIntervalSec` seconds of events can be lost on unclean shutdown (SIGKILL, OOM, power failure).
- **Parseable checkpoints:** Each checkpoint produces a syntactically valid EVTX file with correct file header, chunk header, and CRC32 fields. python-evtx can parse any checkpoint file.
- **Monotonic completeness:** Each checkpoint file is a superset of the previous — events are never removed between checkpoints.
- **Graceful shutdown completeness:** On SIGTERM/SIGINT, `q.Stop()` -> `writer.Close()` flushes all buffered events before the process exits. Zero events are lost on graceful shutdown.

### What it does NOT guarantee

- **True fsync durability:** `os.WriteFile` opens, writes, and closes the file. On Linux, `close(2)` does NOT issue `fsync(2)` — the kernel write cache may not be flushed to storage. A kernel panic or storage failure between a checkpoint write and the next fsync could corrupt or lose the latest checkpoint file. True durability requires `f.Sync()` after every write.
- **`f.Sync()` is impossible in Phase 9:** `os.WriteFile` does not expose the underlying `*os.File`. Calling `f.Sync()` requires holding a persistent open file handle across writes — which is the Phase 10 refactor.
- **Multi-chunk sessions:** The current model buffers all records in a single `w.records []byte` slice and writes them as a single EVTX chunk (max 65,536 bytes). Events beyond the chunk boundary trigger a `go_evtx_chunk_boundary_reached` warning but are silently truncated. Phase 10 fixes this with an open-handle model that flushes chunks incrementally.

## Phase 10 Planned Supersession

Phase 10 will replace `os.WriteFile` with a persistent `*os.File` opened at `New()` time:

- `f.Write()` appends event records incrementally (no full-file rewrite per tick)
- `f.Sync()` is called after each ticker interval for true fsync durability
- EVTX file header and chunk header are updated in-place (seek + write)
- The single-chunk limitation is removed; multi-chunk files are written correctly

After Phase 10, ADR-013 will be updated to "Superseded by Phase 10 open-handle model."

## Consequences

- Operators should set `flush_interval_s` to a value acceptable for their durability SLA (default 15s)
- Operators should treat checkpoint-write as "best-effort" durability, not "guaranteed" durability
- The `go_evtx_file_written` slog event indicates a checkpoint was completed; absence of this event after startup means no checkpoint has fired yet
- Phase 10 is a prerequisite for true `f.Sync()` durability guarantees

## Alternatives Considered

| Alternative | Reason Rejected |
|-------------|-----------------|
| No goroutine (write-on-close only) | Long-running sessions lose all events on unclean shutdown — unacceptable |
| Open-handle model in Phase 9 | Requires EVTX header seek/rewrite logic that is Phase 10's scope; doing it in Phase 9 collapses two phases and risks introducing EVTX format bugs |
| No-op goroutine (skeleton only) | Delivers FLUSH-01 as a stub but misleads operators about durability; rejected |
