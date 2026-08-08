---
phase: 07-binaryevtxwriter
plan: GAP-01
type: execute
wave: 1
depends_on: []
files_modified:
  - pkg/evtx/writer_evtx_notwindows.go
  - pkg/evtx/writer_evtx_notwindows_test.go
autonomous: true
gap_closure: true
requirements:
  - OUT-05
  - OUT-06

must_haves:
  truths:
    - "python-evtx / evtxdump parses the generated .evtx file without OverrunBufferException"
    - "BinXML OpenStartElement tokens carry 4-byte chunk-relative NameNode offsets, not inline hashes"
    - "All 11 NameNodes are pre-placed at chunk offset 512, first event record starts at offset 754"
    - "encodeStringValue length field equals exact char count with no extra null in the stream"
    - "go test ./pkg/evtx/ passes with the new NameNode structural test"
  artifacts:
    - path: "pkg/evtx/writer_evtx_notwindows.go"
      provides: "Corrected BinaryEvtxWriter with static NameNode string table"
      contains: "nameTableOffset = 512"
    - path: "pkg/evtx/writer_evtx_notwindows_test.go"
      provides: "NameNode offset and string-table structural tests"
      contains: "TestBinaryEvtxWriter_NameNodeOffsets"
  key_links:
    - from: "buildBinXML"
      to: "nameOffsets map"
      via: "writeOpenElement / writeAttribute use pre-computed chunk offsets"
      pattern: "nameOffsets\\[name\\]"
    - from: "flushToFile"
      to: "evtxChunkHeaderSize + nameTableSize (754)"
      via: "freeSpaceOffset calculation accounts for name table"
      pattern: "nameTableSize|754"
    - from: "buildChunkHeader"
      to: "chunk byte 44 (LastEventRecordDataOffset) and byte 48 (FreeSpaceOffset)"
      via: "freeSpaceOffset = 754 + len(records)"
      pattern: "freeSpaceOffset"
---

<objective>
Fix the BinXML name encoding in BinaryEvtxWriter so that forensics tools (python-evtx,
Chainsaw, Windows Event Viewer) can parse the generated .evtx files without error.

Purpose: The current implementation writes the 4-byte SDBM hash inline in OpenStartElement
and Attribute tokens. EVTX parsers treat that field as a chunk-relative byte offset to a
NameNode in the chunk's string table. When the hash value (e.g. 0x45100b = 4,526,091)
is used as a file offset, the parser seeks beyond the file bounds and throws
OverrunBufferException. This gap closure installs a pre-built static NameNode table
at chunk offset 512 and rewrites the BinXML emitters to reference names by their
correct chunk offsets.

Output: Corrected writer_evtx_notwindows.go + new structural test confirming NameNode
layout and offset correctness.
</objective>

<execution_context>
@/Users/fjacquet/.claude/get-shit-done/workflows/execute-plan.md
@/Users/fjacquet/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/PROJECT.md
@.planning/ROADMAP.md
@.planning/STATE.md
@.planning/phases/07-binaryevtxwriter/07-UAT.md
@.planning/phases/07-binaryevtxwriter/07-02-SUMMARY.md
@pkg/evtx/writer_evtx_notwindows.go
@pkg/evtx/writer_evtx_notwindows_test.go
@pkg/evtx/evtx_binformat.go
</context>

<tasks>

<task type="auto">
  <name>Task 1: Replace inline name hash encoding with static NameNode string table</name>
  <files>pkg/evtx/writer_evtx_notwindows.go</files>
  <action>
Rewrite the BinXML name encoding subsystem in writer_evtx_notwindows.go. Keep all
other code (BinaryEvtxWriter struct, WriteEvent, Close, flushChunkLocked, CRC helpers,
writeUint16LE, writeUint32LE, uint64LEBytes) unchanged.

### Step 1 — Add constants and the name table builder

Add these constants after the existing `binXML*` const block:

```go
// nameTableOffset: byte offset within the chunk where the NameNode table begins.
// Immediately follows the 512-byte chunk header.
const nameTableOffset = uint32(512)

// nameTableSize: total bytes consumed by all 11 pre-built NameNodes (242 bytes).
// Event records begin at chunk offset nameTableOffset + nameTableSize = 754.
const nameTableSize = uint32(242)

// evtxRecordsStart: chunk-relative offset where the first event record is placed.
const evtxRecordsStart = nameTableOffset + nameTableSize // = 754
```

Add the following package-level variable holding pre-computed chunk offsets for each name,
and the function that builds the binary NameNode table:

```go
// nameOffsets maps each element/attribute name to its chunk-relative byte offset.
// These offsets point to the corresponding NameNode in the static name table
// placed at chunk offset 512.
//
// NameNode layout per libevtx spec / python-evtx Nodes.py:
//   [next_offset: 4B LE = 0] [hash: 2B LE, truncated uint16 SDBM] [string_length: 2B LE] [UTF-16LE chars]
//
// Size per node = 4 + 2 + 2 + len(name)*2 bytes.
//
// Pre-computed offsets (base = 512):
//   "Event"       →  512  (5 chars,  18 bytes → next at 530)
//   "System"      →  530  (6 chars,  20 bytes → next at 550)
//   "Provider"    →  550  (8 chars,  24 bytes → next at 574)
//   "Name"        →  574  (4 chars,  16 bytes → next at 590)
//   "EventID"     →  590  (7 chars,  22 bytes → next at 612)
//   "Level"       →  612  (5 chars,  18 bytes → next at 630)
//   "TimeCreated" →  630  (11 chars, 30 bytes → next at 660)
//   "SystemTime"  →  660  (10 chars, 28 bytes → next at 688)
//   "Computer"    →  688  (8 chars,  24 bytes → next at 712)
//   "EventData"   →  712  (9 chars,  26 bytes → next at 738)
//   "Data"        →  738  (4 chars,  16 bytes → next at 754)
var nameOffsets = map[string]uint32{
    "Event":       512,
    "System":      530,
    "Provider":    550,
    "Name":        574,
    "EventID":     590,
    "Level":       612,
    "TimeCreated": 630,
    "SystemTime":  660,
    "Computer":    688,
    "EventData":   712,
    "Data":        738,
}

// buildNameTable returns the 242-byte binary NameNode table.
// The table is placed at chunk offset 512 (immediately after the chunk header).
// Each NameNode: [next_offset(4B)=0][hash(2B)=sdbm16(name)][length(2B)=charCount][UTF-16LE chars]
func buildNameTable() []byte {
    names := []string{
        "Event", "System", "Provider", "Name", "EventID",
        "Level", "TimeCreated", "SystemTime", "Computer", "EventData", "Data",
    }
    buf := &bytes.Buffer{}
    for _, name := range names {
        u16 := utf16.Encode([]rune(name))
        // next_offset: 4 bytes LE = 0 (no chaining)
        buf.WriteByte(0); buf.WriteByte(0); buf.WriteByte(0); buf.WriteByte(0)
        // hash: truncated SDBM uint16 LE
        h := uint16(sdbmHash(name))
        buf.WriteByte(byte(h)); buf.WriteByte(byte(h >> 8))
        // string_length: uint16 LE = number of UTF-16 code units
        n := uint16(len(u16))
        buf.WriteByte(byte(n)); buf.WriteByte(byte(n >> 8))
        // UTF-16LE chars (no null terminator in NameNode)
        for _, c := range u16 {
            buf.WriteByte(byte(c)); buf.WriteByte(byte(c >> 8))
        }
    }
    return buf.Bytes()
}
```

### Step 2 — Rewrite writeOpenElement and writeAttribute

Replace the existing `writeOpenElement` function:

```go
// writeOpenElement writes a BinXML OpenStartElement token.
// The name is encoded as the 4-byte chunk-relative offset to its NameNode.
// depID is the dependency identifier (always 0 for this writer).
func writeOpenElement(b *bytes.Buffer, depID uint16, name string) {
    b.WriteByte(binXMLOpenElement)
    writeUint16LE(b, depID)
    offset := nameOffsets[name]
    writeUint32LE(b, offset)
}
```

Replace the existing `writeAttribute` function:

```go
// writeAttribute writes a BinXML Attribute token followed by a Value token.
// The attribute name is encoded as the 4-byte chunk-relative offset to its NameNode.
// valueType is one of binXMLType* constants. valueBytes is the raw value data.
func writeAttribute(b *bytes.Buffer, name string, valueType byte, valueBytes []byte) {
    b.WriteByte(binXMLAttribute)
    offset := nameOffsets[name]
    writeUint32LE(b, offset)
    b.WriteByte(binXMLValue)
    b.WriteByte(valueType)
    b.Write(valueBytes)
}
```

### Step 3 — Remove writeName

Delete the entire `writeName` function — it is no longer called by anything.

### Step 4 — Fix encodeStringValue (remove extra null terminator byte count)

The BinXML string value format requires:
  [uint16 char_count] [UTF-16LE chars — char_count code units]
The length field must equal the EXACT number of chars (no extra null appended to the
count). A null terminator may follow the chars in the stream, but the length field
must NOT include it.

The current implementation allocates `2 + len(u16)*2 + 2` bytes (includes a null
terminator in the allocation) but the uint16 at offset 0 is `len(u16)` which is
correct. However, the comment in CLAUDE.md says "length field = exact char count,
no null in stream". Remove the null terminator entirely from encodeStringValue output
to match the gap specification:

```go
// encodeStringValue encodes a Go string as a BinXML string value:
// [uint16 char_count LE][UTF-16LE chars — no null terminator].
// The length field equals the exact number of UTF-16 code units.
func encodeStringValue(s string) []byte {
    u16 := utf16.Encode([]rune(s))
    buf := make([]byte, 2+len(u16)*2)
    binary.LittleEndian.PutUint16(buf[0:], uint16(len(u16)))
    for i, v := range u16 {
        binary.LittleEndian.PutUint16(buf[2+i*2:], v)
    }
    return buf
}
```

### Step 5 — Embed NameNode table in flushToFile and adjust freeSpaceOffset

In `flushToFile`, after building `chunkHeader` and before assembling `chunkBytes`,
prepend the name table into the chunk immediately after the header, and adjust the
freeSpaceOffset calculation:

Replace in `flushToFile`:

```go
// OLD (lines ~185-194):
freeSpaceOffset := uint32(evtxChunkHeaderSize + len(records))
chunkHeader := buildChunkHeader(w.firstID, w.recordID-1, 0, freeSpaceOffset)

chunkBytes := make([]byte, evtxChunkSize)
copy(chunkBytes[0:], chunkHeader)
copy(chunkBytes[evtxChunkHeaderSize:], records)

// Patch event records CRC32 into chunk header at offset 52.
patchEventRecordsCRC(chunkBytes, evtxChunkHeaderSize, evtxChunkHeaderSize+len(records))
```

With:

```go
// Name table is placed at chunk offset 512 (immediately after the 512-byte header).
// Event records follow at chunk offset 754 (512 + 242).
nameTable := buildNameTable()
recordsStart := int(evtxChunkHeaderSize) + len(nameTable) // = 754
freeSpaceOffset := uint32(recordsStart + len(records))
chunkHeader := buildChunkHeader(w.firstID, w.recordID-1, 0, freeSpaceOffset)

chunkBytes := make([]byte, evtxChunkSize)
copy(chunkBytes[0:], chunkHeader)
copy(chunkBytes[evtxChunkHeaderSize:], nameTable)
copy(chunkBytes[recordsStart:], records)

// Patch event records CRC32 into chunk header at offset 52.
// Covers only the event record bytes (not the name table).
patchEventRecordsCRC(chunkBytes, recordsStart, recordsStart+len(records))
```

### Step 6 — Also fix maxRecords calculation in flushToFile

The available space for records is now smaller because the name table occupies 242 bytes:

Replace:

```go
maxRecords := evtxChunkSize - evtxChunkHeaderSize
```

With:

```go
maxRecords := evtxChunkSize - int(evtxChunkHeaderSize) - int(nameTableSize)
```

### Verification of changes

After editing, confirm:

- `writeName` function is gone (grep for it)
- `writeOpenElement` uses `nameOffsets[name]` for the 4-byte field
- `writeAttribute` uses `nameOffsets[name]` for the 4-byte field
- `encodeStringValue` no longer has `+2` for null terminator
- `flushToFile` copies `nameTable` at offset `evtxChunkHeaderSize`
- `buildNameTable()` function exists and is exported-package-visible
  </action>
  <verify>
Run: go build ./pkg/evtx/
Run: go vet ./pkg/evtx/
Both must exit 0 with no errors. Confirm writeName is absent:
  grep -n "func writeName" pkg/evtx/writer_evtx_notwindows.go
must return no output.
  </verify>
  <done>
Package compiles cleanly. writeName is deleted. writeOpenElement and writeAttribute
emit 4-byte chunk-relative offsets from nameOffsets map. encodeStringValue has no
null terminator. flushToFile places the 242-byte name table at chunk offset 512
with event records starting at offset 754.
  </done>
</task>

<task type="auto">
  <name>Task 2: Add NameNode structural tests and run full test suite</name>
  <files>pkg/evtx/writer_evtx_notwindows_test.go</files>
  <action>
Add two new test functions to writer_evtx_notwindows_test.go, then run the full suite.

### Test 1: TestBinaryEvtxWriter_NameNodeOffsets

Verifies that buildNameTable() produces exactly 242 bytes, that the declared offsets
in nameOffsets match the actual byte positions in the table, and that each NameNode's
UTF-16LE name can be read back correctly.

```go
// TestBinaryEvtxWriter_NameNodeOffsets verifies the static name table layout:
// - Total size == nameTableSize (242 bytes)
// - Each entry in nameOffsets points to the correct NameNode in the table
// - The NameNode at each offset decodes to the expected name
func TestBinaryEvtxWriter_NameNodeOffsets(t *testing.T) {
    table := buildNameTable()
    if uint32(len(table)) != nameTableSize {
        t.Fatalf("name table size mismatch: got %d bytes, want %d", len(table), nameTableSize)
    }

    names := []string{
        "Event", "System", "Provider", "Name", "EventID",
        "Level", "TimeCreated", "SystemTime", "Computer", "EventData", "Data",
    }

    for _, name := range names {
        chunkOffset, ok := nameOffsets[name]
        if !ok {
            t.Errorf("name %q missing from nameOffsets", name)
            continue
        }
        // Convert chunk offset to table-relative index (table starts at chunk offset 512)
        tableIdx := int(chunkOffset) - int(nameTableOffset)
        if tableIdx < 0 || tableIdx+8 > len(table) {
            t.Errorf("name %q: chunk offset %d maps to table index %d (out of range)", name, chunkOffset, tableIdx)
            continue
        }
        // NameNode layout: [next(4B)][hash(2B)][length(2B)][UTF-16LE chars]
        // Read string_length at tableIdx+6
        strLen := int(table[tableIdx+6]) | int(table[tableIdx+7])<<8
        if strLen != len([]rune(name)) {
            t.Errorf("name %q: NameNode string_length = %d, want %d", name, strLen, len([]rune(name)))
            continue
        }
        // Decode UTF-16LE from tableIdx+8
        u16Bytes := table[tableIdx+8 : tableIdx+8+strLen*2]
        decoded := make([]uint16, strLen)
        for i := 0; i < strLen; i++ {
            decoded[i] = uint16(u16Bytes[i*2]) | uint16(u16Bytes[i*2+1])<<8
        }
        got := string(utf16.Decode(decoded))
        if got != name {
            t.Errorf("name %q: NameNode decoded as %q", name, got)
        }
    }
}
```

Add `"unicode/utf16"` to the import block (it is already in the production file; add it
to the test file import if not already present).

### Test 2: TestBinaryEvtxWriter_ChunkLayout

Verifies that the generated .evtx chunk has the name table at the correct byte offset
and that event records start at offset 754 (relative to the chunk start, which is at
evtxFileHeaderSize = 4096 in the file).

```go
// TestBinaryEvtxWriter_ChunkLayout verifies the binary layout of the generated chunk:
// - Name table starts at byte 512 of the chunk (byte 4608 of the file)
// - The first event record starts at byte 754 of the chunk (byte 4850 of the file)
// - The first event record begins with the EVTX record signature 0x00002A2A
func TestBinaryEvtxWriter_ChunkLayout(t *testing.T) {
    dir := t.TempDir()
    outPath := filepath.Join(dir, "layout.evtx")

    w, err := NewBinaryEvtxWriter(outPath)
    if err != nil {
        t.Fatalf("NewBinaryEvtxWriter: %v", err)
    }

    e := WindowsEvent{
        EventID:        4663,
        TimeCreated:    time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC),
        Computer:       "testhost",
        ProviderName:   "Microsoft-Windows-Security-Auditing",
        ObjectName:     "/nas/share/file.txt",
        SubjectUserSID: "S-1-5-21-123",
        SubjectUsername: "testuser",
        SubjectDomain:  "DOMAIN",
        AccessMask:     "0x2",
        CEPAEventType:  "CEPP_FILE_WRITE",
    }
    if err := w.WriteEvent(context.Background(), e); err != nil {
        t.Fatalf("WriteEvent: %v", err)
    }
    if err := w.Close(); err != nil {
        t.Fatalf("Close: %v", err)
    }

    data, err := os.ReadFile(outPath)
    if err != nil {
        t.Fatalf("ReadFile: %v", err)
    }

    // Chunk starts at evtxFileHeaderSize (4096).
    chunkStart := evtxFileHeaderSize
    // Name table at chunk offset 512 → file offset 4608.
    nameTableFileOffset := chunkStart + int(nameTableOffset)
    if len(data) < nameTableFileOffset+int(nameTableSize) {
        t.Fatalf("file too short to contain name table: got %d bytes", len(data))
    }

    // First NameNode at file offset 4608 should decode "Event" (5 chars, UTF-16LE).
    // NameNode: [next(4)][hash(2)][length(2)][UTF-16LE chars]
    nn := data[nameTableFileOffset:]
    strLen := int(nn[6]) | int(nn[7])<<8
    if strLen != 5 {
        t.Errorf("first NameNode string_length = %d, want 5 ('Event')", strLen)
    }

    // Event record starts at chunk offset 754 → file offset 4096 + 754 = 4850.
    recordFileOffset := chunkStart + int(evtxRecordsStart)
    if len(data) < recordFileOffset+4 {
        t.Fatalf("file too short to reach first record signature: %d bytes", len(data))
    }

    sig := uint32(data[recordFileOffset]) |
        uint32(data[recordFileOffset+1])<<8 |
        uint32(data[recordFileOffset+2])<<16 |
        uint32(data[recordFileOffset+3])<<24
    if sig != evtxRecordSignature {
        t.Errorf("first record signature at offset %d: got 0x%08x, want 0x%08x",
            recordFileOffset, sig, evtxRecordSignature)
    }
}
```

Add `"unicode/utf16"` to the import block of the test file if not already present.
Export `evtxRecordsStart` from the production file (it is a package-level const,
accessible within `package evtx` white-box tests without export).

### Run the full test suite

```bash
go test ./pkg/evtx/ -v -count=1
```

All existing tests must pass. The two new tests must also pass.
  </action>
  <verify>
Run: go test ./pkg/evtx/ -v -count=1
Expected output:
  --- PASS: TestBinaryEvtxWriter_NameNodeOffsets
  --- PASS: TestBinaryEvtxWriter_ChunkLayout
  --- PASS: TestBinaryEvtxWriter_WriteClose
  --- PASS: TestBinaryEvtxWriter_EmptyClose
  --- PASS: TestBinaryEvtxWriter_Concurrent
  --- PASS: TestBinaryEvtxWriter_EmptyPath
  --- PASS: TestBinaryEvtxWriter_ParentDirCreated
  PASS

Then run: make test
Must exit 0 (all packages).

Then run: make lint (or go vet ./...)
Must exit 0.
  </verify>
  <done>
All 7+ tests pass. TestBinaryEvtxWriter_NameNodeOffsets confirms: table is exactly
242 bytes, all 11 names decode correctly from their pre-computed chunk offsets.
TestBinaryEvtxWriter_ChunkLayout confirms: name table at chunk offset 512, first
record signature at chunk offset 754. make test and make lint both exit 0.
  </done>
</task>

</tasks>

<verification>
Full verification sequence after both tasks complete:

1. go build ./... — must succeed CGO_ENABLED=0
2. go vet ./pkg/evtx/ — must exit 0
3. go test ./pkg/evtx/ -v -count=1 — all tests pass including the two new structural tests
4. make test — all packages pass
5. Structural confirmation:
   - grep -n "func writeName" pkg/evtx/writer_evtx_notwindows.go → no output (deleted)
   - grep -n "nameOffsets\[" pkg/evtx/writer_evtx_notwindows.go → appears in writeOpenElement and writeAttribute
   - grep -n "nameTable" pkg/evtx/writer_evtx_notwindows.go → appears in flushToFile
   - grep -n "evtxRecordsStart\|754" pkg/evtx/writer_evtx_notwindows.go → appears in flushToFile
6. Manual smoke test (optional, closes UAT test 6):
   - Build: make build
   - Run daemon with evtx output: ./cee-exporter -config config.toml (type="evtx", evtx_path="/tmp/audit.evtx")
   - Send a synthetic event
   - Stop daemon: Ctrl-C
   - If python-evtx is available: python3 -m evtx /tmp/audit.evtx → no OverrunBufferException
</verification>

<success_criteria>

- pkg/evtx/writer_evtx_notwindows.go: writeName deleted; writeOpenElement and
  writeAttribute emit 4-byte nameOffsets[name] chunk-relative offsets; buildNameTable()
  produces 242-byte table; flushToFile places name table at chunk offset 512 and
  records at offset 754; encodeStringValue has no null terminator in output.
- pkg/evtx/writer_evtx_notwindows_test.go: TestBinaryEvtxWriter_NameNodeOffsets and
  TestBinaryEvtxWriter_ChunkLayout added and passing.
- go test ./pkg/evtx/ -count=1 exits 0 with all tests green.
- make test exits 0 (no regressions in other packages).
- GAP-1 from 07-UAT.md is closed: BinXML tokens now emit chunk-relative NameNode
  offsets rather than inline hash values.
</success_criteria>

<output>
After completion, create `.planning/phases/07-binaryevtxwriter/07-GAP-01-SUMMARY.md`
with:
- What was changed (writeName removed, nameOffsets map added, buildNameTable added,
  writeOpenElement/writeAttribute rewritten, encodeStringValue trimmed, flushToFile
  updated)
- Key offsets confirmed: nameTableOffset=512, nameTableSize=242, evtxRecordsStart=754
- Tests added: TestBinaryEvtxWriter_NameNodeOffsets, TestBinaryEvtxWriter_ChunkLayout
- Verification results (all tests pass)
- GAP-1 status: CLOSED
</output>
