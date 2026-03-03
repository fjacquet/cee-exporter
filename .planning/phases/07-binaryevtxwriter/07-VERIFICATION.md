---
phase: 07-binaryevtxwriter
verified: 2026-03-03T20:00:00Z
status: human_needed
score: 13/13 must-haves verified (automated); 2/3 success criteria need human
re_verification: false
human_verification:
  - test: "Copy generated .evtx file to Windows and open in Event Viewer"
    expected: "Event Viewer displays events with correct EventIDs (4663, 4660, 4670) and readable audit fields (Computer, ObjectName, AccessMask, SubjectUserName)"
    why_human: "No Windows environment available in CI; BinXML content correctness beyond magic/CRC cannot be verified programmatically without the 0xrawsec/golang-evtx oracle (excluded due to CGO dependency issues)"
  - test: "Parse generated .evtx file with a forensics tool (Splunk, Elastic Agent, or evtxdump)"
    expected: "Tool extracts event records with correct timestamps, EventIDs, and subject/object fields from the BinXML payload"
    why_human: "Requires an actual .evtx parser that can validate BinXML token-stream content, not just file/chunk header structure"
---

# Phase 7: BinaryEvtxWriter Verification Report

**Phase Goal:** Linux operators can configure cee-exporter to write native .evtx files that open correctly in Windows Event Viewer and forensics tools
**Verified:** 2026-03-03T20:00:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

All 13 automated must-haves from the two plan frontmatter sections are verified. Two of the three ROADMAP success criteria require human validation (Windows Event Viewer and forensics tool parsing).

#### Plan 07-01 Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | FILETIME conversion produces correct 100ns epoch offset from Unix timestamps | VERIFIED | `TestToFILETIME` passes; Unix epoch → 116444736000000000; 2024-01-01 → (1704067200*10_000_000)+116444736000000000 |
| 2 | CRC32 computed over correct byte ranges for file header and chunk header | VERIFIED | `TestBuildFileHeader` and `TestPatchChunkCRC` pass; file header uses buf[0:120]; chunk header uses buf[0:120]+buf[128:512] |
| 3 | UTF-16LE encoding produces length-prefixed null-terminated word arrays | VERIFIED | `TestEncodeUTF16LE` passes; empty string→4 bytes, "A"→6 bytes, "AB"→8 bytes with correct layout |
| 4 | Event record wrapper correctly embeds signature, size fields, and size-copy at end | VERIFIED | `TestWrapEventRecord` passes; signature=0x2A2A0000, size at [4:8] and [end-4:end] identical |
| 5 | All helpers compile with CGO_ENABLED=0 on all platforms (no build tag needed) | VERIFIED | `CGO_ENABLED=0 go build ./...` succeeds; `CGO_ENABLED=0 GOOS=windows go build ./...` succeeds |

#### Plan 07-02 Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 6 | Operator sets type=evtx with a file path and daemon writes .evtx file without error | VERIFIED | `TestBinaryEvtxWriter_WriteClose` passes; file created, non-zero size, magic "ElfFile\x00" confirmed |
| 7 | BinaryEvtxWriter.WriteEvent accepts concurrent calls safely via sync.Mutex | VERIFIED | `TestBinaryEvtxWriter_Concurrent` passes; 10 goroutines, file non-zero after Close() |
| 8 | BinaryEvtxWriter.Close flushes chunk (header + records) and file header atomically | VERIFIED | `TestBinaryEvtxWriter_WriteClose` verifies file header CRC32 and chunk magic at offset 4096 |
| 9 | Generated .evtx file round-trips through structural oracle (magic + CRC32) | VERIFIED | Structural oracle used; 0xrawsec/golang-evtx oracle excluded due to CGO dependency issues (per plan) |
| 10 | writer_evtx_stub.go is deleted; writer_evtx_notwindows.go replaces it | VERIFIED | `ls pkg/evtx/writer_evtx_stub.go` → file not found; `writer_evtx_notwindows.go` present (12.3K, //go:build !windows) |
| 11 | make build and make build-windows both succeed after stub deletion | VERIFIED | `CGO_ENABLED=0 go build ./...` succeeds; `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` succeeds |

#### ROADMAP Success Criteria

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| SC-1 | Operator sets `type = "evtx"` with output file path; daemon writes .evtx without error | VERIFIED | `NewNativeEvtxWriter` → `NewBinaryEvtxWriter`; `TestBinaryEvtxWriter_WriteClose` confirms |
| SC-2 | .evtx file copied to Windows opens in Event Viewer with correct EventIDs (4663, 4660, 4670) | NEEDS HUMAN | BinXML content uses correct tokens but full round-trip requires Windows environment |
| SC-3 | Forensics tool extracts records with correct timestamps and subject/object fields | NEEDS HUMAN | No programmatic oracle available; structural tests only verify header correctness |

**Automated Score:** 11/11 automated truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `pkg/evtx/evtx_binformat.go` | Pure binary helpers: toFILETIME, encodeUTF16LE, buildFileHeader, buildChunkHeader, patchChunkCRC, patchEventRecordsCRC, wrapEventRecord | VERIFIED | 194 lines, 7 helpers, no build tag, stdlib only (encoding/binary, hash/crc32, time, unicode/utf16) |
| `pkg/evtx/evtx_binformat_test.go` | Table-driven unit tests for all helpers; no external deps | VERIFIED | 254 lines, 5 test functions, 10 sub-tests, all pass under CGO_ENABLED=0 |
| `pkg/evtx/writer_evtx_notwindows.go` | Full BinaryEvtxWriter: NewBinaryEvtxWriter, WriteEvent, Close, buildBinXML | VERIFIED | 371 lines, //go:build !windows, sync.Mutex, SDBM hash, static BinXML token stream |
| `pkg/evtx/writer_evtx_notwindows_test.go` | Round-trip and structural tests for BinaryEvtxWriter | VERIFIED | 255 lines, //go:build !windows, 5 test functions (WriteClose, EmptyClose, Concurrent, EmptyPath, ParentDirCreated) |
| `pkg/evtx/writer_evtx_stub.go` | DELETED (replaced by notwindows implementation) | VERIFIED | File not found — confirmed deleted |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `pkg/evtx/writer_native_notwindows.go` | `pkg/evtx/writer_evtx_notwindows.go` | `NewBinaryEvtxWriter` call in `NewNativeEvtxWriter` | WIRED | Line 8: `return NewBinaryEvtxWriter(evtxPath)` — returns `Writer` interface, compile-time checked |
| `pkg/evtx/writer_evtx_notwindows.go` | `pkg/evtx/evtx_binformat.go` | helpers called in WriteEvent and Close | WIRED | `toFILETIME` at line 87, `wrapEventRecord` at line 88, `buildChunkHeader`/`patchEventRecordsCRC`/`patchChunkCRC`/`buildFileHeader` in `flushToFile` |
| `pkg/evtx/evtx_binformat.go` | (consumed by writer) | `toFILETIME`, `encodeUTF16LE`, `buildFileHeader`, `wrapEventRecord`, `patchChunkCRC` | WIRED | All 7 exported helpers used; no orphaned helpers |

### Requirements Coverage

| Requirement | Source Plans | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| OUT-05 | 07-01, 07-02 | Operator can configure BinaryEvtxWriter to write native .evtx files on Linux | SATISFIED | `NewNativeEvtxWriter` routes to `NewBinaryEvtxWriter`; `TestBinaryEvtxWriter_WriteClose` confirms file is created with valid structure; factory wired at `writer_native_notwindows.go:8` |
| OUT-06 | 07-01, 07-02 | .evtx files generated on Linux open correctly in Windows Event Viewer and can be parsed by forensics tools | PARTIAL — automated portion satisfied | File/chunk header CRC32 verified (most parser-critical); BinXML content correctness requires human validation with Windows Event Viewer |

**Orphaned requirements:** None. Both OUT-05 and OUT-06 are covered by Phase 7 plans. No additional requirements mapped to Phase 7 in REQUIREMENTS.md.

### Anti-Patterns Found

No anti-patterns detected in key implementation files.

| File | Pattern | Status |
|------|---------|--------|
| `writer_evtx_notwindows.go` | TODO/FIXME/placeholder | None found |
| `evtx_binformat.go` | TODO/FIXME/placeholder | None found |
| `writer_evtx_notwindows.go` | Empty return/stub bodies | None — WriteEvent, Close, flushToFile, buildBinXML all contain real implementation |

**Notable (non-blocking):**

- `flushChunkLocked()` (lines 131-143 of `writer_evtx_notwindows.go`): The mid-stream chunk flush is a "soft flush" — it logs a warning but does not discard records. This is intentional and documented in a code comment. The plan explicitly anticipates this: "Multi-chunk support deferred." Event records accumulate in-memory and are all written in a single chunk on `Close()`. This is an OUT-F01 future requirement, not a blocker for v2.
- `writer_native_notwindows.go` comment says "output directory" but `NewBinaryEvtxWriter` treats `evtxPath` as a file path. Minor documentation inconsistency, not a functional issue.

### Human Verification Required

#### 1. Windows Event Viewer Round-Trip

**Test:** Generate a .evtx file using `go test ./pkg/evtx/ -run TestBinaryEvtxWriter_WriteClose` (the file is created in a temp dir; add a `t.Log(outPath)` or modify the test to keep the file), then copy it to a Windows machine and open it in Event Viewer (eventvwr.msc).

**Expected:** Event Viewer opens the file without an error dialog; the Security log channel shows 3 events with EventIDs 4663, 4660, and 4670; each event shows Computer="testhost" and readable fields in the General/Details tabs.

**Why human:** No Windows environment available in the CI pipeline. The BinXML token stream uses SDBM name hashes and length-prefixed UTF-16LE values per the libevtx specification, but subtle encoding errors in the BinXML layer (not the file/chunk headers) would only surface when a Windows parser tries to interpret the element names and values.

#### 2. Forensics Tool Parsing

**Test:** Produce a .evtx file with real EventIDs (4663, 4660, 4670) and attempt to parse it with `evtxdump` (python-evtx), Splunk Universal Forwarder, or an Elastic Agent. Alternatively, compile and run a Go program using `github.com/0xrawsec/golang-evtx` (with CGO enabled) to parse the file.

**Expected:** The tool extracts 3 event records; EventIDs match 4663/4660/4670; timestamps are within 1 second of the time written; SubjectUserName, ObjectName, and AccessMask fields carry the expected values.

**Why human:** The 0xrawsec/golang-evtx oracle was explicitly excluded from tests due to CGO transitive dependency issues under `CGO_ENABLED=0` (per plan decision). Structural verification (magic bytes + file/chunk CRC32) confirms parser-critical correctness but cannot validate BinXML payload semantics.

### Gaps Summary

No automated gaps were found. All 11 automated truths are verified. The phase is blocked only on human validation of SUCCESS CRITERIA 2 and 3 (Windows Event Viewer and forensics tool round-trip), which require a Windows environment and/or a CGO-compatible EVTX parser.

The structural correctness foundation is solid:
- File magic "ElfFile\x00" and chunk magic "ElfChnk\x00" are correct.
- File header CRC32 (crc32 of buf[0:120]) is verified by `TestBinaryEvtxWriter_WriteClose`.
- Chunk header CRC32 (crc32 of buf[0:120]+buf[128:512]) is verified by `TestPatchChunkCRC`.
- Event record size fields (size at [4:8] and trailing copy at [end-4:end]) are verified by `TestWrapEventRecord`.
- All 74 tests across 9 packages pass with CGO_ENABLED=0.

OUT-06 is the only requirement with a non-automated portion — the "opens correctly in Windows Event Viewer and forensics tools" claim depends on BinXML content correctness, which was implemented using the correct SDBM hash and token-stream approach but cannot be verified without a Windows parser.

---

_Verified: 2026-03-03T20:00:00Z_
_Verifier: Claude (gsd-verifier)_
