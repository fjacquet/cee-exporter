---
phase: 10-open-handle-incremental-flush
plan: "01"
subsystem: go-evtx
tags: [evtx, binary-format, open-handle, incremental-flush, tdd, bug-fix]
dependency_graph:
  requires: [09-01-SUMMARY.md]
  provides: [multi-chunk-evtx-sessions, open-handle-writer, incremental-disk-flush]
  affects: [cee-exporter/pkg/evtx, go-evtx/evtx.go]
tech_stack:
  added: []
  patterns: [open-handle-model, flush-without-reset, pre-append-capacity-check, tdd-red-green]
key_files:
  created:
    - /Users/fjacquet/Projects/go-evtx/openhandle_test.go
  modified:
    - /Users/fjacquet/Projects/go-evtx/evtx.go
decisions:
  - "Option A flush-without-reset: tickFlushLocked() writes in-progress chunk without incrementing chunkCount — enables Reader visibility without losing buffered records"
  - "Pre-append capacity check in WriteRecord/WriteRaw: flush BEFORE appending new record when it would overflow; recompute BinXML offset for new empty chunk"
  - "os.Remove on empty Close: Writer.Close() removes the placeholder file when no records were written and chunkCount == 0"
  - "chunkCount+1 in tickFlushLocked header patch: reflects the in-progress chunk so Readers see it immediately"
metrics:
  duration_seconds: 345
  completed_date: "2026-03-05"
  tasks_completed: 3
  files_created: 1
  files_modified: 1
---

# Phase 10 Plan 01: Open-Handle Incremental Flush Summary

Open-handle multi-chunk EVTX Writer: replaced the single-chunk checkpoint-write model with a file-handle-held-open design that flushes complete 65536-byte chunks incrementally via f.WriteAt, fixing the EVTX-01 silent event drop bug.

## What Was Built

### Struct Changes (evtx.go)

Two new fields added to `Writer`:
- `f *os.File` — open file handle, created in `New()`, closed in `Close()`
- `chunkCount uint16` — tracks number of complete chunks written to disk

### New Methods

**`flushChunkLocked()`** (called under `w.mu`):
- Builds a padded 65536-byte EVTX chunk from `w.records`
- Computes CRCs: `patchEventRecordsCRC` + `patchChunkCRC`
- Writes chunk via `w.f.WriteAt(chunkBytes, evtxFileHeaderSize + chunkCount * evtxChunkSize)`
- Increments `w.chunkCount`, patches file header at offset 0
- Calls `w.f.Sync()`, resets `w.records = w.records[:0]` and `w.firstID = w.recordID`

**`tickFlushLocked()`** (called under `w.mu` by `backgroundLoop`):
- Flush-without-reset: writes the current partial chunk at slot `w.chunkCount` WITHOUT incrementing `w.chunkCount` or resetting `w.records`
- Patches file header with `chunkCount+1` to make the in-progress chunk visible to Readers
- Calls `w.f.Sync()`

### Updated Methods

**`New()`**: Opens file immediately with `os.OpenFile(path, O_RDWR|O_CREATE|O_TRUNC, 0o644)`, writes 4096-byte placeholder header `buildFileHeader(0, 1)`.

**`WriteRecord()` / `WriteRaw()`**: Pre-append capacity check — if adding the new record would exceed `evtxChunkSize - evtxRecordsStart` (65024 bytes), `flushChunkLocked()` is called BEFORE appending. For `WriteRecord`, the BinXML chunk offset is recomputed for the new empty chunk.

**`backgroundLoop()`**: Changed from `flushToFile()` to `tickFlushLocked()`.

**`Close()`**: Ordering: `close(done)` → `wg.Wait()` → `mu.Lock()` → `defer f.Close()`. If `len(w.records) == 0 && w.chunkCount == 0`: calls `os.Remove(w.path)` and returns nil (empty session). Otherwise calls `flushChunkLocked()` for the final partial chunk.

### Removed Methods

**`flushToFile()`**: Completely removed. Replaced by `flushChunkLocked()` + `tickFlushLocked()`.

## Test Counts

| Category | Tests | Status |
|---|---|---|
| New (openhandle_test.go) | 5 | PASS |
| Goroutine lifecycle (goroutine_test.go) | 7 | PASS |
| Integration (evtx_test.go) | 8 | PASS |
| Binary format (binformat_test.go) | 5 | PASS |
| Reader (reader_test.go) | 7 | PASS |
| **Total** | **32** | **All PASS** |

## Race Detector Status

`go test -race ./... -count=1` in `/Users/fjacquet/Projects/go-evtx`: **zero data races**.

`make test` in `/Users/fjacquet/Projects/cee-exporter`: **all 9 packages pass**.

## Key Implementation Decisions

### Option A: Flush-without-reset for tickFlushLocked

`tickFlushLocked()` writes the in-progress chunk to slot `w.chunkCount` (same slot as the next `flushChunkLocked` call would use) WITHOUT incrementing `w.chunkCount` or clearing `w.records`. This means:
- The goroutine tick makes partial progress visible on disk
- The next `flushChunkLocked` call overwrites the same slot with a complete (possibly larger) version
- No data is lost, no duplicate chunk indices are created

### Pre-append capacity check (fixes overflow)

Initial implementation checked AFTER appending: `if len(w.records) >= threshold { flush }`. This caused overflow bytes to be silently dropped on flush (the clamping in `flushChunkLocked` discarded overflow). The fix: check BEFORE appending. If the new record would overflow, flush the current full chunk first, then append the record to the freshly-reset buffer. For `WriteRecord`, the BinXML offset (`binXMLChunkOffset`) must be recomputed for the new empty chunk position.

### os.Remove on empty Close

`New()` creates the file immediately (placeholder header). `Close()` with zero records and `chunkCount == 0` removes it. This preserves backward compatibility with `TestWriter_EmptyClose` in `evtx_test.go`.

### chunkCount+1 in header patch (tickFlushLocked)

The file header's `ChunkCount` field tells Readers how many chunks to load. When `tickFlushLocked` writes an in-progress chunk at slot `w.chunkCount`, it patches the header with `chunkCount+1` so Readers discover it. The actual `w.chunkCount` field is not incremented until `flushChunkLocked` confirms a complete chunk.

## Files Modified

| File | Lines Before | Lines After | Delta |
|---|---|---|---|
| `/Users/fjacquet/Projects/go-evtx/evtx.go` | ~215 | ~330 | +115 |
| `/Users/fjacquet/Projects/go-evtx/openhandle_test.go` | (new) | 275 | +275 |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Overflow bytes discarded on chunk flush**
- **Found during:** Task 2 (GREEN implementation)
- **Issue:** Initial WriteRecord/WriteRaw used post-append threshold check. When records overflowed the 65024-byte chunk limit, `flushChunkLocked` clamped to `maxRecords` bytes and then reset `w.records = w.records[:0]`, silently discarding the overflow bytes (the partial record that triggered the flush).
- **Fix:** Moved the threshold check to PRE-append: if adding the new record would overflow, flush first (on the currently full buffer), then append to the freshly-reset empty chunk. For `WriteRecord`, `binXMLChunkOffset` is recomputed for the new empty buffer position.
- **Files modified:** `/Users/fjacquet/Projects/go-evtx/evtx.go` (WriteRecord, WriteRaw)
- **Test that caught it:** `TestWriter_MultiChunk_EventCount` (read 35, expected 3000) and `TestWriter_OpenHandle_NoRace` (read 35, expected 500)

## Self-Check: PASSED

- openhandle_test.go: FOUND at /Users/fjacquet/Projects/go-evtx/openhandle_test.go
- evtx.go: FOUND at /Users/fjacquet/Projects/go-evtx/evtx.go
- Commit bb61191 (RED tests): FOUND
- Commit a3f4cce (GREEN implementation): FOUND
- Commit 4c8f0c6 (race-clean verification): FOUND
- go test ./... -count=1: PASSED (32 tests, all green)
- go test -race ./... -count=1: PASSED (zero data races)
- make test (cee-exporter): PASSED (all 9 packages)
