# ADR-009: Implement BinaryEvtxWriter from scratch (no library exists)

**Status:** accepted

## Context

v1 ships a `BinaryEvtxWriter` stub that returns an error on every call (see ADR-004).
v2.0 aims to replace the stub with a working implementation that produces `.evtx` files
on Linux that Windows Event Viewer, Splunk, Elastic Agent, and forensics tools can open.

A prerequisite research phase investigated existing Go libraries for EVTX writing:

| Library | Verdict |
|---------|---------|
| `github.com/0xrawsec/golang-evtx` | Parser only — no write capability |
| `www.velocidex.com/golang/evtx` | Parser only |
| `github.com/Velocidex/evtx` | Parser only |

**No pure-Go EVTX writer library exists in the Go ecosystem as of 2026-03.**

## Decision

Implement `BinaryEvtxWriter` from scratch in `pkg/evtx/` using only Go standard
library packages:

| Package | Purpose |
|---------|---------|
| `encoding/binary` | Little-endian binary struct serialisation |
| `hash/crc32` | Chunk checksum calculation (CRC32 ANSI polynomial) |
| `bytes` | In-memory buffer for chunk assembly before flush |
| `unicode/utf16` | BinXML stores strings as UTF-16LE |
| `os` / `io` | File write handle and rotation |

The implementation targets the EVTX format as documented in:

- [MS-EVEN6] Windows XML Event Log binary format specification
- `libyal/libevtx` ASCIIDOC format documentation

**Minimum viable structure:**

1. EVTX file header (4 096 bytes): magic `ElfFile\x00`, chunk count, CRC32 of first 120 bytes.
2. One chunk per N events or per file rotation (65 536 bytes): chunk header `ElfChnk\x00`,
   string table, template pointers, CRC32.
3. Each `WindowsEvent` encoded as a self-contained BinXML fragment (no cross-event
   template sharing in v2 — larger on disk but simpler to implement and verify).
4. File rotation when a chunk is full or a configurable interval elapses.

The writer must implement the existing `evtx.Writer` interface (`WriteEvent`, `Close`).
It carries the build tag `//go:build !windows` — on Windows, the `Win32EventLogWriter`
writes directly to the Windows Security log, making a file-based writer redundant.

**Verification strategy:** The test suite round-trips generated `.evtx` files through
`0xrawsec/golang-evtx` (parser only, safe to use as a test dependency) to validate
that every written event can be parsed back correctly.

## Consequences

- No new production dependencies added to `go.mod`. Test-only dependency on
  `0xrawsec/golang-evtx`.
- Implementation is estimated at 600–1 200 LOC — the highest-complexity feature in v2.0.
  It is assigned its own roadmap phase.
- The self-contained BinXML approach (no cross-event template sharing) produces larger
  files than an optimised template-based encoder. Template optimisation is deferred
  to a future minor release.
- File rotation is an operational necessity (EVTX files are not append-unlimited);
  operators must configure `evtx_rotation_size` or `evtx_rotation_interval`.
- `CGO_ENABLED=0` constraint is satisfied — all packages used are stdlib.
