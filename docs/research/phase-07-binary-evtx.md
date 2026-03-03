# Phase 7: BinaryEvtxWriter - Research

**Researched:** 2026-03-03
**Domain:** Windows EVTX binary file format — pure-Go serialization on Linux
**Confidence:** MEDIUM (format well-documented; no Go writer library exists — must implement from spec)

---

<phase_requirements>

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| OUT-05 | Operator can configure BinaryEvtxWriter to write native `.evtx` files on Linux | BinXML encoding + chunk/file header construction with `encoding/binary` + `hash/crc32`; `writer_evtx_stub.go` is the replacement target |
| OUT-06 | `.evtx` files generated on Linux open correctly in Windows Event Viewer and can be parsed by forensics tools | File must comply with ElfFile/ElfChnk binary layout with correct CRC32 checksums; `0xrawsec/golang-evtx` used as round-trip test oracle |
</phase_requirements>

---

## Summary

No production-ready Go library for **writing** binary EVTX files exists in the ecosystem as of March 2026. All Go libraries found (`0xrawsec/golang-evtx`, `Velocidex/evtx`, `refractionPOINT/evtx`) are read-only parsers. The format must be hand-implemented using the well-documented libevtx specification, the xml2evtx Python reference implementation (JPCERTCC), and the official Microsoft MS-EVEN6/MS-BINXML specifications.

The EVTX binary format is layered: a 4096-byte file header, followed by 65536-byte chunks, each containing a 512-byte chunk header and variable-length event records. Events encode their XML payload as BinXML — a token-based binary encoding with a SDBM element name hash table. The most tractable implementation path for this project is a "static template" approach: one hard-coded BinXML template covering the `Event/System` + `Event/EventData` schema for EventIDs 4663/4660/4670. Template sharing (OUT-F01 in REQUIREMENTS.md) is explicitly deferred.

The existing stub in `pkg/evtx/writer_evtx_stub.go` (`//go:build !windows`) is the replacement target. The `writer_native_notwindows.go` factory already routes `type = "evtx"` on Linux to `NewBinaryEvtxWriter`, so `main.go` and `buildWriter` need no changes. The implementation is pure stdlib — `encoding/binary`, `hash/crc32`, `os`, `sync` — no new dependencies.

**Primary recommendation:** Implement BinaryEvtxWriter as a pure-Go stdlib-only EVTX writer in `pkg/evtx/writer_evtx_stub.go` (rename to `writer_evtx_notwindows.go`) using a statically-encoded BinXML template. Use `0xrawsec/golang-evtx` as a test-only round-trip oracle.

---

## Standard Stack

### Core (all stdlib — zero new dependencies)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `encoding/binary` | stdlib (go1.24) | Write fixed-size structs in little-endian order | Native Go binary serialization; `binary.Write` with `binary.LittleEndian` handles all EVTX header structs |
| `hash/crc32` | stdlib (go1.24) | CRC32 IEEE checksums for file header + chunk header | EVTX checksums are CRC32 per RFC 1952 — `crc32.IEEETable` matches exactly |
| `os` | stdlib | Create and write `.evtx` file on disk | Direct file I/O for output path |
| `sync` | stdlib | `sync.Mutex` guards `*os.File` against concurrent `WriteEvent` calls | All Writers in this project use `sync.Mutex` — matches GELFWriter/BeatsWriter pattern |
| `bytes` | stdlib | `bytes.Buffer` for constructing BinXML payloads before flushing | Avoids partial-write corruption during BinXML assembly |
| `log/slog` | stdlib | Structured logging consistent with rest of codebase | All writers use `slog` |
| `context` | stdlib | `WriteEvent(ctx, e)` interface signature | Required by `evtx.Writer` interface |

### Test Oracle (test-only, not a runtime dependency)

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/0xrawsec/golang-evtx` | latest | Round-trip parse of generated `.evtx` files in tests | Verify generated files are structurally valid — use only in `_test.go` to avoid adding a runtime dep |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Hand-rolled BinXML | xml2evtx Python tool invoked as subprocess | Platform dependency; not acceptable for a static Go binary |
| Static template BinXML | Full BinXML template engine | OUT-F01 deferred; static template covers all required EventIDs at 1/3 the complexity |
| Single `.evtx` file per run | Rolling files by date | Out of scope per REQUIREMENTS.md out-of-scope list ("EVTX chunked streaming (mid-chunk)") |
| 0xrawsec/golang-evtx as runtime dep | Velocidex/evtx | Either works as test oracle; 0xrawsec is more widely referenced |

**Installation (test-only oracle):**

```bash
go get github.com/0xrawsec/golang-evtx@latest
```

Place only in `_test.go` imports so it does not enter the production binary.

---

## Architecture Patterns

### Recommended File Structure (no new packages needed)

```
pkg/evtx/
├── writer_evtx_notwindows.go   # Replaces writer_evtx_stub.go — BinaryEvtxWriter impl
│                               # //go:build !windows
├── writer_evtx_notwindows_test.go  # Round-trip tests using 0xrawsec as oracle
├── writer_native_notwindows.go # UNCHANGED — already routes to NewBinaryEvtxWriter
└── ... (existing files unchanged)
```

The file `writer_evtx_stub.go` must be **deleted** and replaced with `writer_evtx_notwindows.go`. The `_stub` suffix is no longer appropriate. The `//go:build !windows` guard stays.

### Pattern 1: Static BinXML Template

**What:** Pre-encode the BinXML fragment for the known `Event/System/EventData` schema once at construction. At write time, substitute variable fields (EventID, timestamps, string values) at known byte offsets in the pre-built template bytes.

**When to use:** When the XML schema is fixed and known at compile time (true here — we only emit 4663/4660/4670 with identical structure).

**Why:** Full BinXML encoding (name hash table, template pointer array, string deduplication) is 600-1000 LOC. Static template substitution is ~150-200 LOC and produces valid files that pass round-trip parsing.

**Key fields to substitute per event:**

- EventID (uint16 at known offset)
- TimeCreated SystemTime attribute (FILETIME uint64 at known offset)
- EventRecordID (uint64 in event record header)
- Written timestamp in record header (FILETIME uint64)
- String substitutions: Computer, SubjectUserSID, SubjectUsername, SubjectDomain, ObjectName, AccessMask, etc.

### Pattern 2: Single-Chunk Flush Strategy

**What:** Buffer events in memory until the chunk (65536 bytes) is full or `Close()` is called. On close/flush, finalize the chunk header (last record offset, free space offset, CRC32), then write the complete file header + chunk to disk atomically.

**When to use:** For the v2.0 implementation — avoids mid-chunk partial-write corruption.

**Note from REQUIREMENTS.md out-of-scope:** "EVTX chunked streaming (mid-chunk)" is explicitly out of scope. Flush per chunk on `Close()` is the correct approach.

### Pattern 3: CRC32 Deferred Patch

**What:** Write placeholder zeros for CRC32 fields during initial construction. Patch them in the final buffer using slice indexing before flushing to disk.

**Example (Go stdlib):**

```go
// Source: libevtx spec + hash/crc32 stdlib
import "hash/crc32"

// File header CRC32 covers bytes 0-119
headerCRC := crc32.Checksum(buf[0:120], crc32.IEEETable)
binary.LittleEndian.PutUint32(buf[124:128], headerCRC)

// Chunk header CRC32 covers bytes 0-119 and 128-511
chunkCRC := crc32.New(crc32.IEEETable)
chunkCRC.Write(chunkBuf[0:120])
chunkCRC.Write(chunkBuf[128:512])
binary.LittleEndian.PutUint32(chunkBuf[124:128], chunkCRC.Sum32())
```

### Pattern 4: FILETIME Encoding

**What:** Windows FILETIME = 100-nanosecond intervals since 1601-01-01 00:00:00 UTC. Go `time.Time` → FILETIME requires a fixed epoch offset.

**Example:**

```go
// Source: windows FILETIME spec (libevtx documentation)
// FILETIME epoch: January 1, 1601 UTC
// Unix epoch:     January 1, 1970 UTC
// Delta = 116444736000000000 × 100ns intervals
const filetimeEpochDelta = int64(116444736000000000)

func toFILETIME(t time.Time) uint64 {
    ns100 := t.UTC().UnixNano() / 100
    return uint64(ns100 + filetimeEpochDelta)
}
```

### Anti-Patterns to Avoid

- **Writing partial chunks to disk then patching:** CRC32 in chunk header must cover the finalized chunk. Write the full chunk as one atomic `os.File.Write` call.
- **Using `binary.Write` on non-fixed-size structs:** `binary.Write` panics on structs with string fields. Use `encoding/binary` with fixed-size Go types (`[8]byte`, `uint64`, `uint32`, `uint16`) only.
- **Confusing CRC32 scopes:** File header CRC covers bytes 0-119. Chunk header CRC covers bytes 0-119 AND 128-511 (skipping the 8 bytes where the CRC itself sits). Event records CRC covers the entire event data section.
- **Using `_linux.go` suffix:** CLAUDE.md explicitly prohibits this. Use `_notwindows.go` + `//go:build !windows`.
- **Adding testify or external test deps:** CLAUDE.md: "stdlib only — no testify or external test libraries."

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| CRC32 checksum | Custom polynomial loop | `hash/crc32` with `crc32.IEEETable` | RFC 1952 polynomial; stdlib is correct and tested |
| Little-endian struct serialization | Manual byte shifting | `encoding/binary` with `binary.LittleEndian` | Handles all numeric types; `PutUint32`/`PutUint64` for in-place patching |
| EVTX round-trip validation | Custom EVTX parser for tests | `0xrawsec/golang-evtx` as test-only oracle | Existing battle-tested parser validates file structure |
| File concurrency | Ad-hoc locking | `sync.Mutex` (already the project pattern) | Matches GELFWriter/BeatsWriter; queue workers call WriteEvent concurrently |

**Key insight:** The hard part of EVTX writing is BinXML encoding, not file I/O. A static template approach avoids 70% of the complexity while satisfying OUT-05 and OUT-06.

---

## Common Pitfalls

### Pitfall 1: Incorrect CRC32 Scope for Chunk Header

**What goes wrong:** File rejected by parsers with "corrupt chunk" error.
**Why it happens:** Chunk header CRC32 covers bytes 0-119 AND 128-511, NOT bytes 0-511. The 8 bytes at 120-127 (flags) are included but the CRC field itself (124-127) must be zero when computing.
**How to avoid:** Zero out bytes 120-127 before computing, then patch the CRC at offset 124.
**Warning signs:** 0xrawsec parser returns CRC error; Windows Event Viewer shows "The event log file is corrupted".

### Pitfall 2: Wrong FILETIME Epoch

**What goes wrong:** Event timestamps appear as year ~1601 or distant future in Event Viewer.
**Why it happens:** Confusing Unix epoch (1970) with FILETIME epoch (1601). The delta is 116444736000000000 hundred-nanosecond intervals.
**How to avoid:** Use the constant `filetimeEpochDelta` and always convert via `time.UTC().UnixNano()/100 + delta`.
**Warning signs:** Events show 1601-01-01 or 2106+ timestamps in Event Viewer.

### Pitfall 3: Event Record Size Mismatch

**What goes wrong:** Parser panic or parse failure after the first event.
**Why it happens:** The event record size field at offset 4 AND the size-copy field at the end of the record must both equal the total record length. If BinXML payload grows/shrinks, both must be updated.
**How to avoid:** Assemble the full event record in a `bytes.Buffer`, then patch the size fields once the final length is known.
**Warning signs:** Round-trip test fails on second event; parser returns "unexpected EOF" or "size mismatch".

### Pitfall 4: Empty String UTF-16LE Encoding

**What goes wrong:** Strings appear garbled in Event Viewer; parser extracts empty fields.
**Why it happens:** EVTX strings are UTF-16LE (not UTF-8). String values in BinXML are length-prefixed (character count as uint16) followed by UTF-16LE bytes with a null terminator.
**How to avoid:** Use `unicode/utf16` + `encoding/binary` for all string encoding:

```go
// Source: BinXML spec (libevtx documentation)
import "unicode/utf16"
runes := utf16.Encode([]rune(s))
// write uint16(len(runes)) then each uint16 in LittleEndian, then 0x0000
```

**Warning signs:** Event Viewer shows "?" characters; 0xrawsec extracts empty strings.

### Pitfall 5: File Header "Next Record Identifier" Off-By-One

**What goes wrong:** Forensics tools report record ID gaps.
**Why it happens:** The file header `Next Record Identifier` must equal the last written record ID + 1. It is incremented atomically with each event.
**How to avoid:** Track a monotonically incrementing `recordID uint64` counter guarded by the mutex; write it to the file header on `Close()`.

### Pitfall 6: CGO_ENABLED=0 Incompatibility

**What goes wrong:** Build fails if any dependency uses CGO.
**Why it happens:** The project mandates `CGO_ENABLED=0` (CLAUDE.md). The `0xrawsec/golang-evtx` library must be checked for CGO dependencies before use as a test oracle.
**How to avoid:** Verify `0xrawsec/golang-evtx` compiles with `CGO_ENABLED=0`. If it does not, use `Velocidex/evtx` or `refractionPOINT/evtx` instead.
**Warning signs:** `make test` fails with "cgo not available" or CGO linker errors.

---

## Code Examples

Verified patterns from specifications and stdlib:

### File Header Construction (encoding/binary pattern)

```go
// Source: libevtx specification (github.com/libyal/libevtx) + stdlib encoding/binary
import (
    "encoding/binary"
    "hash/crc32"
)

type evtxFileHeader struct {
    Signature        [8]byte  // "ElfFile\x00"
    FirstChunkNum    uint64
    LastChunkNum     uint64
    NextRecordID     uint64
    HeaderSize       uint32   // always 128
    MinorVersion     uint16   // 1
    MajorVersion     uint16   // 3
    BlockSize        uint16   // 4096
    ChunkCount       uint16
    _                [76]byte // reserved
    Flags            uint32
    CRC32            uint32
    _                [3968]byte // padding to 4096 bytes
}

func buildFileHeader(chunkCount uint16, nextRecordID uint64) []byte {
    buf := make([]byte, 4096)
    copy(buf[0:8], "ElfFile\x00")
    binary.LittleEndian.PutUint64(buf[8:], 0)            // first chunk
    binary.LittleEndian.PutUint64(buf[16:], uint64(chunkCount-1)) // last chunk
    binary.LittleEndian.PutUint64(buf[24:], nextRecordID)
    binary.LittleEndian.PutUint32(buf[32:], 128)          // header size
    binary.LittleEndian.PutUint16(buf[36:], 1)            // minor version
    binary.LittleEndian.PutUint16(buf[38:], 3)            // major version
    binary.LittleEndian.PutUint16(buf[40:], 4096)         // block size
    binary.LittleEndian.PutUint16(buf[42:], chunkCount)
    // CRC32 of bytes 0-119 (with CRC field zeroed)
    crc := crc32.Checksum(buf[0:120], crc32.IEEETable)
    binary.LittleEndian.PutUint32(buf[124:], crc)
    return buf
}
```

### Chunk Header CRC32 (dual-range checksum)

```go
// Source: libevtx specification — CRC covers bytes 0-119 AND 128-511
func patchChunkCRC(chunk []byte) {
    // Zero the CRC field before computing
    binary.LittleEndian.PutUint32(chunk[124:], 0)
    h := crc32.New(crc32.IEEETable)
    h.Write(chunk[0:120])
    h.Write(chunk[128:512])
    binary.LittleEndian.PutUint32(chunk[124:], h.Sum32())
}
```

### FILETIME Conversion

```go
// Source: Windows FILETIME specification
// FILETIME epoch: 1601-01-01 00:00:00 UTC = 11644473600 seconds before Unix epoch
const filetimeEpochDelta = int64(116444736000000000) // 100ns intervals

func toFILETIME(t time.Time) uint64 {
    return uint64(t.UTC().UnixNano()/100 + filetimeEpochDelta)
}
```

### UTF-16LE String Encoding for BinXML

```go
// Source: BinXML value type specification (libevtx)
import "unicode/utf16"

func encodeUTF16LE(s string) []byte {
    u16 := utf16.Encode([]rune(s))
    b := make([]byte, len(u16)*2+2) // +2 for null terminator
    for i, r := range u16 {
        binary.LittleEndian.PutUint16(b[i*2:], r)
    }
    // last 2 bytes are 0x0000 (null terminator)
    return b
}
```

### Event Record Wrapper

```go
// Source: libevtx event record format
const evtxRecordSignature = uint32(0x00002A2A) // "**\x00\x00"

func wrapEventRecord(recordID uint64, timestamp uint64, binXMLPayload []byte) []byte {
    recordSize := uint32(24 + len(binXMLPayload) + 4) // header(24) + payload + size_copy(4)
    buf := make([]byte, recordSize)
    binary.LittleEndian.PutUint32(buf[0:], evtxRecordSignature)
    binary.LittleEndian.PutUint32(buf[4:], recordSize)
    binary.LittleEndian.PutUint64(buf[8:], recordID)
    binary.LittleEndian.PutUint64(buf[16:], timestamp) // FILETIME
    copy(buf[24:], binXMLPayload)
    binary.LittleEndian.PutUint32(buf[recordSize-4:], recordSize) // size copy
    return buf
}
```

### Test Structure (matching CLAUDE.md conventions)

```go
//go:build !windows

package evtx

import (
    "os"
    "testing"
    // test-only oracle — must not appear in production imports
)

func TestBinaryEvtxWriter_RoundTrip(t *testing.T) {
    dir := t.TempDir()
    w, err := NewBinaryEvtxWriter(dir)
    if err != nil {
        t.Fatalf("NewBinaryEvtxWriter: %v", err)
    }
    // ... write events, Close(), parse with oracle, verify EventIDs
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `BinaryEvtxWriter` stub (returns error) | Full implementation with static BinXML template | Phase 7 (v2.0) | OUT-05/OUT-06 satisfied; Linux operators get native .evtx output |
| Per-event BinXML template | Cross-event template sharing (OUT-F01) | Future (deferred) | Would reduce file size ~40%; deferred beyond v2.0 |

**Deferred/out-of-scope per REQUIREMENTS.md:**

- `OUT-F01: BinaryEvtxWriter uses cross-event template sharing` — reduces file size but adds significant complexity; future work.
- Rolling/chunked streaming mid-chunk — explicitly out of scope; flush-on-close is the v2 approach.

---

## Open Questions

1. **CGO compatibility of `0xrawsec/golang-evtx` under `CGO_ENABLED=0`**
   - What we know: The project mandates `CGO_ENABLED=0`; the library has not been verified for this constraint
   - What's unclear: Whether any transitive deps use CGO (e.g., for mmap or syscall wrappers)
   - Recommendation: First task in Phase 7 must be a spike: `CGO_ENABLED=0 go test ./pkg/evtx/ -run TestBinaryEvtxWriter` with the oracle imported; if CGO issues arise, switch to `Velocidex/evtx` or manual hex-dump comparison

2. **Minimum viable BinXML payload for Event Viewer acceptance**
   - What we know: xml2evtx produces valid EVTX from full Event XML; the static template needs to match Windows' expected schema namespace `http://schemas.microsoft.com/win/2004/08/events/event`
   - What's unclear: Whether Event Viewer requires the `Provider` element's `Guid` attribute or accepts `Name` only; whether missing optional fields (Version, Level, Task, Opcode, Keywords) cause display errors
   - Recommendation: Use xml2evtx to generate a reference .evtx from a minimal Event XML, then use that as the static template byte sequence

3. **`evtx_path` config semantics: directory vs. file path**
   - What we know: `OutputConfig.EVTXPath` already exists in `main.go`; the stub used it as `outputDir`
   - What's unclear: Should the writer create one file per session (e.g., `cee-exporter-20260303.evtx`) or accept a literal file path?
   - Recommendation: Treat `evtx_path` as a file path (not a directory) — simpler, no rotation, matches the requirement's description "write native .evtx files". Document in config.toml.example.

4. **Event records CRC32 in chunk header (offset 52)**
   - What we know: The libevtx spec says "CRC32 of event records"; some parsers tolerate a zero value here
   - What's unclear: Whether `0xrawsec/golang-evtx` or Windows Event Viewer validates this field strictly
   - Recommendation: Implement correctly (CRC32 of all event record bytes in the chunk); if causing test failures, investigate tolerance

---

## Sources

### Primary (HIGH confidence)

- [libyal/libevtx EVTX specification](https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc) — file header, chunk header, event record, BinXML token structure
- [MS-EVEN6: Event type schema (Microsoft Learn)](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-even6/8c61aef7-bd4b-4edb-8dfd-3c9a7537886b) — official XML schema for `Event/System` and `Event/EventData`
- `pkg.go.dev/encoding/binary` — stdlib write API (HIGH — stdlib)
- `pkg.go.dev/hash/crc32` — CRC32 IEEE table (HIGH — stdlib)

### Secondary (MEDIUM confidence)

- [JPCERTCC/xml2evtx — Python reference implementation](https://github.com/JPCERTCC/xml2evtx/blob/main/xml2evtx.py) — BinXML token encoding, SDBM hash algorithm, chunk construction patterns; MEDIUM because Python code not directly tested in Go context
- [0xrawsec/golang-evtx](https://github.com/0xrawsec/golang-evtx) — binary constants, chunk/file magic values, token types; MEDIUM because it is a parser (not writer) but defines the same constants
- [Velocidex/evtx chunk structure](https://pkg.go.dev/www.velocidex.com/golang/evtx) — cross-reference for chunk header layout confirmation

### Tertiary (LOW confidence)

- WebSearch results confirming no EVTX writer library exists in Go ecosystem — useful negative result but LOW because absence of evidence
- Windows FILETIME epoch delta value — verified against libevtx and xml2evtx independently (MEDIUM after cross-reference)

---

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — stdlib only; no library choices to get wrong
- Binary format spec: MEDIUM-HIGH — libevtx + MS-EVEN6 are authoritative; xml2evtx provides reference Python impl
- BinXML encoding: MEDIUM — static template approach reduces risk; full encoding remains complex
- Pitfalls: MEDIUM — derived from spec + reference impl analysis; CGO compatibility unverified
- Architecture: HIGH — fits existing codebase patterns exactly (same Writer interface, same mutex pattern, same build tag convention)

**Research date:** 2026-03-03
**Valid until:** 2026-09-03 (EVTX format is stable; stable for 30+ days; check libevtx changelog if format questions arise)
