# Pitfalls Research

**Domain:** Go daemon — v4.0 Industrialisation (fsync + file rotation for BinaryEvtxWriter)
**Project:** cee-exporter
**Researched:** 2026-03-04
**Confidence:** HIGH (code-inspection-driven for concurrent/format pitfalls; MEDIUM for OS-level fsync/rename edge cases)

---

## Critical Pitfalls

Mistakes that cause data corruption, silent event loss, or rewrite of the rotation implementation.

---

### Pitfall 1: Write/Rotate Race — Lock Scope Mismatch

**What goes wrong:**
A `Rotate()` method is added that acquires `w.mu`, closes the current file, and opens a new one. Concurrently, `WriteEvent` also acquires `w.mu` — so far correct. The race happens when the fsync ticker goroutine calls a helper that attempts to acquire `w.mu` **after** `Rotate()` has already taken it, or — worse — when `Rotate()` is called from **outside** the lock boundary, e.g. from a `select` in the ticker goroutine before calling a `flushLocked()` method that re-acquires the lock.

Symptoms: `sync: unlock of unlocked mutex`, data written to a file that was already renamed/closed, or partial binary payload split across two `.evtx` files (corrupting both chunk CRCs).

**Why it happens:**
`BinaryEvtxWriter` already uses a single `sync.Mutex` (`w.mu`) to guard its in-memory buffer (`w.records`). Developers add `Flush()` and `Rotate()` as public methods and call them from a separate ticker goroutine without recognising that both methods must complete their entire critical section — including the file `Write`/`Sync`/`Rename` calls — under the same lock held for the duration. Splitting "decide to rotate" from "execute rotate" across two lock acquisitions creates a TOCTOU window.

**How to avoid:**
- Define a single `doFlushLocked()` internal function that performs all I/O while the caller holds `w.mu`. Never call `w.mu.Lock()` inside it.
- The ticker goroutine calls: `w.mu.Lock(); w.doFlushLocked(); w.mu.Unlock()` — one uninterrupted critical section.
- `Rotate()` calls: `w.mu.Lock(); w.doRotateLocked(); w.mu.Unlock()` — same pattern.
- No exported method that performs I/O may call another exported method (avoids mutex re-entrant deadlock).
- Test with `-race` enabled: `go test -race ./pkg/evtx/` (CGO must be on for race detector — run in a separate CI step, not via `make test`).

**Warning signs:**
- `go vet` or `-race` reports a data race on `w.records`, `w.file`, or `w.recordID`.
- Event log contains a record whose `EventRecordID` resets to 1 mid-file (indicates a flush happened on a wrong file handle).
- python-evtx throws a CRC error on a rotated file (partial records span across the chunk boundary).

**Phase to address:** Phase 1 — fsync/flush goroutine scaffolding. The lock architecture must be right before any I/O is wired in.

---

### Pitfall 2: EVTX File Header Not Updated on Each Flush

**What goes wrong:**
`flushToFile()` currently calls `os.WriteFile(w.path, fileBytes, 0o644)` — a full rewrite. When rotation is added, a file is kept open across multiple flush cycles. The `buildFileHeader()` function writes `ChunkCount`, `LastChunkNumber`, and `NextRecordIdentifier` at construction time. If `flushLocked()` appends new chunk data to an open file handle without rewriting the file header (bytes 0–4095), the file header's `NextRecordIdentifier` and `ChunkCount` fields go stale. Forensics parsers (python-evtx, Velociraptor) rely on `NextRecordIdentifier` to know the expected record sequence — stale values cause parsers to emit "unexpected record ID" warnings or skip the file entirely.

**Why it happens:**
The current single-shot `os.WriteFile` rewrite avoids the "seek back to offset 0" pattern entirely. When transitioning to an incremental append model (open file handle, append chunks, periodic flush), developers forget to `f.Seek(0, 0)` and rewrite the 4096-byte file header after each chunk is finalised.

**How to avoid:**
- On every successful chunk flush: (1) seek to offset 0, (2) write `buildFileHeader(chunkCount, w.recordID)`, (3) fsync, (4) seek to end before next append.
- Alternatively: use the existing full-rewrite approach for the initial prototype (always `os.WriteFile` the full in-memory buffer) and introduce incremental append only in a dedicated refactor phase.
- Add a test that opens the written file with `os.Open`, reads bytes 24–32 (`NextRecordIdentifier`), and asserts it equals `w.recordID`.

**Warning signs:**
- python-evtx parses a rotated `.evtx` file and reports a record count that does not match the chunk's `LastEventRecordNumber`.
- `evtx_binformat_test.go` CRC tests pass on individual chunks but file-level tools report the file as corrupt.

**Phase to address:** Phase 2 — incremental flush and open file handle management.

---

### Pitfall 3: Goroutine Leak — ticker.Stop() Without done Channel

**What goes wrong:**
A flush ticker goroutine is started in `NewBinaryEvtxWriter` (or in a separate `Start()` method) with the pattern:

```go
go func() {
    for range ticker.C {
        w.mu.Lock()
        w.doFlushLocked()
        w.mu.Unlock()
    }
}()
```

When `Close()` is called, it calls `ticker.Stop()` but does **not** close or signal a `done` channel. The goroutine is now permanently blocked on `ticker.C` (which will never receive after `Stop()`). The goroutine is leaked for the lifetime of the process. In tests, leaked goroutines accumulate across test runs, and `-race` may report a false positive write on `w.records` after the test's `Close()` returned.

**Why it happens:**
`ticker.Stop()` documentation states: "Stop does not close the channel, to prevent a read from the channel succeeding incorrectly." Developers assume `Stop()` terminates the goroutine — it does not. This is the #1 goroutine leak in Go ticker-based code (tracked in `golang/go` issue #68483 as a potential `go vet` check).

**How to avoid:**
```go
type BinaryEvtxWriter struct {
    // ...
    done chan struct{}  // closed by Close()
    wg   sync.WaitGroup
}

// In constructor:
w.done = make(chan struct{})
w.wg.Add(1)
go func() {
    defer w.wg.Done()
    for {
        select {
        case <-ticker.C:
            w.mu.Lock()
            _ = w.doFlushLocked()
            w.mu.Unlock()
        case <-w.done:
            return
        }
    }
}()

// In Close():
ticker.Stop()
close(w.done)   // unblocks the goroutine
w.wg.Wait()     // wait for goroutine to exit before final flush
w.mu.Lock()
_ = w.doFlushLocked()
w.mu.Unlock()
```

**Warning signs:**
- `go test` output includes `goroutine NNN [chan receive]:` stack traces pointing at the ticker loop after test completion.
- pprof goroutine profile shows accumulating blocked goroutines named `BinaryEvtxWriter`.
- Leaked goroutine holds `w.mu` after `Close()` returns, causing a deadlock in the next test that creates a new writer.

**Phase to address:** Phase 1 — fsync/flush goroutine scaffolding. The `done` channel is foundational; adding it later is a refactor.

---

### Pitfall 4: Chunk CRC Computed Before Records Are Complete

**What goes wrong:**
`patchEventRecordsCRC` and `patchChunkCRC` are called in `flushToFile()` after all records are appended to the in-memory `chunkBytes` slice. When rotation is introduced, a developer may call `patchChunkCRC` after each `WriteEvent` call (to keep the file "always valid") and then append more records. The CRC no longer covers the full records region, and the resulting file fails CRC validation by any forensics parser.

**Why it happens:**
The EVTX chunk CRC covers the **entire** records region (`evtxRecordsStart` to `FreeSpaceOffset`). Patching it incrementally and then continuing to write records invalidates the prior CRC. The spec does not support "running CRC" updates because the final free-space offset is not known until the chunk is closed.

**How to avoid:**
- CRC patching (`patchEventRecordsCRC` + `patchChunkCRC`) must occur **exactly once per chunk**, immediately before the chunk bytes are written to disk. Never call CRC patching before appending the final record.
- If "always valid on disk" is a hard requirement, implement a full-chunk-rewrite on every flush (acceptable for chunks ≤ 65 KB). Write to a temp file, fsync, then `os.Rename` to the active path — this ensures readers never see a partial CRC state.
- File header CRC (`buildFileHeader` CRC32 over bytes 0–120) must also be recomputed after any file header update.

**Warning signs:**
- python-evtx raises `InvalidChunkException` or `ChunkChecksumMismatch` on a file that was readable before.
- A test that writes one event, flushes, writes another, flushes, and then parses with python-evtx fails on the second flush.

**Phase to address:** Phase 2 — incremental flush. Any flush that does not close the chunk must skip CRC patching; only chunk-close flushes must patch.

---

### Pitfall 5: File Naming Collision on Sub-Second Rotation

**What goes wrong:**
Rotation renames the active file to a timestamped archive name such as `cee-exporter-20260304-150405.evtx`. If two rotation events fire within the same wall-clock second (e.g. size-based and time-based triggers both fire, or a test calls `Rotate()` twice in quick succession), both renames target the same destination path. The second `os.Rename` silently overwrites the first archive, destroying the events from the first rotation.

**Why it happens:**
`time.Now().Format("20060102-150405")` has one-second resolution. Under high load, the size-based trigger may fire multiple times per second. In tests, mocked clocks make this trivial to trigger.

**How to avoid:**
- Use a monotonic sequence number in the rotated filename: `cee-exporter-20260304-150405-00001.evtx`. Maintain `w.rotationSeq uint64` (atomic or under `w.mu`) and increment on each rotation.
- Check for destination existence before rename; if the target exists, increment sequence and retry (at most once — two collisions per second is a logic bug, not a naming concern).
- Do not use nanosecond timestamps — they look unique but are not guaranteed to be when the clock resolution is coarser than nanosecond on the host OS.

**Warning signs:**
- `ls -la` of the rotation directory shows fewer archive files than expected after a burst test.
- Integration test that triggers rotation twice asserts two archive files but finds only one.

**Phase to address:** Phase 3 — file rotation (size/count/time). The naming scheme must be defined before any rotation logic is implemented.

---

### Pitfall 6: os.Rename Not Atomic on Windows

**What goes wrong:**
`os.Rename(oldpath, newpath)` is POSIX-atomic on Linux (the kernel guarantees the inode swap). On Windows, `os.Rename` calls `MoveFileEx` which **fails** if `newpath` already exists and requires `MOVEFILE_REPLACE_EXISTING` to replace. Go's `os.Rename` on Windows does not set this flag, so renaming the active `.evtx` file to an archive name fails with `os.ErrExist` if an archive by that name already exists. Even when it succeeds, the operation is **not** atomic — a reader may observe the file half-renamed.

**Why it happens:**
Developers test rotation on Linux where `os.Rename` is atomic, then discover failures on Windows deployment. The `cee-exporter` binary runs on both platforms (`//go:build !windows` vs `//go:build windows` split).

**How to avoid:**
- On Windows, use `golang.org/x/sys/windows.MoveFileEx(oldPtr, newPtr, windows.MOVEFILE_REPLACE_EXISTING)` for atomic rename.
- Encapsulate in a `renameFile(src, dst string) error` platform-specific helper using build-tag files (`rename_windows.go` / `rename_notwindows.go`).
- On Linux, fsync the **directory** containing the file after rename to ensure durability: open the parent directory, call `f.Sync()`, close it. Without this, a crash between `rename` and directory sync may leave the old name visible.

**Warning signs:**
- Rotation tests pass on Linux CI but fail on Windows with `The file exists` error in `os.Rename`.
- A forensics analyst reports that an `.evtx` archive from a Windows deployment is truncated (mid-rename crash on a slow disk).

**Phase to address:** Phase 3 — file rotation. Must be addressed before Windows testing.

---

### Pitfall 7: Config Zero-Value Ambiguity — 0 = Unlimited vs 0 = Disabled

**What goes wrong:**
The v4.0 spec defines three rotation triggers:

- `max_file_size_mb = 0` → unlimited (no size rotation)
- `max_file_count = 0` → unlimited (keep all rotated files)
- `rotation_interval_h = 0` → disabled (no time-based rotation)
- `flush_interval_s = 0` → **ambiguous** — does 0 mean "flush on every write" (maximum durability) or "no periodic flush" (default behaviour)?

If `flush_interval_s = 0` is interpreted as "flush every write", an operator who omits the field gets a `time.NewTicker(0)` call, which panics in Go: `ticker: non-positive interval for NewTicker`.

**Why it happens:**
Go's zero value for `int` and `uint` is 0. TOML unmarshalling (via `BurntSushi/toml`) leaves unset fields at zero. Without a documented and validated semantics for zero in each field, the implementation must guess, and the guess often differs from operator expectation.

**How to avoid:**
- Document zero-value semantics explicitly in `config.toml.example` with a comment for every rotation field.
- Add a `validateConfig()` function that rejects `flush_interval_s = 0` (must be ≥ 1) or maps it to a default (e.g. 15).
- `time.NewTicker` panics on zero or negative duration — always gate with `if cfg.FlushIntervalS > 0`.
- For rotation fields, treat 0 as "disabled/unlimited" consistently; document this explicitly. A negative value is always an error.
- Write a unit test for `validateConfig` that asserts every out-of-range input returns an error.

**Warning signs:**
- Daemon panics at startup with `ticker: non-positive interval for NewTicker` on a host where `flush_interval_s` is absent from `config.toml`.
- Operator sets `max_file_count = 0` expecting "keep 0 files" (delete all) but gets "unlimited retention" instead.

**Phase to address:** Phase 4 — config section addition. Config validation must be written and tested before any rotation feature uses the config values.

---

### Pitfall 8: flushChunkLocked Is a No-Op Stub — Silently Drops Events at Chunk Boundary

**What goes wrong:**
`flushChunkLocked()` in the current codebase (line 154–160 of `writer_evtx_notwindows.go`) is a placeholder that logs a warning and returns `nil` without writing anything. At the `chunkFlushThreshold` (60,000 bytes), `WriteEvent` calls `flushChunkLocked()` and silently continues buffering into the same already-oversized buffer. When rotation is added, this stub is not replaced, and multi-chunk `.evtx` files never materialise — the warning is emitted but the event is retained in the buffer alongside all previous events, eventually producing a truncated chunk when `Close()` calls `flushToFile()`.

**Why it happens:**
The stub was explicitly noted as a placeholder for multi-chunk support. Under deadline pressure it is easy to wire up fsync and rotation without noticing the stub is still in place. The stub returns `nil` (not an error), so no caller detects the omission.

**How to avoid:**
- Replace `flushChunkLocked()` with a real implementation as part of Phase 2 (incremental flush). Multi-chunk support is required for rotation to work correctly on high-volume deployments.
- Add a test: write events until `len(w.records) > chunkFlushThreshold` and assert that the output file contains two valid EVTX chunks, each with a valid CRC.
- Change the stub to return a non-nil sentinel error (`ErrMultiChunkNotImplemented`) so callers can surface it rather than silently continuing.

**Warning signs:**
- Daemon logs `binary_evtx_chunk_boundary_reached` WARN but no new file is produced.
- The `.evtx` file produced for a session with more than ~2,400 events is truncated at `maxRecords` in `flushToFile()` (the truncation WARN is logged but easy to miss).

**Phase to address:** Phase 2 — incremental flush must implement real multi-chunk flushing before rotation is added.

---

### Pitfall 9: fsync on File Descriptor After os.WriteFile

**What goes wrong:**
`os.WriteFile` opens, writes, and closes the file. The `Close()` call flushes the kernel page cache to the file system buffer, but on most Linux file systems this does not guarantee the data has reached non-volatile storage. A hard reboot or kernel panic between `os.WriteFile` return and the next physical write cycle can silently truncate the file. The guarantee required is: after `flush_interval_s` elapses and `doFlushLocked()` returns, events must be durable.

**Why it happens:**
Developers equate `os.File.Close()` with fsync. `Close()` ensures file descriptor resources are freed; it does not issue `fsync(2)`. The distinction is invisible in normal operation and only manifests after a crash.

**How to avoid:**
- When using an open file handle (incremental flush model): call `f.Sync()` after each flush, before releasing the lock.
- When using the full-rewrite model: write to a temp file (`os.CreateTemp`), call `f.Sync()`, close, then `os.Rename` to the final path. This provides both atomicity and durability.
- fsync the containing directory after rename on Linux (open the directory, `f.Sync()`, close) to ensure the directory entry is durable.
- Note: `os.WriteFile` alone provides **no** fsync guarantee. A test that calls `os.WriteFile` followed immediately by `os.ReadFile` will always succeed — durability failures only appear under crash conditions.

**Warning signs:**
- After a simulated crash (`kill -9` of the daemon), the `.evtx` file is 0 bytes or contains only the file header with no chunks.
- A staging server with a power-loss test produces corrupt EVTX files that are unparseable.

**Phase to address:** Phase 1 — the fsync semantics must be decided (full-rewrite + rename, or incremental + sync) before writing any flush code.

---

## Technical Debt Patterns

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Full-rewrite on every flush (`os.WriteFile`) | Simple, always-valid file on disk | Re-serialises entire in-memory buffer (up to 65 KB) on every flush tick | Acceptable for MVP given 65 KB max chunk size; revisit only if profiling shows I/O bottleneck |
| Single-chunk EVTX files only | Avoids multi-chunk header bookkeeping | Events silently truncated at `chunkFlushThreshold`; rotation over multi-MB files impossible | Never: the stub must be replaced before v4.0 ships |
| Timestamp-only rotation filenames | Simple implementation | Silent archive collision at high event rates | Never: always include a sequence number |
| No config validation for zero values | Fewer lines of code | Panic at startup on misconfigured hosts | Never: ticker panics are fatal and hard to diagnose |
| Calling `Rotate()` from the ticker goroutine without lock | Avoids coordinating two goroutines | TOCTOU window between size check and actual rotate | Never: rotation must happen under `w.mu` |

---

## Integration Gotchas

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| Queue → BinaryEvtxWriter | Calling `WriteEvent` from multiple queue workers while a flush ticker fires from a separate goroutine | All paths that touch `w.records`, `w.file`, or `w.recordID` must hold `w.mu` for the complete operation |
| os.Rename on Windows | `os.Rename` fails with `ERROR_ALREADY_EXISTS` when target exists | Use `windows.MoveFileEx` with `MOVEFILE_REPLACE_EXISTING` in a `_windows.go` build-tag file |
| TOML config unmarshalling | Zero value fields silently left at `0`, causing ticker panic | Call `validateConfig()` after unmarshalling; reject or substitute defaults for `flush_interval_s = 0` |
| python-evtx / forensics parsers | Parsers validate CRC32 of chunk headers and record regions strictly | Do not write partial chunks; only call `patchChunkCRC` and `patchEventRecordsCRC` once per closed chunk |
| Multi-writer fan-out | `MultiWriter` calls `WriteEvent` on each child including `BinaryEvtxWriter`; ticker goroutine also flushes the same writer | The writer's internal lock must be the only synchronisation primitive — no external locking by `MultiWriter` |

---

## Performance Traps

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| fsync on every WriteEvent | Write latency spikes to 10–100 ms per event; queue fills and events are dropped | fsync only at flush_interval_s tick, not per-event | At ~100 events/s, individual fsync makes the writer the bottleneck |
| Full in-memory buffer rewrite at each flush | Memory allocation spike proportional to event volume during flush | Cap flush at one chunk (65 KB); start a new chunk for new events after flush | Not a significant concern for ≤ 65 KB chunks; becomes visible at > 1,000 events/flush |
| Rotation scanning all files to enforce max_file_count | `filepath.Glob` over large directory takes > 1 ms, blocks under `w.mu` | Cache rotated file list; only re-scan on startup and after each rotation | At > 10,000 rotated files; unlikely in practice |

---

## Security Mistakes

| Mistake | Risk | Prevention |
|---------|------|------------|
| Rotated archives world-readable (0o644) | EVTX files contain user SIDs, object paths, and access masks — PII | Write new files with `0o640` or `0o600`; create parent directory with `0o750` |
| Temp files left on failed rename | `.evtx.tmp` files accumulate, consuming disk and leaking partial event data | Use `defer os.Remove(tmpPath)` before rename; clean up orphaned `.tmp` files on startup |
| No disk-full detection before write | Daemon silently drops events when disk fills | Check `os.WriteFile` error; emit a `slog.Error` + increment `metrics.M.WriterErrorsTotal`; expose disk-full state in `/health` |

---

## "Looks Done But Isn't" Checklist

- [ ] **fsync implemented:** `f.Sync()` is called (not just `f.Close()`) — verify with a crash test or by checking `strace -e fsync` output
- [ ] **Goroutine cleanup:** `wg.Wait()` in `Close()` confirms the ticker goroutine has exited before the final flush — verify by checking pprof after `Close()` returns
- [ ] **Chunk CRC correct after multi-flush:** Run python-evtx on a file produced by two successive flush ticks — if it parses cleanly, the CRC is correct
- [ ] **File header updated:** `NextRecordIdentifier` in bytes 24–32 of the file header equals the writer's `w.recordID` — verify with a hex dump after flush
- [ ] **Config validation tested:** Unit test covers `flush_interval_s = 0`, negative values, and values > 3600 — confirm daemon rejects them at startup
- [ ] **Rotation sequence number increments:** Two rotations within one second produce two distinct archive files — verify with a fast test
- [ ] **flushChunkLocked stub replaced:** `chunkFlushThreshold` is exercised in a test that asserts two chunks in the output — not just a logged warning
- [ ] **Windows rename works:** Rotation test runs on Windows (or is gated by build tag) and produces a valid renamed archive

---

## Recovery Strategies

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Write/rotate race corrupts a chunk | HIGH | Stop daemon, rename corrupt file to `.corrupt`, parse remaining events from intact chunks with python-evtx, restart daemon with fixed locking |
| Goroutine leak in production | MEDIUM | Restart daemon; add goroutine count to `/health` endpoint and alert on growth |
| Stale file header after crash | MEDIUM | Re-open file, seek to offset 0, rewrite file header with correct `ChunkCount` and `NextRecordIdentifier`; re-patch file header CRC |
| File naming collision overwrites archive | HIGH | Archives cannot be recovered once overwritten; add sequence numbers prospectively — there is no retroactive fix |
| ticker panic from zero flush_interval | LOW | Daemon exits immediately with clear panic message; operator adds `flush_interval_s = 15` to config.toml and restarts |
| flushChunkLocked stub drops events | HIGH | Events buffered beyond `chunkFlushThreshold` are permanently lost; implement real multi-chunk flushing and deploy patched binary |

---

## Pitfall-to-Phase Mapping

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Write/rotate race (Pitfall 1) | Phase 1: flush goroutine scaffolding | `-race` test, lock-holding audit of all I/O paths |
| Stale EVTX file header (Pitfall 2) | Phase 2: incremental flush | Hex dump test — assert `NextRecordIdentifier` at offset 24 |
| Goroutine leak in ticker (Pitfall 3) | Phase 1: flush goroutine scaffolding | pprof goroutine profile after `Close()`; goroutine count = pre-Start count |
| CRC computed before records complete (Pitfall 4) | Phase 2: incremental flush | python-evtx parses two-flush session file without CRC error |
| Rotation filename collision (Pitfall 5) | Phase 3: file rotation | Test: two `Rotate()` calls within 1 ms — assert two distinct archive files |
| os.Rename not atomic on Windows (Pitfall 6) | Phase 3: file rotation | Windows CI test; build-tag helper for platform rename |
| Config zero-value ambiguity (Pitfall 7) | Phase 4: [output] config section | Unit test: `flush_interval_s = 0` returns validation error at startup |
| flushChunkLocked stub (Pitfall 8) | Phase 2: incremental flush | Test: write > `chunkFlushThreshold` bytes; assert two valid chunks in output |
| fsync missing after WriteFile (Pitfall 9) | Phase 1: flush goroutine scaffolding | Decide full-rewrite-with-rename vs. open-handle-with-sync; document in ADR |

---

## Sources

- libevtx EVTX binary format documentation — https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc
- EVTX integrity and CRC fields — https://notes.qazeer.io/dfir/windows/ttps_analysis/evtx_integrity
- Go time.Ticker goroutine leak proposal — https://github.com/golang/go/issues/68483
- Atomically writing files in Go — https://michael.stapelberg.ch/posts/2017-01-28-golang_atomically_writing/
- renameio: fsync on file and directory after rename — https://github.com/google/renameio/issues/11
- os.Rename atomicity on Windows — https://github.com/golang/go/issues/8914
- lumberjack: single-process assumption and rotate/close design — https://github.com/natefinch/lumberjack
- logrotate: filename collision prevention with random hash — https://pkg.go.dev/github.com/easyCZ/logrotate
- Build-your-own database: checksum-protected headers — https://build-your-own.org/database/01_files
- Go ticker and goroutine leak patterns — https://blogtitle.github.io/go-advanced-concurrency-patterns-part-2-timers/
- Code inspection: `/Users/fjacquet/Projects/cee-exporter/pkg/evtx/writer_evtx_notwindows.go` (stubs, lock model, CRC calls)
- Code inspection: `/Users/fjacquet/Projects/cee-exporter/pkg/queue/queue.go` (multi-worker concurrent WriteEvent)

---
*Pitfalls research for: Go BinaryEvtxWriter — v4.0 fsync + file rotation*
*Researched: 2026-03-04*
