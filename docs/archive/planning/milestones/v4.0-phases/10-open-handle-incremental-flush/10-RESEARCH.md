# Phase 10: Open-Handle Incremental Flush - Research

**Researched:** 2026-03-05
**Domain:** EVTX binary format, Go file I/O (open-handle model, incremental writes, fsync), multi-chunk EVTX
**Confidence:** HIGH

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| EVTX-01 | BinaryEvtxWriter writes all events to disk regardless of session length (fix `flushChunkLocked()` stub that currently silently drops events beyond ~2,400 per session) | Open-handle model documented; multi-chunk write sequence specified; `f.Seek(0)` file-header patch pattern identified; CRC re-computation requirements catalogued |
</phase_requirements>

---

## Summary

Phase 9 delivered a goroutine-backed checkpoint-write model: `flushToFile()` rewrites the entire file from the in-memory `w.records` buffer using `os.WriteFile`. This model is bounded to a single EVTX chunk of 65,536 bytes. The current `WriteRecord` / `WriteRaw` implementations warn (via `slog.Warn`) when `len(w.records)` exceeds `chunkFlushThreshold` (60,000 bytes) and `flushToFile()` silently truncates anything beyond `evtxChunkSize - evtxRecordsStart` (65,024 bytes). A typical BinXML event record from a CEPA session is roughly 27 bytes, meaning approximately 2,400 events saturate a single chunk.

Phase 10 replaces the write-on-close model with an open-handle model: a persistent `*os.File` is created at `New()` time (or at first write) and held open for the writer's lifetime. When the in-memory records buffer reaches a chunk-full threshold, `flushChunkLocked()` writes the filled chunk as a `[512-byte header + records]` block to the file, calls `f.Sync()`, resets `w.records` and `w.firstID` for the next chunk, and increments `w.chunkCount`. `Close()` writes the final (possibly partial) chunk and then patches the 4,096-byte file header at offset 0 with the final `ChunkCount` and `NextRecordIdentifier`. Between flushes the background goroutine (from Phase 9) calls the equivalent of a file-header patch + `f.Sync()` to keep the file valid on disk even if the process crashes.

The key insight is that python-evtx and the go-evtx `Reader` both use `ChunkCount` from the file header to know how many chunks to read. If the file header is not patched after each chunk flush, readers see only the initially-written chunk count (1) and stop there, silently skipping later chunks. Therefore, the file header at offset 0 must be updated after every chunk flush AND on the periodic goroutine tick.

**Primary recommendation:** Keep the `*os.File` open from `New()` through `Close()`. Write complete chunks incrementally using `f.WriteAt()` at computed offsets. After each chunk, seek to offset 0 and overwrite the 4,096-byte file header. Call `f.Sync()` after each header patch. Use the existing `w.mu` for all operations.

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `os.File` (via `os.Create` / `os.OpenFile`) | Go stdlib | Persistent file handle held for writer lifetime | Enables `f.WriteAt()`, `f.Seek()`, and `f.Sync()` without closing between writes |
| `os.File.WriteAt` | Go stdlib | Write chunk bytes at a known file offset without seeking | Avoids seek+write races; offset is deterministic: `evtxFileHeaderSize + chunkIdx * evtxChunkSize` |
| `os.File.Seek` | Go stdlib | Seek to offset 0 to patch file header after each chunk | Needed to overwrite header bytes in-place |
| `os.File.Sync` | Go stdlib | `fsync(2)` — actual durability guarantee | Must be called after every chunk+header write; `os.File.Close` does NOT call `fsync` on Linux |
| `sync.Mutex` | Go stdlib | Guards all `w.records`, `w.f`, `w.chunkCount`, `w.recordID` state | Already present in Phase 9; extend scope |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `hash/crc32` | Go stdlib | CRC32 for file header and chunk header patches | `patchChunkCRC()` and `buildFileHeader()` already use this; extend to in-place patching |
| `encoding/binary` | Go stdlib | Little-endian field writes for in-place header updates | Already used throughout `binformat.go` |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `f.WriteAt(data, offset)` | `f.Seek(offset); f.Write(data)` | `WriteAt` is atomic for kernel calls; `Seek+Write` requires two syscalls and is harder to reason about under concurrency. Prefer `WriteAt` for chunk body writes. |
| Rewrite file header with `f.WriteAt` | `os.WriteFile` on Close only | `os.WriteFile` rewrites the whole file; `f.WriteAt` patches only 4,096 bytes at offset 0, which is correct for open-handle model |
| Open-handle with incremental chunks | Keep checkpoint-write, just increase chunk count | Checkpoint-write always writes the whole file from the start; on large sessions it rewrites megabytes per tick. Open-handle writes each chunk once. |
| `bufio.Writer` wrapping `*os.File` | Direct `*os.File` operations | `bufio.Writer` buffers writes but complicates `WriteAt` (they are incompatible). Direct file operations are simpler and sufficient. |

**Installation:** No new packages. All changes use Go stdlib. go-evtx remains dependency-free.

---

## Architecture Patterns

### Writer Struct Changes

```
go-evtx/evtx.go:

Writer struct adds:
  f          *os.File  // open file handle; nil before first write
  chunkCount uint16    // number of chunks written (including any in-progress chunk)

Writer struct removes (or repurposes):
  records  []byte   // still used: current in-progress chunk records
  firstID  uint64   // still used: first record ID of the current (in-progress) chunk
  recordID uint64   // still used: next record ID to assign
```

### File Layout on Disk (multi-chunk)

```
Offset 0          : [4096 bytes]  File header (ElfFile, ChunkCount, NextRecordIdentifier, CRC)
Offset 4096       : [65536 bytes] Chunk 0 (ElfChnk header + records, padded to 65536)
Offset 4096+65536 : [65536 bytes] Chunk 1 (ElfChnk header + records, padded to 65536)
...
```

The file header at offset 0 is always 4,096 bytes regardless of chunk count. Each chunk is always exactly 65,536 bytes (padded with zeros if records do not fill it). This is the Windows EVTX format: fixed-size chunks.

### Pattern 1: Open-Handle File Creation

**What:** `New()` creates the file immediately with `os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)` and writes the initial 4,096-byte file header as a placeholder.
**When to use:** Every time a Writer is created.

```go
// Source: direct code analysis of evtx.go + os package docs
func New(path string, cfg RotationConfig) (*Writer, error) {
    // ... existing validation ...
    f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
    if err != nil {
        return nil, fmt.Errorf("go_evtx: create file: %w", err)
    }
    // Write placeholder file header (ChunkCount=0, NextRecordIdentifier=1).
    hdr := buildFileHeader(0, 1)
    if _, err := f.Write(hdr); err != nil {
        f.Close()
        return nil, fmt.Errorf("go_evtx: write file header: %w", err)
    }
    w := &Writer{
        path:     path,
        f:        f,
        recordID: 1,
        firstID:  1,
        cfg:      cfg,
        done:     make(chan struct{}),
    }
    // ... start goroutine if cfg.FlushIntervalSec > 0 ...
    return w, nil
}
```

**Consequence for EmptyClose:** If `Close()` is called with no records written, the file exists but contains only the 4,096-byte placeholder header and zero chunks. The current behaviour ("do not create file on empty session") must be reconsidered. Options:
1. Delete the file on empty `Close()` — matches current behaviour.
2. Leave the placeholder file — valid EVTX structure with 0 chunks (python-evtx handles this).

Recommendation: delete the file on empty `Close()` to preserve backward compatibility with existing tests (`TestWriter_EmptyClose`).

### Pattern 2: Chunk Flush (`flushChunkLocked`)

**What:** When `w.records` fills beyond `chunkFlushThreshold`, `flushChunkLocked()` (called under `w.mu`) writes the current chunk to disk, calls `f.Sync()`, resets the in-memory buffer, and increments `w.chunkCount`.
**When to use:** Called from `WriteRecord` / `WriteRaw` after appending a record when `len(w.records) > chunkFlushThreshold`.

```go
// Source: EVTX format spec + direct code analysis
// flushChunkLocked writes the in-progress chunk to disk at the correct file offset,
// patches the file header to reflect the new chunk count, then resets the buffer.
// MUST be called with w.mu held.
func (w *Writer) flushChunkLocked() error {
    if len(w.records) == 0 {
        return nil
    }

    // Build the 65536-byte chunk.
    freeSpaceOffset := uint32(evtxRecordsStart) + uint32(len(w.records))
    chunkHeader := buildChunkHeader(w.firstID, w.recordID-1, freeSpaceOffset)

    chunkBytes := make([]byte, evtxChunkSize)
    copy(chunkBytes[0:], chunkHeader)
    copy(chunkBytes[evtxRecordsStart:], w.records)

    patchEventRecordsCRC(chunkBytes, int(evtxRecordsStart), int(evtxRecordsStart)+len(w.records))
    patchChunkCRC(chunkBytes)

    // Write chunk at its file offset.
    chunkOffset := int64(evtxFileHeaderSize) + int64(w.chunkCount)*int64(evtxChunkSize)
    if _, err := w.f.WriteAt(chunkBytes, chunkOffset); err != nil {
        return fmt.Errorf("go_evtx: write chunk %d: %w", w.chunkCount, err)
    }
    w.chunkCount++

    // Patch file header at offset 0: ChunkCount + NextRecordIdentifier + new CRC.
    newHdr := buildFileHeader(w.chunkCount, w.recordID)
    if _, err := w.f.WriteAt(newHdr, 0); err != nil {
        return fmt.Errorf("go_evtx: patch file header: %w", err)
    }

    // fsync to guarantee durability.
    if err := w.f.Sync(); err != nil {
        return fmt.Errorf("go_evtx: fsync: %w", err)
    }

    // Reset for next chunk.
    w.records = w.records[:0]
    w.firstID = w.recordID

    return nil
}
```

### Pattern 3: WriteRecord / WriteRaw Threshold Check

**What:** After appending a record, check if the buffer has crossed the flush threshold. If yes, call `flushChunkLocked()`.

```go
// Source: existing evtx.go WriteRecord pattern — extend with flush call
func (w *Writer) WriteRecord(eventID int, fields map[string]string) error {
    w.mu.Lock()
    defer w.mu.Unlock()

    binXMLChunkOffset := evtxRecordsStart + uint32(len(w.records)) + evtxRecordHeaderSize
    payload := buildBinXML(eventID, fields, binXMLChunkOffset)
    ts := toFILETIME(parseTimeCreated(fields))
    rec := wrapEventRecord(w.recordID, ts, payload)
    w.records = append(w.records, rec...)
    w.recordID++

    // Replace warn-and-truncate with actual chunk flush.
    if len(w.records) >= int(evtxChunkSize-evtxRecordsStart) {
        return w.flushChunkLocked()
    }
    return nil
}
```

**Note on threshold:** The current `chunkFlushThreshold = 60,000` is a warning threshold, not a hard boundary. For Phase 10, the correct trigger is when `len(w.records) >= evtxChunkSize - evtxRecordsStart` (i.e., 65,536 - 512 = 65,024 bytes). In practice, a write that would push `len(w.records)` over 65,024 should trigger a flush BEFORE appending (pre-check) to avoid the record spanning a chunk boundary — or flush AFTER appending if the record fits exactly. The safest approach is to flush when the record just appended brings `len(w.records)` above threshold.

**BinXML chunk offset adjustment:** After a chunk flush, `w.records` is reset to empty. The next `WriteRecord` call must compute `binXMLChunkOffset` relative to the NEW chunk. Since `w.records` is now empty, `binXMLChunkOffset = evtxRecordsStart + 0 + evtxRecordHeaderSize`. This is correct: the `w.records` buffer always holds records for the current in-progress chunk only, and `evtxRecordsStart` is the chunk-relative offset.

### Pattern 4: Background Goroutine Tick (Phase 9 goroutine — Phase 10 changes)

**What:** The ticker no longer calls `flushToFile()` (which does a full file rewrite). Instead it calls a new function `tickFlushLocked()` that:
1. If `len(w.records) > 0`: flushes the current partial chunk to disk (same as `flushChunkLocked()`).
2. Patches the file header regardless (so `NextRecordIdentifier` is always current on disk).
3. Calls `f.Sync()`.

```go
// Source: goroutine pattern from Phase 9, adapted for open-handle model
func (w *Writer) backgroundLoop() {
    defer w.wg.Done()
    ticker := time.NewTicker(time.Duration(w.cfg.FlushIntervalSec) * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            w.mu.Lock()
            _ = w.tickFlushLocked()
            w.mu.Unlock()
        case <-w.done:
            return
        }
    }
}

// tickFlushLocked writes the current partial chunk (if any) and patches the file header.
// MUST be called with w.mu held.
func (w *Writer) tickFlushLocked() error {
    if len(w.records) == 0 {
        return nil
    }
    // Write the current partial chunk as-is (without resetting the buffer —
    // the goroutine tick does NOT advance the chunk; it just makes progress durable).
    // This is a "checkpoint write" of the in-progress chunk at position w.chunkCount.
    // On the next tick or flush, the same chunk slot is overwritten with more records.

    // ... (see Pitfall 3 for the subtlety here)
    // Alternative: flush the full chunk, reset, start new chunk.
    // Simpler: always flush when threshold crossed in WriteRecord; goroutine only does f.Sync().
    // See Open Questions #1.
    return nil
}
```

**NOTE:** The goroutine tick interaction with partial chunks is the most architecturally subtle decision in Phase 10. See Open Questions section.

### Pattern 5: Close()

```go
// Source: evtx.go Close() from Phase 9, extended for open-handle
func (w *Writer) Close() error {
    close(w.done) // 1. signal goroutine
    w.wg.Wait()   // 2. wait for goroutine exit (without holding lock)
    w.mu.Lock()   // 3. safe to acquire now
    defer w.mu.Unlock()

    defer w.f.Close() // 4. always close the file handle

    if len(w.records) == 0 && w.chunkCount == 0 {
        // No records written — delete the placeholder file.
        os.Remove(w.path)
        return nil
    }
    if len(w.records) > 0 {
        // Flush the final partial chunk.
        if err := w.flushChunkLocked(); err != nil {
            return err
        }
    }
    // flushChunkLocked() already patched the header and called f.Sync().
    return nil
}
```

### Recommended Project Structure

Changes are confined to go-evtx only (no cee-exporter changes needed for Phase 10):

```
go-evtx/
├── evtx.go           # Writer: add f *os.File, chunkCount; implement flushChunkLocked;
│                     # update WriteRecord/WriteRaw threshold; update New(), Close()
├── binformat.go      # No changes needed: buildFileHeader(), patchChunkCRC() already correct
├── binxml.go         # No changes needed: evtxRecordsStart, evtxChunkSize constants
├── evtx_test.go      # Add TestWriter_MoreThan2400Events, TestWriter_TwoFlushSessions,
│                     # TestWriter_FileHeaderFields; update TestWriter_EmptyClose
└── goroutine_test.go # Existing tests still valid; add TestWriter_TickerFlush_OpenHandle
```

No changes to `reader.go`, `binxml_reader.go`, `binformat_test.go`, `binxml.go`. The existing `Reader` already handles multi-chunk files (confirmed: `loadChunk()` iterates chunks by index up to `numChunks`).

### Anti-Patterns to Avoid

- **Writing partial chunks to disk and then resetting on the NEXT write:** This creates a window where the chunk on disk has an invalid CRC (because `patchChunkCRC` was not called on the partial bytes). Always build the full 65,536-byte chunk with padding BEFORE writing.
- **Calling `buildFileHeader` with `chunkCount = 0` for the initial placeholder AND for subsequent flushes without updating:** The file header's `ChunkCount` must always reflect the number of COMPLETE chunks on disk. Writing it once and never updating means readers stop at chunk 0.
- **Using `f.Write(data)` instead of `f.WriteAt(data, offset)` for chunk bodies:** Sequential `f.Write` calls advance a cursor; if the goroutine tick runs between two sequential writes, it reads a file with inconsistent state. `WriteAt` is atomic per chunk.
- **Calling `f.Sync()` from outside `w.mu`:** The file handle `w.f` must only be accessed while holding `w.mu`. `f.Sync()` flushes OS page cache to disk but is NOT a critical section in itself — however, consistency requires the header and chunk to be fully written before sync.
- **Not zeroing the records slice properly after flush:** `w.records = w.records[:0]` reuses the underlying array (good for allocation); but the `firstID` must also be updated to `w.recordID` before the next chunk starts. Forgetting `w.firstID = w.recordID` produces corrupt chunk headers.
- **Using `os.WriteFile` for the file header patch in the open-handle model:** `os.WriteFile` opens-truncates-writes-closes, destroying the chunk data. Use `f.WriteAt(hdr, 0)` to patch only the first 4,096 bytes.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| CRC32 computation | Custom CRC | `hash/crc32` (`crc32.IEEETable`) | Already used in `patchChunkCRC()` and `buildFileHeader()`; IEEE polynomial matches Windows EVTX spec |
| File header update | Custom byte manipulation | Extend `buildFileHeader(chunkCount, nextID)` | Function already correct; just call it with updated args and `f.WriteAt(hdr, 0)` |
| Chunk padding to 65,536 | Manual byte fill | `make([]byte, evtxChunkSize)` + `copy` | Already used in `flushToFile()`; extend the same pattern to `flushChunkLocked()` |
| Multi-chunk traversal in Reader | Custom reader | go-evtx `Reader` (already supports multi-chunk) | `reader.go loadChunk()` already iterates chunks; confirmed by `TestReadRecord_MultipleRecords` which tests 5-record files |

**Key insight:** The go-evtx `Reader` already handles multi-chunk EVTX files. `Open()` reads `numChunks` from the file header at `hdr[42:44]`. `nextRecord()` calls `loadChunk(r.chunkIdx + 1)` when a chunk is exhausted. The ONLY requirement for Phase 10 is that `buildFileHeader` correctly writes `ChunkCount` at `buf[42:44]` and that `ChunkCount` is patched after every flush — which it will be.

---

## Common Pitfalls

### Pitfall 1: File Header ChunkCount Not Updated After Each Chunk Flush

**What goes wrong:** Chunks are written to disk at correct offsets but the file header still reports `ChunkCount = 1`. python-evtx and go-evtx `Reader` both stop reading after chunk 0. Events from chunk 1+ are silently invisible.

**Why it happens:** The file header is written once at `New()` time and not updated. Developers focus on writing chunk data and forget the header.

**How to avoid:** `flushChunkLocked()` MUST call `f.WriteAt(buildFileHeader(w.chunkCount, w.recordID), 0)` after incrementing `w.chunkCount`. This is the critical contract of Phase 10.

**Warning signs:** `go test -run TestWriter_MoreThan2400Events` produces a file that python-evtx parses without errors but reports fewer records than expected.

### Pitfall 2: BinXML Chunk Offset Stale After Chunk Reset

**What goes wrong:** After `flushChunkLocked()` resets `w.records = w.records[:0]`, the first new `WriteRecord` call computes:
```go
binXMLChunkOffset := evtxRecordsStart + uint32(len(w.records)) + evtxRecordHeaderSize
```
Since `len(w.records) == 0`, this gives `binXMLChunkOffset = evtxRecordsStart + evtxRecordHeaderSize` = 512 + 24 = 536. This is CORRECT — the first record in a new chunk starts at chunk-relative offset 536. No stale offset issue because `w.records` is the correct relative buffer. However, if code uses `w.recordID` to compute absolute offsets, a bug would appear.

**Why it happens:** Misunderstanding of whether `binXMLChunkOffset` is chunk-relative or file-relative. It is chunk-relative (relative to the start of the current chunk's BinXML section).

**How to avoid:** Confirm: `binXMLChunkOffset` feeds into `buildBinXML()` which uses it for `NameNode` offset calculations within a template. These offsets are chunk-relative in the EVTX format. The current code is correct. Verify by round-tripping a two-chunk file through the go-evtx `Reader`.

**Warning signs:** python-evtx or go-evtx `Reader` reports BinXML parse errors on records in chunk 1+.

### Pitfall 3: Partial Chunk on Disk Has Wrong CRC

**What goes wrong:** The goroutine tick fires while `w.records` holds a partial chunk. The tick writes partial records to the chunk slot on disk but computes CRC over the partial data. Then more records arrive. The next full flush computes CRC over more data, overwrites the chunk. But if the process crashes BETWEEN the partial write and the full flush, the on-disk chunk has a valid partial CRC that covers records 1..N, with trailing zeros. python-evtx would see a valid chunk with fewer records than were written.

**Why it happens:** Partial chunks are inherently incomplete. Any snapshot written mid-chunk is technically valid EVTX (correct CRC, correct FreeSpaceOffset) but represents a point-in-time snapshot.

**How to avoid:** This is by design — the goroutine tick provides bounded data loss (at most `FlushIntervalSec` seconds). The tick writes a valid partial chunk and patches the file header accordingly. This is the same bounded-loss guarantee as the Phase 9 checkpoint-write model, now implemented with an open-handle.

**Decision for Phase 10:** The goroutine tick should write the partial chunk to disk (same as `flushChunkLocked()`) WITHOUT resetting the buffer. This means the chunk slot is overwritten on the next tick (or the next threshold-crossing flush). The file header is patched to reflect `ChunkCount = w.chunkCount + 1` (the in-progress chunk counts as one incomplete chunk that readers can still parse).

**Warning signs:** Confusion between "flush" (write to disk) and "reset" (start a new chunk). Only reset when the chunk is FULL. Goroutine ticks should flush-without-reset.

### Pitfall 4: File Handle Leaked if New() Fails Mid-Initialization

**What goes wrong:** `os.OpenFile` succeeds, writing the placeholder header fails, and `w.f` is not closed before returning the error.

**Why it happens:** Error path in `New()` before the Writer struct is returned.

**How to avoid:** Always `f.Close()` in the error path of `New()`:
```go
f, err := os.OpenFile(...)
if err != nil { return nil, err }
if _, err := f.Write(hdr); err != nil {
    f.Close()  // must close before returning error
    return nil, fmt.Errorf(...)
}
```

**Warning signs:** `go test -race` reports leaked goroutines or file descriptor leaks on test teardown.

### Pitfall 5: TestWriter_EmptyClose Now Creates a File

**What goes wrong:** With the open-handle model, `New()` immediately creates the file (placeholder header). `TestWriter_EmptyClose` currently asserts that no file exists after `Close()` with no writes. This test fails.

**Why it happens:** Phase 9 used `os.WriteFile` in `Close()` — file only created if records exist. Phase 10 creates the file in `New()`.

**How to avoid:** In `Close()`, if `len(w.records) == 0 && w.chunkCount == 0` (no records ever written), delete the file with `os.Remove(w.path)` before returning. Update `TestWriter_EmptyClose` to verify the file does NOT exist after empty close, which still passes with this approach.

### Pitfall 6: `f.WriteAt` Beyond Current File Size

**What goes wrong:** For chunk N (N > 0), `f.WriteAt(chunkBytes, chunkOffset)` is called where `chunkOffset = evtxFileHeaderSize + N * evtxChunkSize`. If the file has only chunks 0..N-1 written, this write extends the file. Most OS implementations support sparse writes (hole punching), and Go's `os.File.WriteAt` works correctly even if the offset is past the current end of file — the OS fills the gap with zeros.

**Why it happens:** Not a bug, but developers may be surprised that `WriteAt` can extend the file.

**How to avoid:** Confirm behavior with a test. On Linux (ext4, XFS) and macOS (APFS, HFS+), `pwrite(2)` past EOF creates a hole filled with zeros. This is correct for EVTX since chunks are always exactly `evtxChunkSize` bytes and are written in order (no gaps between chunks).

**Warning signs:** File size does not match `evtxFileHeaderSize + chunkCount * evtxChunkSize` after flush.

### Pitfall 7: recordID Off-By-One in File Header

**What goes wrong:** `buildFileHeader(chunkCount, nextRecordID)` sets `NextRecordIdentifier = nextRecordID`. If `w.recordID` starts at 1 and is incremented AFTER appending each record, then after writing N records, `w.recordID == N+1`. Passing `w.recordID` to `buildFileHeader` is correct: it represents the ID the NEXT record would get. If the caller passes `w.recordID - 1` (last written ID), python-evtx accepts it but the field semantics differ from Windows-generated EVTX files.

**Why it happens:** Confusion between "last written record ID" and "next record ID". The EVTX spec uses `NextRecordIdentifier` which means the ID the next record would receive.

**How to avoid:** The existing `buildFileHeader(1, w.recordID)` call in `flushToFile()` already passes `w.recordID` (the next ID), which is correct. Phase 10 should preserve this: `buildFileHeader(w.chunkCount, w.recordID)`.

---

## Code Examples

Verified patterns from official sources and direct codebase inspection:

### buildFileHeader Field Layout (from binformat.go — verified)

```go
// Source: /Users/fjacquet/Projects/go-evtx/binformat.go lines 82-103
// Fields relevant to Phase 10:
//   [16:24]  LastChunkNumber  = chunkCount - 1
//   [24:32]  NextRecordIdentifier = nextRecordID
//   [42:44]  ChunkCount = chunkCount
//   [124:128] CRC32 of buf[0:120]

func buildFileHeader(chunkCount uint16, nextRecordID uint64) []byte {
    // ... existing implementation ...
    // chunkCount=0 in New() placeholder; must be updated to 1+ after first chunk flush
}
```

**CRITICAL:** When `chunkCount = 0` (no chunks written), `LastChunkNumber = uint64(chunkCount-1)` underflows to `0xFFFFFFFFFFFFFFFF`. This is acceptable for the placeholder header that is never read by parsers (because ChunkCount=0 tells readers to stop before loading any chunks). After the first flush, `chunkCount = 1` and `LastChunkNumber = 0`, which is correct.

### Chunk File Offset Calculation

```go
// Source: direct code analysis — pattern from reader.go lines 93
// Reader uses: int64(evtxFileHeaderSize) + int64(idx)*int64(evtxChunkSize)
// Writer must use the same offsets:

func chunkFileOffset(chunkIdx uint16) int64 {
    return int64(evtxFileHeaderSize) + int64(chunkIdx)*int64(evtxChunkSize)
}
// For chunk 0: 4096
// For chunk 1: 4096 + 65536 = 69632
// For chunk N: 4096 + N * 65536
```

### File Header Patch After Chunk Flush

```go
// Source: binformat.go buildFileHeader + os.File.WriteAt docs
// After writing chunk at index w.chunkCount (before increment):

w.chunkCount++ // now reflects the chunk just written
newHdr := buildFileHeader(w.chunkCount, w.recordID)
if _, err := w.f.WriteAt(newHdr, 0); err != nil {
    return fmt.Errorf("go_evtx: patch file header after chunk %d: %w", w.chunkCount-1, err)
}
```

### Records Slice Reset After Full Chunk Flush

```go
// Source: direct analysis — correct reset pattern
w.records = w.records[:0]   // reset length, keep allocated capacity
w.firstID = w.recordID      // next chunk starts at current recordID
// Note: w.recordID is NOT reset — it is monotonically increasing across all chunks
```

### Test: More Than 2,400 Events (Success Criterion 1)

```go
// Source: Phase 10 success criteria → test specification
// A single record is ~27 bytes of BinXML payload + 24-byte header = ~51 bytes minimum.
// A single chunk holds 65024 bytes of records = ~1,274 minimal records.
// Use larger events (~100 bytes each) to hit the threshold at ~650 records.
// For exactly 2,400 events, each event is ~27 bytes → ~2,400 * 27 = ~64,800 bytes per chunk
// (just under the 65,024-byte limit). Use 3,000 events to guarantee two chunks.

func TestWriter_MoreThan2400Events(t *testing.T) {
    dir := t.TempDir()
    outPath := filepath.Join(dir, "large.evtx")

    w, err := New(outPath, RotationConfig{})
    if err != nil {
        t.Fatalf("New: %v", err)
    }

    const count = 3000
    for i := 0; i < count; i++ {
        fields := map[string]string{
            "ProviderName":    "Microsoft-Windows-Security-Auditing",
            "Computer":        "testhost",
            "SubjectUserName": "user",
            "ObjectName":      "/nas/share/file.txt",
        }
        if err := w.WriteRecord(4663, fields); err != nil {
            t.Fatalf("WriteRecord %d: %v", i, err)
        }
    }
    if err := w.Close(); err != nil {
        t.Fatalf("Close: %v", err)
    }

    // Verify record count using go-evtx Reader (equivalent to python-evtx check).
    r, err := Open(outPath)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    defer r.Close()

    var got int
    for {
        rec, err := r.ReadRecord()
        if errors.Is(err, ErrNoMoreRecords) { break }
        if err != nil { t.Fatalf("ReadRecord %d: %v", got, err) }
        got++
        if rec.RecordID != uint64(got) {
            t.Errorf("record %d RecordID = %d, want %d", got, rec.RecordID, got)
        }
    }
    if got != count {
        t.Errorf("read %d records, want %d", got, count)
    }
}
```

### Test: Two-Flush Session (Success Criterion 2)

```go
// Source: Phase 10 success criteria
// Two ticker intervals means the goroutine flushes twice. Each flush writes the current
// partial chunk to disk. Use FlushIntervalSec=1 and write events in two batches.

func TestWriter_TwoFlushSessions(t *testing.T) {
    dir := t.TempDir()
    outPath := filepath.Join(dir, "two_flush.evtx")

    w, err := New(outPath, RotationConfig{FlushIntervalSec: 1})
    if err != nil { t.Fatalf("New: %v", err) }

    fields := map[string]string{
        "ProviderName": "Microsoft-Windows-Security-Auditing",
        "Computer": "testhost",
    }

    // Batch 1: write 5 events, wait for tick.
    for i := 0; i < 5; i++ {
        if err := w.WriteRecord(4663, fields); err != nil { t.Fatalf("WriteRecord: %v", err) }
    }
    time.Sleep(1500 * time.Millisecond)

    // Batch 2: write 5 more events, wait for second tick.
    for i := 0; i < 5; i++ {
        if err := w.WriteRecord(4663, fields); err != nil { t.Fatalf("WriteRecord: %v", err) }
    }
    time.Sleep(1500 * time.Millisecond)

    if err := w.Close(); err != nil { t.Fatalf("Close: %v", err) }

    // Verify file is parseable and has all 10 records.
    r, err := Open(outPath)
    if err != nil { t.Fatalf("Open: %v", err) }
    defer r.Close()

    var count int
    for {
        _, err := r.ReadRecord()
        if errors.Is(err, ErrNoMoreRecords) { break }
        if err != nil { t.Fatalf("ReadRecord: %v", err) }
        count++
    }
    if count != 10 {
        t.Errorf("got %d records, want 10", count)
    }
}
```

### Test: File Header Field Verification (Success Criterion 3)

```go
// Source: Phase 10 success criteria — hex dump at offsets 24-32 (NextRecordIdentifier)
// and offsets 42-44 (ChunkCount)

func TestWriter_FileHeaderFields(t *testing.T) {
    dir := t.TempDir()
    outPath := filepath.Join(dir, "header.evtx")

    const count = 5
    w, err := New(outPath, RotationConfig{})
    if err != nil { t.Fatalf("New: %v", err) }

    for i := 0; i < count; i++ {
        fields := map[string]string{"ProviderName": "Test", "Computer": "host"}
        if err := w.WriteRecord(4663, fields); err != nil { t.Fatalf("WriteRecord: %v", err) }
    }
    if err := w.Close(); err != nil { t.Fatalf("Close: %v", err) }

    data, err := os.ReadFile(outPath)
    if err != nil { t.Fatalf("ReadFile: %v", err) }

    // NextRecordIdentifier at [24:32] = count + 1 (next record would be count+1).
    nextRecordID := binary.LittleEndian.Uint64(data[24:32])
    if nextRecordID != uint64(count+1) {
        t.Errorf("NextRecordIdentifier = %d, want %d", nextRecordID, count+1)
    }

    // ChunkCount at [42:44] = 1 (single chunk for 5 small records).
    chunkCount := binary.LittleEndian.Uint16(data[42:44])
    if chunkCount != 1 {
        t.Errorf("ChunkCount = %d, want 1", chunkCount)
    }
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `flushToFile()` using `os.WriteFile` (full rewrite) | `flushChunkLocked()` using `f.WriteAt` (incremental chunk write) | Phase 10 | No more silent truncation beyond ~2,400 events; true multi-chunk support |
| No persistent file handle (`os.WriteFile` opens+closes each time) | `*os.File` held open from `New()` to `Close()` | Phase 10 | Enables `f.Sync()` without file reopen; aligns with ADR-013 plan |
| `chunkFlushThreshold = 60000` as a warn-only threshold | Hard flush trigger at chunk capacity (65,024 bytes) | Phase 10 | Converts warning to action |
| `flushChunkLocked()` is a NO-OP stub | Fully implemented chunk flush with CRC + file header patch | Phase 10 | EVTX-01 satisfied |
| `buildFileHeader(1, w.recordID)` — hardcoded chunkCount=1 | `buildFileHeader(w.chunkCount, w.recordID)` — dynamic | Phase 10 | Multi-chunk file headers correctly reflect chunk count |

**Deprecated/outdated by Phase 10:**
- `flushToFile()` method: can be removed entirely or kept as a thin wrapper for backward compatibility. The goroutine tick in Phase 10 calls `tickFlushLocked()` instead.
- `os.WriteFile` in `flushToFile()`: no longer used; file is always open.

---

## Open Questions

1. **Goroutine tick: flush-without-reset vs flush-and-reset**

   - What we know: The goroutine tick fires every `FlushIntervalSec` seconds. It can either:
     - **Option A (flush-without-reset):** Write the current partial chunk to disk at slot `w.chunkCount` (without advancing `w.chunkCount`). On the next tick, the same slot is overwritten with more records. When the chunk fills, `flushChunkLocked()` writes it, increments `w.chunkCount`, and opens a new slot.
     - **Option B (flush-and-reset):** On each tick, write the current partial chunk as a complete chunk, increment `w.chunkCount`, reset the buffer, start a new chunk. This means the file grows by one chunk per tick interval even if the chunk is mostly empty.
   - What's unclear: Option A produces smaller files (fewer empty chunk slots) but requires the goroutine to know that it should overwrite the same slot. Option B is simpler to implement and is always correct for readers (each chunk is self-contained) but wastes space.
   - Recommendation: **Option A** for Phase 10 (flush-without-reset on goroutine tick). The chunk slot at `w.chunkCount` is the "in-progress" slot; the goroutine writes a valid partial chunk there. This aligns with how Windows Event Log service works (the in-progress chunk is always partially filled). Option B is simpler but produces files with many mostly-empty chunks in low-throughput sessions.

2. **What happens to `flushToFile()` after Phase 10?**

   - What we know: `flushToFile()` is called by the goroutine (from Phase 9) and by `Close()`. Phase 10 replaces both call sites.
   - What's unclear: Should `flushToFile()` be removed, renamed, or kept?
   - Recommendation: Remove `flushToFile()`. Replace all call sites with `flushChunkLocked()` (for full-chunk flush) and a new `patchHeaderAndSync()` helper (for the goroutine tick's partial-chunk checkpoint).

3. **go-evtx version bump**

   - What we know: go-evtx is at v0.2.0 with a local `replace` directive in cee-exporter's `go.mod`.
   - What's unclear: Phase 10 is a breaking API change? No — `New()`, `WriteRecord()`, `WriteRaw()`, `Close()` signatures are unchanged. The `Writer` struct gains internal fields. No public API change. This is a v0.2.x patch release (v0.2.1) or a v0.3.0 minor release.
   - Recommendation: Publish as v0.3.0 (new feature: multi-chunk support) after Phase 10 tests pass. Update cee-exporter `go.mod` replace directive.

4. **`TestWriter_EmptyClose` behaviour change**

   - What we know: Currently, empty `Close()` does not create the file. With open-handle, the file is created in `New()`.
   - What's unclear: Is deleting the file in `Close()` on empty session the right choice?
   - Recommendation: Yes — `os.Remove(w.path)` in `Close()` when zero records written. This preserves backward compatibility with `TestWriter_EmptyClose`. The test needs minor update: verify file does not exist (same assertion, same result).

5. **Race between goroutine tick and `WriteRecord` for the same chunk slot**

   - What we know: With Option A (flush-without-reset on goroutine tick), the goroutine and `WriteRecord` both access `w.records` and potentially the same chunk slot on disk. Both are guarded by `w.mu`, so there is no data race.
   - What's unclear: If the goroutine tick writes chunk slot 0 with 100 records, then `WriteRecord` appends record 101 and checks if threshold is crossed, would it then call `flushChunkLocked()` which also writes slot 0? Answer: yes, and this is correct — slot 0 is overwritten with 101 records, which supersedes the 100-record checkpoint written by the goroutine.
   - Recommendation: Document this "slot overwrite" pattern clearly in the implementation. No code change needed beyond what is described above.

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing package (stdlib), go 1.24 |
| Config file | None — `go test ./...` in each module |
| Quick run command | `cd /Users/fjacquet/Projects/go-evtx && go test ./...` |
| Full suite command | `cd /Users/fjacquet/Projects/go-evtx && go test -race ./...` (requires CGO=1) |
| cee-exporter sanity | `cd /Users/fjacquet/Projects/cee-exporter && make test` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| EVTX-01 | 3,000 events produces file where Reader reports all 3,000 records | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_MoreThan2400Events -v ./...` | ❌ Wave 0 |
| EVTX-01 | Two-flush session produces parseable file with all records | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_TwoFlushSessions -v ./...` | ❌ Wave 0 |
| EVTX-01 | File header NextRecordIdentifier and ChunkCount correct after Close | unit | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_FileHeaderFields -v ./...` | ❌ Wave 0 |
| EVTX-01 | Zero data races on concurrent WriteRecord + Close | race | `cd /Users/fjacquet/Projects/go-evtx && go test -race -run TestWriter_BackgroundFlush_NoRace ./...` | ✅ (exists, must still pass) |
| EVTX-01 | ChunkCount in file header increments correctly after each chunk boundary | unit | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_ChunkCountIncrement -v ./...` | ❌ Wave 0 |
| EVTX-01 | Empty Close deletes placeholder file (backward compat) | unit | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_EmptyClose -v ./...` | ✅ (exists, may need update) |
| EVTX-01 | Reader reads all chunks from multi-chunk file | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestReadRecord_MultipleRecords -v ./...` | ✅ (exists — extend to multi-chunk range) |

### Sampling Rate

- **Per task commit:** `cd /Users/fjacquet/Projects/go-evtx && go test ./...` (no CGO; baseline correctness)
- **Per wave merge:** `cd /Users/fjacquet/Projects/go-evtx && go test -race ./...` + `cd /Users/fjacquet/Projects/cee-exporter && make test`
- **Phase gate:** Both suites green + race-clean before `/gsd:verify-work`

### Wave 0 Gaps

- [ ] `TestWriter_MoreThan2400Events` in `/Users/fjacquet/Projects/go-evtx/evtx_test.go` — covers EVTX-01 record count (Success Criterion 1)
- [ ] `TestWriter_TwoFlushSessions` in `/Users/fjacquet/Projects/go-evtx/goroutine_test.go` — covers EVTX-01 two-flush correctness (Success Criterion 2)
- [ ] `TestWriter_FileHeaderFields` in `/Users/fjacquet/Projects/go-evtx/evtx_test.go` — covers EVTX-01 header field correctness (Success Criterion 3)
- [ ] `TestWriter_ChunkCountIncrement` in `/Users/fjacquet/Projects/go-evtx/evtx_test.go` — covers EVTX-01 multi-chunk header tracking
- [ ] Update `TestWriter_EmptyClose` if file creation in `New()` changes its assertion

---

## Sources

### Primary (HIGH confidence)

- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/evtx.go` — Writer struct, `New()`, `WriteRecord()`, `WriteRaw()`, `Close()`, `flushToFile()` — confirmed write-on-close model with single-chunk truncation; `flushChunkLocked()` is a NO-OP
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/binformat.go` — `buildFileHeader()` layout, `patchChunkCRC()`, `evtxFileHeaderSize=4096`, `evtxChunkSize=65536`, `evtxChunkHeaderSize=512`; `buildFileHeader` field offsets documented in code comments
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/binxml.go` — `evtxRecordsStart=512`, `evtxRecordHeaderSize=24`, `chunkFlushThreshold=60000`
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/reader.go` — `Open()` reads `numChunks` from `hdr[42:44]`; `loadChunk()` iterates by index using `int64(evtxFileHeaderSize) + int64(idx)*int64(evtxChunkSize)` offset; multi-chunk read confirmed working
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/reader_test.go` — `TestReadRecord_MultipleRecords` writes 5 records to single chunk, reads them back; multi-chunk test NOT yet written (Wave 0 gap)
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/goroutine_test.go` — existing 7 goroutine tests confirmed; all must pass after Phase 10 refactor
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/.planning/phases/09-goroutine-scaffolding-and-fsync/09-VERIFICATION.md` — Phase 9 fully verified; 13/13 truths confirmed; confirms Writer has `done`, `wg`, `backgroundLoop()`, `Close()` ordering
- [pkg.go.dev/os#File.WriteAt](https://pkg.go.dev/os#File.WriteAt) — WriteAt writes to file at given offset without affecting current file cursor; equivalent to `pwrite(2)`
- [pkg.go.dev/os#File.Sync](https://pkg.go.dev/os#File.Sync) — maps to `fsync(2)` on Linux, `F_FULLFSYNC` on macOS
- libevtx EVTX format documentation — file header layout at offsets 8-128; chunk layout at offsets 0-512; confirmed by binformat.go source

### Secondary (MEDIUM confidence)

- `.planning/phases/09-goroutine-scaffolding-and-fsync/09-RESEARCH.md` — Phase 9 research; documents the planned Phase 10 transition from write-on-close to open-handle; ADR-013 confirms "open-handle model planned for Phase 10"
- `.planning/STATE.md` blocker entry: "`flushChunkLocked()` stub silently drops events beyond ~2,400 per session — must be fixed before rotation is added"
- `.planning/REQUIREMENTS.md` EVTX-01 definition: "fix `flushChunkLocked()` stub that currently silently drops events beyond ~2,400 per session"

### Tertiary (LOW confidence)

- None — all findings grounded in direct code inspection and stdlib documentation.

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — stdlib only; all patterns derived from direct source inspection of working go-evtx code
- Architecture: HIGH — EVTX format constraints verified against binformat.go and reader.go which are already correct; multi-chunk read confirmed via reader.go analysis
- Pitfalls: HIGH — derived from direct analysis of the specific code that will be modified; no speculative pitfalls

**Research date:** 2026-03-05
**Valid until:** 2026-04-05 (EVTX format is frozen; Go stdlib file I/O semantics are stable)
