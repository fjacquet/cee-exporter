---
status: passed
phase: 07-binaryevtxwriter
source: 07-01-SUMMARY.md, 07-02-SUMMARY.md
started: 2026-03-03T22:00:00Z
updated: 2026-03-04T05:35:00Z
---

## Current Test

number: 6
name: Forensics tool parses the file
expected: |
  Run evtxdump (or python-evtx / Chainsaw) on /tmp/audit.evtx.
  Tool outputs event records with correct EventIDs and timestamps.
  No parse error about corrupt data.
result: pass — python-evtx parses all records, renders full XML with correct EventIDs
  (4663, 4660, 4670), element names, attribute values, and substitution data.
  Template-based BinXML with inline NameNodes confirmed working.
awaiting: none

## Tests

### 1. Start daemon with evtx output configured
expected: Edit config.toml to set type = "evtx" and evtx_path = "/tmp/test-audit.evtx". Run ./cee-exporter -config config.toml. Daemon starts and logs ready — no crash or error about the evtx writer.
result: pass

### 2. File created after sending an event
expected: With daemon running, send a PUT request simulating a CEPA event (or use the RegisterRequest handshake). After calling Close (e.g. stop the daemon with Ctrl-C), /tmp/test-audit.evtx exists and has non-zero size.
result: pass

### 3. EVTX magic bytes correct
expected: Run `xxd /tmp/test-audit.evtx | head -1`. The first 8 bytes should be `45 6c 66 46 69 6c 65 00` (ElfFile\x00). The file is recognisable as a valid EVTX file.
result: pass

### 4. File header CRC32 valid
expected: Run `go test ./pkg/evtx/ -run TestBinaryEvtxWriter_WriteClose -v`. All assertions pass, including the file header CRC32 check. No "CRC mismatch" failure.
result: pass

### 5. Windows Event Viewer opens the file
expected: Copy /tmp/test-audit.evtx to a Windows machine. Open eventvwr.msc → Action → Open Saved Log → select the file. Event Viewer displays a list of events with EventIDs 4663, 4660, or 4670 and readable Computer/Subject fields. No "The file is not a valid event log file" error.
result: skipped — no Windows machine available during this session

### 6. Forensics tool parses the file
expected: Run evtxdump (or python-evtx / Chainsaw) on /tmp/test-audit.evtx. Tool outputs event records with correct EventIDs and timestamps. No parse error about corrupt data.
result: pass — python-evtx 0.8.x successfully parses all records. Template-based BinXML
  with TemplateInstanceNode (0x0C) + NormalSubstitution (0x0D) tokens renders full XML
  including Provider, EventID, Level, TimeCreated, Computer, and 12 EventData fields.
  Three event types tested: CEPP_FILE_WRITE (4663), CEPP_DELETE_FILE (4660), CEPP_SETACL_FILE (4670).

## Summary

total: 6
passed: 5
issues: 0
pending: 0
skipped: 1

## Gaps

### GAP-1: BinXML name encoding uses inline hash instead of chunk-relative NameNode offset

**File:** `pkg/evtx/writer_evtx_notwindows.go`

**Root cause:**
The EVTX BinXML format requires that OpenStartElement and Attribute tokens encode
element/attribute names as a **4-byte chunk-relative offset** pointing to a NameNode
in the chunk's string table. Our implementation instead writes the 4-byte SDBM hash
inline, which parsers (python-evtx, Windows Event Viewer) interpret as a file offset,
causing an OverrunBufferException.

**Required fix:**
1. Pre-build a static NameNode string table for all 11 unique names used by the writer.
   Place it at chunk offset 512 (immediately after the 512-byte chunk header).
2. Each NameNode format (per python-evtx Nodes.py / libevtx spec):
     [next_offset: 4B LE = 0]  [hash: 2B LE, truncated SDBM uint16]
     [string_length: 2B LE]    [UTF-16LE chars: string_length * 2 bytes]
3. Rewrite writeOpenElement / writeAttribute to emit the 4-byte chunk-relative
   offset to the corresponding pre-placed NameNode instead of calling writeName.
4. Remove writeName (no longer needed).
5. Adjust flushToFile so event records start at chunk offset 512 + len(nameTable).
6. Update buildChunkHeader FreeSpaceOffset accordingly.

**Names and pre-computed chunk offsets (base = 512):**
  Event(512), System(530), Provider(550), Name(574), EventID(590),
  Level(612), TimeCreated(630), SystemTime(660), Computer(688),
  EventData(712), Data(738) — first record at 754.

**Gap plan file:** 07-GAP-01-PLAN.md
