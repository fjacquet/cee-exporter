# Project Research Summary

**Project:** cee-exporter v4.0 Industrialisation
**Domain:** Go daemon — periodic fsync + file rotation for BinaryEvtxWriter
**Researched:** 2026-03-04
**Confidence:** HIGH

## Executive Summary

The cee-exporter v4.0 milestone adds production-grade durability and file lifecycle management to the `BinaryEvtxWriter` — the Linux-side EVTX output backend. Today the writer accumulates all events in memory and flushes them only on `Close()`, meaning a crash loses every event since process start. The v4.0 goal is a bounded durability guarantee (events on disk within 15 seconds) and automatic file rotation (by size, time, and count) so operators do not need to manually manage `.evtx` file growth. The recommended approach uses only Go stdlib primitives (`os.File.Sync()`, `time.NewTicker`, `context.Context`) plus one carefully chosen pure-Go dependency (`gopkg.in/natefinch/lumberjack.v2` v2.2.1 for its rotation behavioral model), preserving the `CGO_ENABLED=0` static-linking constraint.

The foundational change is architectural: the current `flushToFile()` call uses `os.WriteFile()` — an open/write/close per invocation with no persistent file handle. All v4.0 features depend on replacing this with a persistent `*os.File` held for the writer's lifetime, appended to incrementally, and synced periodically. Once that open-handle model is established, the remaining features — fsync goroutine, `rotate()` helper, size/time/count triggers, and config fields — follow a clear dependency chain with low individual complexity. The research identifies nine concrete pitfalls that must be avoided, the most severe being a write/rotate race condition (data corruption), a goroutine leak from `ticker.Stop()` without a done channel (silent resource exhaustion), and omitting CRC patching at the correct moment (invalid EVTX files that forensics parsers reject).

The four suggested implementation phases map directly to the dependency graph: goroutine scaffolding first (establishes locking contract and shutdown), then open-handle incremental flush (highest risk, must be correct before anything else), then rotation (safe once flush is correct), and finally config hardening (validation, TOML fields, ADRs). This order eliminates every identified pitfall at the phase where it would otherwise be introduced.

---

## Key Findings

### Recommended Stack

The v4.0 stack is intentionally minimal. All existing capabilities (GELF, Win32, Syslog, Beats, TLS, Prometheus, async queue) are already validated and require no changes. Only `BinaryEvtxWriter` and `main.go` config wiring are modified.

See full analysis: `.planning/research/STACK.md`

**Core technologies:**
- `os.File.Sync()` (stdlib): periodic fsync — direct Go wrapper for `fsync(2)` on Linux and `F_FULLFSYNC` on Darwin; no dependency, returns `error`, the only correct primitive for the durability guarantee
- `time.NewTicker` (stdlib): periodic flush and rotation trigger — canonical repeating-interval pattern; combines with `select` + done-channel for clean shutdown; zero allocation after creation
- `context.Context` (stdlib): goroutine lifecycle — propagates shutdown from the daemon's main signal handler into the flush goroutine; matches existing queue pattern
- `gopkg.in/natefinch/lumberjack.v2` v2.2.1: behavioral model for size/count/age rotation — pure Go (confirmed CGO-free); exposes `Rotate()`; de-facto Go standard; used for its naming and retention logic only, NOT as the byte sink for EVTX writes

**Critical version note:** lumberjack v3 is flagged by pkg.go.dev as NOT the latest version of its module — v2 is canonical. The archived `lestrrat-go/file-rotatelogs` library must never be used (archived July 2021, explicit author warning "DO NOT USE THIS PROJECT").

---

### Expected Features

Research identifies eight P1 features required for v4.0 to be considered complete, plus two P2 differentiators to defer to v4.x.

See full analysis: `.planning/research/FEATURES.md`

**Must have (v4.0 table stakes):**
- Persistent `*os.File` handle — structural foundation; blocks all other features; currently absent (file only open during `Close()`)
- Periodic fsync via `flush_interval_s` — the named milestone requirement; default 15 seconds
- `rotate()` function — required by size, time, and SIGHUP rotation; implement once, call everywhere
- Size-based rotation via `max_file_size_mb` — prevents unbounded file growth; forensics tools reject very large `.evtx` files
- Count-based retention via `max_file_count` — prevents disk exhaustion after rotation; filesystem glob + delete operation
- Time-based rotation via `rotation_interval_h` — aligns file boundaries with SIEM ingestion windows
- Config `[output]` new TOML fields — all parameters operator-configurable without recompiling
- ADRs for flush interval and rotation strategy — named v4.0 requirement; documents "why 15s" and "why rename-not-truncate"

**Should have (v4.x differentiators):**
- SIGHUP-triggered rotation — standard Unix convention; nearly free once `rotate()` exists; one signal channel, one select case, under 20 LOC
- Prometheus `cee_last_fsync_unix_seconds` gauge — enables SRE alerting for stuck writers; observable durability

**Defer (v5+):**
- Multi-chunk EVTX support — `flushChunkLocked()` stub must be replaced (Pitfall 8), but full multi-chunk file design is a separate optimization milestone
- Compression of rotated files — blocked on forensics tool support; `.evtx` files compressed with gzip or zstd are unreadable by Event Viewer, python-evtx, and Splunk without explicit decompression

**Anti-features (avoid regardless of operator requests):**
- Using lumberjack as the EVTX byte sink — lumberjack manages files as plain text streams and cannot control EVTX chunk CRC patching and record ID sequencing
- Async rotation in a background goroutine — creates a race window where events may be written to the wrong file; rotation must be synchronous under `w.mu`
- Per-event fsync — at CEPA batch ingestion rates, per-event fsync backs up the queue and drops events; periodic fsync produces better durability outcomes

---

### Architecture Approach

All v4.0 changes are contained within two existing packages (`pkg/evtx` and `cmd/cee-exporter`); no new packages are required. The `Writer` interface remains at two methods (`WriteEvent`, `Close`) — unchanged. The queue, parser, mapper, server, and all other writer backends are untouched.

See full analysis: `.planning/research/ARCHITECTURE.md`

**Major components and responsibilities:**

1. `BinaryEvtxWriter` (`pkg/evtx/writer_evtx_notwindows.go`) — PRIMARY CHANGE FILE: gains `RotationConfig` struct, persistent `*os.File` field, `pending []byte` staging buffer, `currentFileBytes int64` counter, `stopCh`/`stopped` channels, `backgroundLoop()` goroutine, `rotateLocked()` helper, `openRotatedFile()` helper, `pruneOldFiles()` helper; `flushToFile()` is removed and replaced by the incremental open-handle model
2. `OutputConfig` and `buildWriter()` in `cmd/cee-exporter/main.go` — gains four new TOML-mapped fields (`flush_interval_s`, `max_file_size_mb`, `max_file_count`, `rotation_interval_h`) and maps them to `RotationConfig` at construction time; `defaultConfig()` sets `FlushIntervalSec = 15`
3. `writer_native_notwindows.go` — minimal change: pass zero `RotationConfig{}` to `NewBinaryEvtxWriter` for backward compatibility
4. `config.toml.example` — updated with four new `[output]` fields and operator guidance on zero-value semantics

**Key patterns:**
- Writer-owned background goroutine: fsync ticker and rotation ticker live inside `BinaryEvtxWriter`; lifecycle is identical to the writer object; no queue changes required
- Open-handle incremental write: `WriteEvent` stages encoded records in `w.pending`; the fsync tick drains pending to `w.f` then calls `f.Sync()`; write cost per tick is proportional to new events only, not total file size
- Timestamped rotation naming with sequence number: `audit-2026-03-04T120000Z-00001.evtx`; sequence number prevents collision when two rotations occur within the same wall-clock second

---

### Critical Pitfalls

Nine pitfalls identified; five carry data-loss or data-corruption risk.

See full analysis: `.planning/research/PITFALLS.md`

1. **Write/rotate race — lock scope mismatch** — all I/O paths must hold `w.mu` for the complete critical section; never call `mu.Lock()` inside a helper that is already called under the lock; validate with `go test -race ./pkg/evtx/` (requires CGO=1; run separately from `make test`)
2. **Goroutine leak — `ticker.Stop()` without done channel** — `ticker.Stop()` does not close the channel and does not terminate the goroutine; always pair with a `done chan struct{}` closed by `Close()` and a `sync.WaitGroup` to synchronize goroutine exit
3. **EVTX file header not updated on each flush** — `NextRecordIdentifier` and `ChunkCount` go stale if not rewritten at offset 0 after each chunk flush; forensics parsers rely on these fields for sequence validation; add a hex-dump test asserting `NextRecordIdentifier` at bytes 24–32
4. **CRC computed before records are complete** — `patchChunkCRC` must be called exactly once per closed chunk; calling it mid-write and continuing to append records produces a CRC covering partial data
5. **`flushChunkLocked()` stub silently drops events** — the current placeholder returns nil and logs a warning only; events beyond `chunkFlushThreshold` (~2,400 events) are permanently lost; must be replaced before v4.0 ships
6. **Rotation filename collision on sub-second rotation** — timestamp-only names collide when two rotations fire within the same second; include a monotonic `w.rotationSeq` counter in every archive filename
7. **`os.Rename` not atomic on Windows** — Windows `MoveFileEx` requires `MOVEFILE_REPLACE_EXISTING`; encapsulate in platform-specific build-tag helpers (`rename_windows.go` / `rename_notwindows.go`)
8. **Config zero-value ambiguity causing ticker panic** — `time.NewTicker(0)` panics at runtime; always gate with `if cfg.FlushIntervalS > 0`; add `validateConfig()` that rejects zero or negative `flush_interval_s` at startup
9. **fsync missing after WriteFile** — `os.File.Close()` does not issue `fsync(2)`; only `f.Sync()` guarantees durability; document the chosen model in an ADR

---

## Implications for Roadmap

Based on the dependency graph from FEATURES.md and the pitfall-to-phase mapping from PITFALLS.md, four phases are recommended. The ordering is driven by concrete dependencies, not preference.

### Phase 1: Goroutine Scaffolding and fsync

**Rationale:** Pitfalls 1, 3, and 9 — the locking contract, goroutine lifecycle, and fsync semantics — must be established before any I/O code is written. Getting the concurrency architecture wrong here requires a full rewrite of every subsequent phase.

**Delivers:**
- Background goroutine with correct `done chan struct{}` and `sync.WaitGroup`
- Periodic `f.Sync()` on ticker; `Close()` that signals and waits for goroutine exit before final flush
- Decision documented in ADR: full-rewrite-with-rename vs. open-handle-with-sync (whichever is chosen must be consistent throughout the implementation)

**Addresses:** Periodic fsync (`flush_interval_s`), graceful shutdown flush

**Avoids:** Pitfall 1 (lock race established), Pitfall 3 (goroutine leak), Pitfall 9 (fsync semantics)

**Research flag:** Standard patterns — Go ticker and goroutine lifecycle are canonical and exhaustively documented; no additional research needed.

---

### Phase 2: Open-Handle Incremental Flush

**Rationale:** This is the highest-risk change. The transition from `os.WriteFile()` (open/write/close per invocation) to a persistent `*os.File` (held for writer lifetime, appended incrementally) requires coordinated changes to `NewBinaryEvtxWriter`, `WriteEvent`, and `Close`. Pitfalls 2, 4, and 8 all live here. This must be correct and validated with python-evtx before rotation is added.

**Delivers:**
- Persistent `*os.File` held from construction to `Close()`
- `pending []byte` staging buffer; `currentFileBytes` counter
- `WriteEvent` appends to pending instead of accumulating all records
- `Close()` flushes pending, patches EVTX file header at offset 0 (including `NextRecordIdentifier` and `ChunkCount`), syncs, closes
- `flushChunkLocked()` stub replaced with real implementation; two-flush session produces a parseable file per python-evtx

**Addresses:** Persistent `*os.File` handle (structural change), real chunk boundary handling

**Avoids:** Pitfall 2 (stale file header), Pitfall 4 (CRC before records complete), Pitfall 8 (stub drops events)

**Research flag:** Deep domain knowledge required — EVTX binary format correctness for the header-patch-at-offset-0 pattern; validate with python-evtx after each structural change before proceeding.

---

### Phase 3: File Rotation (Size, Time, Count)

**Rationale:** Rotation depends on Phase 2's open-handle model and Phase 1's locking contract. Once those foundations exist, the `rotateLocked()` helper, size trigger, time ticker, and pruning are individually straightforward. Pitfalls 5, 6, and 7 must be addressed here before rotation is considered complete.

**Delivers:**
- `rotateLocked()` helper: sync, finalize headers, close, rename, open new file with fresh EVTX header
- `openRotatedFile()` with timestamped + monotonic sequence-number naming
- Size-based trigger check in `WriteEvent`; time-based rotation ticker in `backgroundLoop()`
- `pruneOldFiles()` using `filepath.Glob` sorted by mtime
- Platform-specific `renameFile()` helper: `rename_windows.go` using `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING`; `rename_notwindows.go` using `os.Rename` + directory fsync

**Addresses:** Size-based rotation (`max_file_size_mb`), time-based rotation (`rotation_interval_h`), count-based retention (`max_file_count`)

**Avoids:** Pitfall 5 (filename collision via sequence number), Pitfall 6 (non-atomic Windows rename via `MoveFileEx`)

**Research flag:** Windows rename behavior — validate `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING` on the target Go version; confirmed issue in `golang/go#8914`. Directory fsync after rename on Linux requires direct syscall not currently used in the codebase — confirm the implementation pattern before coding.

---

### Phase 4: Config, Validation, and ADRs

**Rationale:** Config field stubs can be added in Phase 1, but full validation logic and operator documentation must be finalized as a dedicated effort to avoid `time.NewTicker(0)` panics in production and to provide clear operator guidance on the semantics of each zero value. The ADRs are a named v4.0 deliverable.

**Delivers:**
- Four new TOML fields on `[output]`: `flush_interval_s`, `max_file_size_mb`, `max_file_count`, `rotation_interval_h`
- `validateConfig()` rejecting zero or negative `flush_interval_s`; `defaultConfig()` setting `FlushIntervalSec = 15`
- `config.toml.example` updated with all four fields, zero-value semantics documented for each
- ADR-01: Flush ticker ownership — writer layer vs. queue layer; explains why `Flush()` was not added to the `Writer` interface
- ADR-02: Open-handle vs. write-on-close model and EVTX crash tolerance

**Addresses:** Config `[output]` new fields; ADRs (named v4.0 requirement)

**Avoids:** Pitfall 7 (ticker panic from zero `flush_interval_s`)

**Research flag:** Standard patterns — TOML field mapping and startup validation follow the same conventions already established in `main.go`; no additional research needed.

---

### Phase Ordering Rationale

- **Phase 1 before Phase 2:** The goroutine and locking architecture must be correct before any persistent file handle code is written. Adding I/O to a racy scaffold requires a full rewrite.
- **Phase 2 before Phase 3:** Rotation calls into the flush path. If the flush path has EVTX header or CRC bugs, every rotated file will be corrupt. Validate flush in isolation first.
- **Phase 3 after Phase 2:** Size-based rotation depends on `currentFileBytes` (Phase 2 open-handle model); time-based rotation calls `rotateLocked()` which calls `doFlushLocked()` (Phase 2 flush path).
- **Phase 4 can partially overlap Phase 1:** TOML struct fields and `defaultConfig()` can be stubbed in Phase 1 as the API contract for the goroutine parameters. Validation logic and ADRs are finalized in Phase 4.
- **No changes to queue, server, parser, mapper, or other writers:** This milestone is intentionally scoped to a single component. The `Writer` interface remains stable at two methods.

---

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 2:** EVTX binary format correctness — CRC field offsets, `NextRecordIdentifier` byte position (24–32), chunk boundary rules; validate each flush invariant with python-evtx before moving to Phase 3.
- **Phase 3 (Windows rename path):** `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING` — confirm behavior on the target Go version; consider whether Windows rotation testing requires a Windows CI runner or can be covered with a build-tag unit test.

Phases with standard patterns (skip research-phase):
- **Phase 1:** Go ticker and goroutine lifecycle are canonical; `done`+`WaitGroup` pattern is the authoritative approach per official Go docs.
- **Phase 4:** TOML config wiring and startup validation are identical to existing `[output]` field patterns already in `main.go`.

---

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All recommendations verified against pkg.go.dev, official Go stdlib docs, and lumberjack source inspection. Single new dependency (`lumberjack v2.2.1`) confirmed pure Go and CGO-free. Archived alternative definitively eliminated. |
| Features | HIGH | Dependency graph derived from direct codebase inspection of `BinaryEvtxWriter`, `Writer` interface, and queue lifecycle. Feature list cross-validated against EVTX format requirements. |
| Architecture | HIGH | Based on direct source inspection of all relevant files. Component boundaries and suggested build order are unambiguous given the dependency structure. |
| Pitfalls | HIGH (code pitfalls) / MEDIUM (OS-level edge cases) | Concurrency and EVTX format pitfalls are code-inspection-driven (HIGH). OS-level fsync/rename edge cases — directory fsync after rename on Linux, Windows `MoveFileEx` atomicity — are sourced from community references and confirmed Go issues (MEDIUM). |

**Overall confidence:** HIGH

### Gaps to Address

- **`flushChunkLocked()` stub replacement scope:** The stub must be replaced (Pitfall 8), but full multi-chunk EVTX support is deferred to v5+. The Phase 2 implementation must define the minimum viable chunk-boundary handling — finalize the current chunk and start a new one — without implementing the complete multi-chunk file format. This boundary needs explicit definition in the Phase 2 task specification.
- **Windows rotation test coverage:** Windows CI availability for this milestone is not confirmed. If a Windows CI runner is unavailable, the `rename_windows.go` helper must have a unit test with a mock or be validated manually. Flag this risk during Phase 3 planning.
- **Directory fsync after rename on Linux:** The research recommends `syscall.Fsync(dirFd)` after `os.Rename()` for crash-safe rotation. This is a correctness requirement, not optional, but it adds a raw syscall not currently used anywhere in the codebase. Confirm the implementation pattern during Phase 3.

---

## Sources

### Primary (HIGH confidence)
- [pkg.go.dev/gopkg.in/natefinch/lumberjack.v2](https://pkg.go.dev/gopkg.in/natefinch/lumberjack.v2) — struct fields, `Rotate()` method, v2.2.1 release date
- [github.com/natefinch/lumberjack blob/v2.0/chown.go](https://github.com/natefinch/lumberjack/blob/v2.0/chown.go) — confirmed CGO-free: pure Go `os.Chown` + `syscall.Stat_t` only
- [pkg.go.dev/os#File.Sync](https://pkg.go.dev/os#File.Sync) — confirmed maps to `fsync(2)` / `F_FULLFSYNC` on Darwin; returns error
- [github.com/golang/go/issues/68483](https://github.com/golang/go/issues/68483) — ticker goroutine leak; `ticker.Stop()` does not close channel; potential `go vet` check
- [github.com/golang/go/issues/8914](https://github.com/golang/go/issues/8914) — `os.Rename` not atomic on Windows; `MoveFileEx` required
- [github.com/lestrrat-go/file-rotatelogs](https://github.com/lestrrat-go/file-rotatelogs) — confirmed ARCHIVED July 2021; README: "DO NOT USE THIS PROJECT"
- Direct codebase inspection: `pkg/evtx/writer_evtx_notwindows.go`, `pkg/evtx/writer.go`, `pkg/evtx/evtx_binformat.go`, `pkg/queue/queue.go`, `cmd/cee-exporter/main.go`, `config.toml.example`

### Secondary (MEDIUM confidence)
- [michael.stapelberg.ch — atomically writing files in Go](https://michael.stapelberg.ch/posts/2017-01-28-golang_atomically_writing/) — rename + fsync directory pattern
- [evanjones.ca — durability: Linux file APIs](https://www.evanjones.ca/durability-filesystem.html) — fsync semantics, why directory fsync is needed after rename
- [danluu.com — files are hard](https://danluu.com/file-consistency/) — crash safety, rename atomicity caveats
- [libevtx EVTX binary format documentation](https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc) — CRC field structure, chunk layout, `NextRecordIdentifier` offset
- [notes.qazeer.io — EVTX integrity and CRC fields](https://notes.qazeer.io/dfir/windows/ttps_analysis/evtx_integrity)
- [blogtitle.github.io — Go advanced concurrency patterns: timers](https://blogtitle.github.io/go-advanced-concurrency-patterns-part-2-timers/) — ticker goroutine leak patterns

### Tertiary (LOW confidence)
- [pkg.go.dev/gopkg.in/lumberjack.v3](https://pkg.go.dev/gopkg.in/lumberjack.v3) — confirmed NOT latest per pkg.go.dev notice; v2 canonical (inferred from site notice, not authoritative changelog)

---
*Research completed: 2026-03-04*
*Ready for roadmap: yes*
