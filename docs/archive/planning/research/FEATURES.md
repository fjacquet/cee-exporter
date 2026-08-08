# Feature Research

**Domain:** Go daemon — periodic fsync and file rotation for BinaryEvtxWriter (v4.0 Industrialisation)
**Researched:** 2026-03-04
**Confidence:** HIGH (Go stdlib patterns and lumberjack behavioral model are authoritative sources)

---

## Context: Existing Capabilities (Already Shipped)

This is research for a subsequent milestone only. The following are complete and must not be re-implemented:

| Already Built | Where |
|---------------|-------|
| CEPA ingestion, XML parsing, 6 output backends | `pkg/server`, `pkg/parser`, `pkg/mapper`, `pkg/evtx` |
| TLS (off/manual/acme/self-signed) | `cmd/cee-exporter/tls.go` |
| Prometheus metrics, async queue | `pkg/metrics`, `pkg/queue` |
| BinaryEvtxWriter: template-based BinXML, in-memory buffering, flush on Close() | `pkg/evtx/writer_evtx_notwindows.go` |
| Writer interface: `WriteEvent(ctx, WindowsEvent) error` + `Close() error` | `pkg/evtx/writer.go` |
| Config system: TOML, `[output]` section | `cmd/cee-exporter/main.go` |

**Critical structural observation:** `BinaryEvtxWriter.flushToFile()` currently uses `os.WriteFile()` — it opens, writes, and closes the file atomically per call. No persistent `*os.File` handle is held between events. Periodic fsync is impossible without first changing this. The foundational change for all v4.0 features is: open the file in `NewBinaryEvtxWriter()`, hold it as a struct field, and close it only in `Close()` or `Rotate()`.

---

## Feature Landscape

### Table Stakes (Users Expect These)

Features that must exist for the v4.0 milestone to be considered complete. Missing any of these means the durability/rotation story is broken in production.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Persistent `*os.File` handle (structural change) | All other v4.0 features depend on it; today the file is only open during `Close()` | MEDIUM | Open in `NewBinaryEvtxWriter()`, hold as `w.file *os.File`; write event bytes incrementally instead of accumulating them all in `w.records []byte`; close only in `Close()`/`Rotate()`. Track `w.currentSize int` for size-based rotation. |
| Periodic fsync (flush_interval_s) | Without it events are lost on crash; CEPA is a compliance use-case; 15s is the named milestone target | MEDIUM | Background goroutine started by `NewBinaryEvtxWriter()` (or a separate `Start()` method); `time.NewTicker(flushInterval)` fires `w.file.Sync()` under `w.mu`; goroutine stops cleanly when `Close()` is called via a stop channel. `os.File.Sync()` wraps `fdatasync(2)` on Linux — no custom syscall needed. |
| Size-based rotation (max_file_size_mb) | Prevents unbounded file growth; forensics tools reject very large .evtx files; disk exhaustion is a production incident | MEDIUM | Track `w.currentSize` after each `WriteEvent`; before writing a new event, check `w.currentSize + len(encoded) >= maxFileSize`; if so, call `w.rotate()` first. 0 = disabled. |
| Count-based retention (max_file_count) | Without cleanup, rotation alone fills disk faster; operators expect "keep last N files" behavior | LOW | After each `w.rotate()`, call `w.cleanOldFiles()`: `filepath.Glob(baseName+"-*.evtx")`, sort by mtime, delete oldest until `len(backups) <= maxFileCount`. 0 = keep all. |
| Time-based rotation (rotation_interval_h) | Aligns file boundaries with SIEM ingestion windows and daily forensics collection | MEDIUM | Second ticker in the fsync goroutine (or a second goroutine); fires `w.rotate()` every N hours; fires even on empty file to maintain predictable file-per-interval naming. 0 = disabled. |
| Config `[output]` new fields | Operators need to tune all parameters via `config.toml` without recompiling | LOW | Extend `OutputConfig` in `main.go`: `FlushIntervalS int` (tag `flush_interval_s`, default 15), `MaxFileSizeMB int` (tag `max_file_size_mb`, default 0), `MaxFileCount int` (tag `max_file_count`, default 0), `RotationIntervalH int` (tag `rotation_interval_h`, default 0). Pass to `NewBinaryEvtxWriter()` only when `type = "evtx"`. |
| fsync before rename (rotation correctness) | Ensures all buffered data reaches disk before the rotated file is renamed; prevents partial files in backup set | LOW | Inside `w.rotate()`: call `w.file.Sync()`, finalize EVTX file header (chunk count, last record ID), close file, `os.Rename(activePath, backupPath)`, open new file, write new EVTX file header. |
| Graceful shutdown flushes then syncs | On SIGTERM, in-memory state must reach disk before the process exits | LOW | `Close()` must: signal stop channel, drain ticker goroutine, call `w.file.Sync()`, finalize headers, close file. The existing `q.Stop()` -> `w.Close()` chain in `main.go` already provides the correct hook — no changes needed there. |

### Differentiators (Competitive Advantage)

Features that go beyond the minimum and add meaningful production value for v4.0 or v4.x.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| SIGHUP-triggered rotation | Standard Unix convention; lets ops rotate without restart; integrates with OS `logrotate(8)` | LOW | After `rotate()` method exists, add `signal.Notify(sighupCh, syscall.SIGHUP)` to the background goroutine's `select`. Nearly free — one signal channel, one case. Not required for v4.0 but cost is <20 LOC once infrastructure exists. |
| Prometheus gauge: `cee_last_fsync_unix_seconds` | Enables SRE alerting: "fsync hasn't fired in >30s" catches stuck writers; observable durability | LOW | One `prometheus.Gauge` in `pkg/metrics`, set to `float64(time.Now().Unix())` after each `file.Sync()` call. Wire into existing Prometheus registry. |
| ADRs for flush interval and rotation strategy | Process requirement named in PROJECT.md; documents "why 15s" and "why rename-not-truncate" for future maintainers | LOW | Two markdown files in `docs/adr/`. Architecture decision records are text, not code. |

### Anti-Features (Commonly Requested, Often Problematic)

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| Use lumberjack library for rotation | Well-known, proven library; seems like "use existing tool" | Lumberjack implements `io.Writer` and manages files as plain text streams. BinaryEvtxWriter must control EVTX file header writing, chunk CRC patching, and record ID sequencing — none of which fit the `io.Writer` contract. Wrapping would require coupling incompatible abstractions. | Implement rotation natively in `BinaryEvtxWriter` using lumberjack's behavioral model (size/count/time triggers, rename strategy) without the library dependency. |
| Compress rotated .evtx files | Saves disk space; lumberjack supports gzip | `.evtx` files compressed with gzip/zstd are not readable by Windows Event Viewer, python-evtx, Splunk, or forensics tools without decompression. Compliance workflows assume uncompressed `.evtx`. Adds complexity for zero tooling compatibility. | Let OS-level storage compression (zfs, btrfs, filesystem-level) handle this if needed. |
| Async rotation in a background goroutine | "Don't block WriteEvent during rotation" | Creates a race window: events written after the rotation decision but before the new file is opened may be lost or written to the wrong file. EVTX rotation requires CRC patching which must be atomic relative to file state. | Rotate synchronously under `w.mu`. Rotation is rare (triggered by size/time threshold) and fast (fsync + rename + new file header = typically <5ms). |
| Per-event fsync | "Maximum durability" for compliance | At CEPA ingestion rates (thousands of events per PUT batch), per-event fsync serializes all I/O and causes the queue to back up, which drops events. This produces worse durability outcomes than periodic fsync because dropped events are permanently lost. | Use `flush_interval_s` (default 15). Document the guarantee: "events reach disk within 15 seconds of being written to the queue". |
| fdatasync via custom syscall | "fdatasync is faster than fsync" | Go's `os.File.Sync()` already calls `fdatasync(2)` on Linux (verified in Go stdlib source `src/os/file_unix.go`). There is no separate `FdataSync()` method in the Go standard library — `f.Sync()` is already the optimal call on Linux. | Use `f.Sync()` directly. Document in ADR. |
| Directory fsync after rename | "Correct crash-safe rotation" | Necessary for crash-safe rename, but the cost is an extra syscall on every rotation. For an EVTX audit writer that rotates once per hour or once per 100 MB, this overhead is negligible and should be included. | Include `syscall.Fsync(dirFd)` inside `rotate()` after `os.Rename()`. This is a correctness requirement, not optional for crash-safe rotation. |

---

## Feature Dependencies

```
[Persistent *os.File handle — structural change]
    required by ──> [Periodic fsync ticker]
    required by ──> [Size-based rotation]
    required by ──> [Time-based rotation]
    required by ──> [fsync before rename in rotate()]
    required by ──> [Graceful shutdown sync]
    required by ──> [SIGHUP rotation]

[rotate() function]
    required by ──> [Size-based rotation]  (calls rotate() when size threshold crossed)
    required by ──> [Time-based rotation]  (calls rotate() on ticker fire)
    required by ──> [SIGHUP rotation]      (calls rotate() on signal)
    contains ──────> [fsync before rename] (called inside rotate())
    triggers ──────> [Count-based retention / cleanOldFiles()]

[Count-based retention]
    depends on ──> [rotate()]  (cleanup runs after each rotation)
    independent of ──> [*os.File handle]  (filesystem glob operation only)

[Config [output] new fields]
    required by ──> [All rotation and flush features]
    must be parsed ──> before ──> NewBinaryEvtxWriter()

[Background goroutine (fsync + time-rotation ticker)]
    started by ──> NewBinaryEvtxWriter() or Start() method
    stopped by ──> Close() via stop channel
    contains ──> [Periodic fsync]
    contains ──> [Time-based rotation]
    contains ──> [SIGHUP rotation]  (differentiator)
```

### Dependency Notes

- **Persistent `*os.File` must be implemented first.** All other features depend on it. Current `os.WriteFile()` approach is incompatible with periodic fsync — it opens, writes, and closes atomically with no hook for intermediate syncs.
- **`rotate()` is the second foundational piece.** Once it exists, size-based, time-based, and SIGHUP rotation are all one-liner calls in their respective trigger paths.
- **Config fields are a prerequisite.** `NewBinaryEvtxWriter()` must accept configuration (flush interval, max size, max count, rotation interval) before any goroutine can be parameterized.
- **Periodic fsync and time-based rotation share a goroutine.** A single `select` over two `time.Ticker` channels avoids goroutine proliferation. The `stop` channel is the third case in the select.
- **Count-based retention is decoupled.** It is a filesystem glob + delete operation triggered inside `rotate()`. It does not touch the file handle and has no external dependencies beyond `os.Remove`.
- **Other writer types (gelf, syslog, beats) are unaffected.** Rotation config fields are only passed to `NewBinaryEvtxWriter()` in the `buildWriter()` `case "evtx"` arm. No changes to other writers.

---

## Definition of Done (Per Feature)

Observable behaviors that confirm each feature is working correctly.

### Foundational: Persistent `*os.File` handle

- [ ] `BinaryEvtxWriter` struct has `file *os.File` field
- [ ] `NewBinaryEvtxWriter()` opens (or creates) the file and writes the EVTX file header immediately
- [ ] `WriteEvent()` appends encoded record bytes directly to `w.file` via `w.file.Write()`; updates `w.currentSize`
- [ ] `Close()` finalizes the EVTX file header (chunk count, last record ID) and closes `w.file`
- [ ] Unit test: create writer, write 10 events, call `Close()`, assert file exists and is parseable by python-evtx

### Periodic fsync (flush_interval_s)

- [ ] Background goroutine fires `w.file.Sync()` every `flush_interval_s` seconds
- [ ] `flush_interval_s = 0` disables the ticker (default is 15, so 0 must be an explicit override)
- [ ] Goroutine stops within one tick after `Close()` is called (stop channel is closed)
- [ ] `Close()` calls `w.file.Sync()` once more before closing the file handle
- [ ] `slog.Debug("binary_evtx_fsynced", "path", w.path)` emitted after each periodic fsync
- [ ] Unit test: write events, advance mock clock past flush interval, assert file bytes on disk match expected

### Size-based rotation (max_file_size_mb)

- [ ] `w.currentSize` tracks total bytes written to current file (file header + all records)
- [ ] Before writing each event: if `w.currentSize + recordLen >= maxFileSizeBytes`, call `w.rotate()`
- [ ] `rotate()`: sync, finalize headers, close, `os.Rename(activePath, backupPath)`, open new file, write header, reset `w.currentSize`
- [ ] Backup filename format: `basename-2006-01-02T15-04-05.evtx` (applied at rotation time using Go `time.Format`)
- [ ] `max_file_size_mb = 0` means no size-based rotation is triggered
- [ ] Unit test: configure `max_file_size_mb = 1`, write events until encoded size > 1 MB, assert timestamped backup exists and active file has a smaller, valid EVTX header

### Count-based retention (max_file_count)

- [ ] After each `rotate()`, `cleanOldFiles()` runs: `filepath.Glob(dir + "/" + base + "-*.evtx")`
- [ ] Files are sorted by modification time (oldest first)
- [ ] Files are deleted (oldest first) until `len(backups) <= max_file_count`
- [ ] The active (non-timestamped) file is never included in the glob match and never deleted
- [ ] `max_file_count = 0` skips `cleanOldFiles()` entirely — all rotated files retained
- [ ] Unit test: configure `max_file_count = 2`, trigger 4 rotations, assert only 2 `.evtx` backup files remain

### Time-based rotation (rotation_interval_h)

- [ ] `time.NewTicker(rotationInterval)` in the background goroutine fires `w.rotate()`
- [ ] `rotation_interval_h = 0` means no time-based rotation ticker is created
- [ ] Rotation fires even when 0 events have been written since last rotation (empty .evtx with file header is valid)
- [ ] Unit test: invoke `rotate()` directly 3 times with short intervals; assert 3 backup files with distinct timestamps exist

### Config `[output]` new fields

- [ ] `OutputConfig` has: `FlushIntervalS int`, `MaxFileSizeMB int`, `MaxFileCount int`, `RotationIntervalH int`
- [ ] TOML tags: `flush_interval_s`, `max_file_size_mb`, `max_file_count`, `rotation_interval_h`
- [ ] `defaultConfig()` sets `FlushIntervalS = 15`, all others `= 0`
- [ ] `config.toml.example` has a commented `[output]` block documenting all four fields; `0` values are explained as "disabled/unlimited"
- [ ] Non-evtx writer types ignore the rotation config fields (no panic, no warning)
- [ ] Unit test: decode a TOML string with all four fields set, assert values propagate to `OutputConfig`

### fsync before rename (rotation correctness)

- [ ] `rotate()` calls `w.file.Sync()` before `w.file.Close()`
- [ ] `rotate()` calls `os.Rename(activePath, backupPath)`
- [ ] After rename, a new `*os.File` is opened at `activePath` and a fresh EVTX file header is written
- [ ] `slog.Info("binary_evtx_rotated", "from", backupPath, "to", activePath)` emitted on each rotation

---

## MVP Definition

### Launch With (v4.0)

Minimum to claim durability + file lifecycle management:

- [ ] Persistent `*os.File` handle (structural change) — foundation for everything
- [ ] Periodic fsync via `flush_interval_s` — the named milestone requirement
- [ ] `rotate()` function — required by three other features
- [ ] Size-based rotation via `max_file_size_mb` — explicit v4.0 target
- [ ] Count-based retention via `max_file_count` — prevents disk exhaustion
- [ ] Time-based rotation via `rotation_interval_h` — explicit v4.0 target
- [ ] Config `[output]` new fields with defaults — all parameters configurable
- [ ] ADRs for flush interval choice and rotation strategy — explicit v4.0 requirement

### Add After Validation (v4.x)

- [ ] SIGHUP-triggered rotation — nearly free once `rotate()` exists; add when ops team requests it
- [ ] Prometheus `cee_last_fsync_unix_seconds` gauge — add when SRE requests fsync alerting

### Future Consideration (v5+)

- [ ] Multi-chunk EVTX support — `flushChunkLocked()` is currently a no-op stub; separate optimization milestone
- [ ] Compression of rotated files — blocked on forensics tool support

---

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| Persistent `*os.File` handle | HIGH (blocks everything) | MEDIUM | P1 — must be first |
| Periodic fsync | HIGH | LOW (after structural change) | P1 |
| `rotate()` function | HIGH (required by 3 features) | MEDIUM | P1 — implement once, use everywhere |
| Size-based rotation | HIGH | LOW (after rotate() exists) | P1 |
| Count-based retention | HIGH | LOW | P1 |
| Time-based rotation | MEDIUM | LOW (after rotate() exists) | P1 |
| Config `[output]` new fields | HIGH | LOW | P1 |
| ADRs | MEDIUM (named requirement) | LOW | P1 |
| SIGHUP rotation | MEDIUM | LOW | P2 |
| Prometheus fsync gauge | LOW | LOW | P3 |

**Priority key:** P1 = must have for v4.0 launch, P2 = add in v4.x, P3 = future consideration

---

## Sources

- [natefinch/lumberjack — behavioral model: size/count/time rotation, rename strategy](https://github.com/natefinch/lumberjack)
- [lumberjack v2 API — MaxSize, MaxBackups, MaxAge, Rotate()](https://pkg.go.dev/gopkg.in/natefinch/lumberjack.v2)
- [Go os.File.Sync() wraps fdatasync on Linux](https://groups.google.com/g/golang-nuts/c/fc3lh8L_5GM)
- [Atomically writing files in Go — rename + fsync directory pattern](https://michael.stapelberg.ch/posts/2017-01-28-golang_atomically_writing/)
- [Durability: Linux File APIs — fsync semantics, why directory fsync is needed after rename](https://www.evanjones.ca/durability-filesystem.html)
- [Files are hard — crash safety, rename atomicity caveats](https://danluu.com/file-consistency/)
- [Ticker-based goroutine patterns with graceful shutdown in Go](https://medium.com/the-bug-shots/synchronising-periodic-tasks-and-graceful-shutdown-with-goroutines-and-tickers-golang-9d50f1aaf097)
- [Implementing Log File Rotation in Go: logrus/zap/slog patterns](https://dev.to/leapcell/implementing-log-file-rotation-in-go-insights-from-logrus-zap-and-slog-5b9o)

---

*Feature research for: cee-exporter v4.0 — periodic fsync and file rotation for BinaryEvtxWriter*
*Researched: 2026-03-04*
