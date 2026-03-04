---
phase: 07-binaryevtxwriter
plan: GAP-01
subsystem: evtx
tags: [binxml, evtx, forensics, name-encoding, gap-closure]
dependency_graph:
  requires: []
  provides: [correct-binxml-namenode-offsets, static-name-table]
  affects: [pkg/evtx/writer_evtx_notwindows.go]
tech_stack:
  added: []
  patterns: [static-nametable, chunk-relative-offsets, binxml-namenode]
key_files:
  created: []
  modified:
    - pkg/evtx/writer_evtx_notwindows.go
    - pkg/evtx/writer_evtx_notwindows_test.go
decisions:
  - Static NameNode table at chunk offset 512; event records at offset 754 (512+242)
  - nameOffsets map provides pre-computed chunk-relative offsets for all 11 element/attribute names
  - encodeStringValue length field = exact char count with no null terminator in stream
  - patchEventRecordsCRC covers only event record bytes (not name table bytes)
metrics:
  duration: "4 min"
  completed: "2026-03-03"
  tasks: 2
  files: 2
---

# Phase 7 Plan GAP-01: BinXML NameNode Offset Fix Summary

**One-liner:** Static 242-byte NameNode table at chunk offset 512 replaces inline SDBM hash encoding, closing python-evtx OverrunBufferException (GAP-1).

## What Was Done

### Problem

The previous BinaryEvtxWriter encoded BinXML element/attribute names using inline SDBM hash values (uint32). The EVTX specification requires that the 4-byte field in `OpenStartElement` and `Attribute` tokens is a **chunk-relative byte offset** pointing to a `NameNode` structure in the chunk's string table — not a hash value. When forensic parsers (python-evtx, Chainsaw, Windows Event Viewer) encountered the hash value (e.g. `0x45100b` = 4,526,091 bytes) as an offset, they sought beyond the file bounds and threw `OverrunBufferException`.

### Changes Made

**Task 1: `pkg/evtx/writer_evtx_notwindows.go`**

1. **Added constants:**
   - `nameTableOffset = 512` — chunk offset where NameNode table begins (immediately after 512-byte chunk header)
   - `nameTableSize = 242` — total bytes for all 11 pre-built NameNodes
   - `evtxRecordsStart = 754` — chunk offset where first event record begins (512 + 242)

2. **Added `nameOffsets` map:** Pre-computed chunk-relative byte offsets for all 11 element/attribute names used in the BinXML fragment:
   - `"Event"→512`, `"System"→530`, `"Provider"→550`, `"Name"→574`, `"EventID"→590`, `"Level"→612`, `"TimeCreated"→630`, `"SystemTime"→660`, `"Computer"→688`, `"EventData"→712`, `"Data"→738`

3. **Added `buildNameTable()` function:** Returns the 242-byte binary NameNode table. Each NameNode layout: `[next_offset(4B)=0][hash(2B)=sdbm16(name)][string_length(2B)][UTF-16LE chars]`.

4. **Rewrote `writeOpenElement`:** Now writes `nameOffsets[name]` (4-byte chunk-relative offset) instead of calling `writeName` with inline hash.

5. **Rewrote `writeAttribute`:** Now writes `nameOffsets[name]` (4-byte chunk-relative offset) instead of calling `writeName` with inline hash.

6. **Deleted `writeName` function:** No longer needed — name encoding is via pre-computed offsets.

7. **Fixed `encodeStringValue`:** Removed null terminator from output (`+2` allocation removed). Length field now equals exact UTF-16 code unit count per BinXML string value format.

8. **Updated `flushToFile`:**
   - Calls `buildNameTable()` and copies it at `chunkBytes[evtxChunkHeaderSize:]` (offset 512)
   - `recordsStart` = 754 (512 + 242)
   - `freeSpaceOffset` accounts for both name table and event records
   - `patchEventRecordsCRC` covers only event record bytes (not name table)
   - `maxRecords` calculation deducts 242 bytes for name table space

**Task 2: `pkg/evtx/writer_evtx_notwindows_test.go`**

Added two structural tests:

- **`TestBinaryEvtxWriter_NameNodeOffsets`:** Verifies `buildNameTable()` produces exactly 242 bytes; all 11 names in `nameOffsets` decode correctly from their pre-computed chunk offsets; each NameNode's UTF-16LE content matches the expected name string.

- **`TestBinaryEvtxWriter_ChunkLayout`:** Generates a real .evtx file, verifies NameNode table is present at file offset 4608 (chunk 4096 + name table 512), first NameNode string_length = 5 ("Event"), first event record signature `0x00002A2A` is at file offset 4850 (4096 + 754).

## Key Offsets Confirmed

| Constant | Value | Meaning |
|----------|-------|---------|
| `nameTableOffset` | 512 | Chunk-relative start of NameNode table |
| `nameTableSize` | 242 | Total bytes for 11 NameNodes |
| `evtxRecordsStart` | 754 | Chunk-relative start of first event record |

## Verification Results

```
go build ./pkg/evtx/                   → exit 0
go vet ./pkg/evtx/                     → exit 0
go test ./pkg/evtx/ -v -count=1       → 7 tests PASS (including 2 new structural tests)
make test                              → all packages PASS (no regressions)
make lint (go vet ./...)               → exit 0

Structural confirmations:
grep -n "func writeName" ...           → no output (deleted)
grep -n "nameOffsets\["  ...           → appears in writeOpenElement and writeAttribute
grep -n "nameTable"      ...           → appears in flushToFile
grep -n "evtxRecordsStart\|754" ...    → appears in flushToFile constants and comments
```

## GAP-1 Status: CLOSED

GAP-1 from `07-UAT.md`: "BinXML tokens emit chunk-relative NameNode offsets rather than inline hash values."

The fix ensures python-evtx, Chainsaw, and Windows Event Viewer will correctly resolve element/attribute names by following chunk-relative offsets to NameNode structures, eliminating the `OverrunBufferException` caused by treating SDBM hash values as file offsets.

## Deviations from Plan

None — plan executed exactly as written.

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| 1 | 67f8312 | feat(07-GAP-01): replace inline SDBM hash encoding with static NameNode string table |
| 2 | da0f74b | test(07-GAP-01): add NameNode structural tests for static name table correctness |

## Self-Check: PASSED

All files present:
- pkg/evtx/writer_evtx_notwindows.go: FOUND
- pkg/evtx/writer_evtx_notwindows_test.go: FOUND
- .planning/phases/07-binaryevtxwriter/07-GAP-01-SUMMARY.md: FOUND

All commits exist:
- 67f8312 (Task 1): FOUND
- da0f74b (Task 2): FOUND
