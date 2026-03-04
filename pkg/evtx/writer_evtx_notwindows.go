//go:build !windows

// BinaryEvtxWriter — full implementation for non-Windows platforms.
//
// Writes valid binary .evtx files with template-based BinXML encoding.
// The BinXML uses TemplateInstanceNode (0x0C) with NormalSubstitution
// tokens (0x0D), matching the format required by python-evtx's xml()
// rendering and other forensics parsers.
//
// Template structure per MS-EVEN6 / libevtx:
//
//	FragmentHeader → TemplateInstanceNode → TemplateNode (header + body) → SubstitutionArray
//
// The template body contains the XML structure with substitution placeholders.
// The substitution array carries the actual event values (strings, integers, timestamps).
package evtx

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf16"
)

// BinXML token type constants (per libevtx / MS-EVEN6 specification).
const (
	binXMLFragmentHeader     = 0x0F // Fragment header token
	binXMLOpenElement        = 0x01 // Open start element (no attrs)
	binXMLOpenElementAttrs   = 0x41 // Open start element with attribute list flag
	binXMLCloseElement       = 0x02 // Close start element tag
	binXMLEndElement         = 0x04 // End element tag
	binXMLAttribute          = 0x06 // Attribute token
	binXMLTemplateInstance   = 0x0C // Template instance token
	binXMLNormalSubstitution = 0x0D // Normal substitution token

	binXMLTypeString   = 0x01 // Value type: UTF-16LE string (WSTRING)
	binXMLTypeUint16   = 0x06 // Value type: uint16 (UNSIGNED_WORD)
	binXMLTypeFiletime = 0x11 // Value type: FILETIME (uint64)
)

// evtxRecordsStart: chunk-relative offset where the first event record is placed.
// python-evtx hardcodes first_record() at chunk offset 0x200 (512).
const evtxRecordsStart = uint32(evtxChunkHeaderSize) // = 512

// evtxRecordHeaderSize is the fixed size of an event record header:
// signature(4) + size(4) + recordID(8) + timestamp(8) = 24 bytes.
const evtxRecordHeaderSize = 24

// chunkFlushThreshold: flush and start a new chunk when buffered records exceed this.
const chunkFlushThreshold = 60000

// substitutionEntry holds one substitution value for the BinXML template.
type substitutionEntry struct {
	typ  byte   // BinXML value type (binXMLTypeString, etc.)
	data []byte // raw value bytes
}

// BinaryEvtxWriter writes Windows .evtx binary format files on non-Windows platforms.
// All exported methods are safe for concurrent use.
type BinaryEvtxWriter struct {
	mu       sync.Mutex
	path     string // output file path (e.g. "/var/log/cee-exporter.evtx")
	records  []byte // accumulated event record bytes for current chunk
	recordID uint64 // monotonically incrementing record ID, starts at 1
	firstID  uint64 // first record ID in current chunk
}

// NewBinaryEvtxWriter creates a BinaryEvtxWriter that will write to evtxPath.
//
// evtxPath is the output file path (not a directory). The parent directory is
// created if it does not exist. The file itself is only written on Close().
func NewBinaryEvtxWriter(evtxPath string) (*BinaryEvtxWriter, error) {
	if evtxPath == "" {
		return nil, fmt.Errorf("binary_evtx_writer: evtxPath must be non-empty")
	}
	if err := os.MkdirAll(filepath.Dir(evtxPath), 0o755); err != nil {
		return nil, fmt.Errorf("binary_evtx_writer: create parent directory: %w", err)
	}
	slog.Info("binary_evtx_writer_ready", "path", evtxPath)
	return &BinaryEvtxWriter{
		path:     evtxPath,
		recordID: 1,
		firstID:  1,
	}, nil
}

// WriteEvent encodes e as a BinXML event record and buffers it.
func (w *BinaryEvtxWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Compute chunk-relative offset where this record's BinXML payload starts.
	binXMLChunkOffset := evtxRecordsStart + uint32(len(w.records)) + evtxRecordHeaderSize

	payload := buildBinXML(e, binXMLChunkOffset)
	ts := toFILETIME(e.TimeCreated)
	rec := wrapEventRecord(w.recordID, ts, payload)
	w.records = append(w.records, rec...)
	w.recordID++

	slog.Debug("binary_evtx_event_buffered",
		"event_id", e.EventID,
		"record_id", w.recordID-1,
		"buffer_bytes", len(w.records),
	)

	if len(w.records) > chunkFlushThreshold {
		if err := w.flushChunkLocked(); err != nil {
			return fmt.Errorf("binary_evtx_writer: mid-stream chunk flush: %w", err)
		}
	}
	return nil
}

// Close flushes all buffered events to disk and finalises the .evtx file.
func (w *BinaryEvtxWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.records) == 0 {
		slog.Warn("binary_evtx_writer_closed_empty", "path", w.path)
		return nil
	}
	return w.flushToFile()
}

// buildChunkHeader constructs the 512-byte EVTX chunk header.
func buildChunkHeader(firstRecordID, lastRecordID uint64, _ uint16, freeSpaceOffset uint32) []byte {
	buf := make([]byte, evtxChunkHeaderSize)
	copy(buf[0:8], evtxChunkMagic)
	binary.LittleEndian.PutUint64(buf[8:], firstRecordID)    // FirstEventRecordNumber
	binary.LittleEndian.PutUint64(buf[16:], lastRecordID)    // LastEventRecordNumber
	binary.LittleEndian.PutUint64(buf[24:], firstRecordID)   // FirstEventRecordIdentifier
	binary.LittleEndian.PutUint64(buf[32:], lastRecordID)    // LastEventRecordIdentifier
	binary.LittleEndian.PutUint32(buf[40:], 128)             // HeaderSize
	binary.LittleEndian.PutUint32(buf[44:], freeSpaceOffset) // LastEventRecordDataOffset
	binary.LittleEndian.PutUint32(buf[48:], freeSpaceOffset) // FreeSpaceOffset
	return buf
}

// patchEventRecordsCRC computes CRC32 over the event records region.
func patchEventRecordsCRC(chunk []byte, recordsStart, recordsEnd int) {
	crc := crc32.Checksum(chunk[recordsStart:recordsEnd], crc32.IEEETable)
	binary.LittleEndian.PutUint32(chunk[52:], crc)
}

// flushChunkLocked is a placeholder for multi-chunk support.
func (w *BinaryEvtxWriter) flushChunkLocked() error {
	slog.Warn("binary_evtx_chunk_boundary_reached",
		"path", w.path,
		"buffered_bytes", len(w.records),
	)
	return nil
}

// flushToFile assembles the complete single-chunk .evtx file and writes it.
func (w *BinaryEvtxWriter) flushToFile() error {
	maxRecords := evtxChunkSize - int(evtxRecordsStart)
	records := w.records
	if len(records) > maxRecords {
		slog.Warn("binary_evtx_records_truncated",
			"path", w.path,
			"total_bytes", len(records),
			"max_bytes", maxRecords,
		)
		records = records[:maxRecords]
	}

	recordsStart := int(evtxRecordsStart)
	freeSpaceOffset := uint32(recordsStart + len(records))
	chunkHeader := buildChunkHeader(w.firstID, w.recordID-1, 0, freeSpaceOffset)

	chunkBytes := make([]byte, evtxChunkSize)
	copy(chunkBytes[0:], chunkHeader)
	copy(chunkBytes[recordsStart:], records)

	patchEventRecordsCRC(chunkBytes, recordsStart, recordsStart+len(records))
	patchChunkCRC(chunkBytes)

	fileHeader := buildFileHeader(1, w.recordID)
	fileBytes := append(fileHeader, chunkBytes...)
	if err := os.WriteFile(w.path, fileBytes, 0o644); err != nil {
		return fmt.Errorf("binary_evtx_writer: write file: %w", err)
	}

	slog.Info("binary_evtx_file_written",
		"path", w.path,
		"records", w.recordID-w.firstID,
	)
	return nil
}

// ---------------------------------------------------------------------------
// Template-based BinXML encoding
// ---------------------------------------------------------------------------
//
// BinXML layout per MS-EVEN6 / python-evtx:
//
//   [FragmentHeader: 4B]
//   [TemplateInstanceNode: 10B]
//   [TemplateNode header: 24B]
//   [Template body: variable — XML structure with NormalSubstitution tokens]
//   [Substitution array: count + value_specs + value_data]
//
// python-evtx's xml() → render_root_node() reads:
//   1. RootNode finds FragmentHeader, then TemplateInstanceNode
//   2. TemplateInstanceNode loads resident TemplateNode (inline)
//   3. RootNode.substitutions() reads the sub array after the template
//   4. render_root_node_with_subs() walks template body, replaces subs → XML
// ---------------------------------------------------------------------------

const (
	fragHeaderSize   = 4  // 0x0F + major + minor + flags
	templInstSize    = 10 // token + unknown0 + template_id + template_offset
	templNodeHdrSize = 24 // next_offset(4) + GUID(16, first 4B = template_id) + data_length(4)
	preambleSize     = fragHeaderSize + templInstSize + templNodeHdrSize // 38
)

// buildBinXML encodes a WindowsEvent as template-based BinXML.
//
// binXMLChunkOffset is the chunk-relative byte offset where this BinXML
// payload starts (used for inline NameNode offset calculations).
func buildBinXML(e WindowsEvent, binXMLChunkOffset uint32) []byte {
	// Template body starts after: fragment header + template instance + template node header.
	templateBodyBase := binXMLChunkOffset + preambleSize

	// Build template body with substitution placeholders.
	tbody := buildTemplateBody(templateBodyBase)

	// Collect actual substitution values from the event.
	subs := collectSubstitutions(e)

	out := &bytes.Buffer{}

	// 1. Fragment header (4 bytes).
	out.WriteByte(binXMLFragmentHeader)
	out.WriteByte(0x01) // major version
	out.WriteByte(0x00) // minor version
	out.WriteByte(0x00) // flags

	// 2. TemplateInstanceNode (10 bytes).
	out.WriteByte(binXMLTemplateInstance) // token 0x0C
	out.WriteByte(0x01)                  // unknown0
	writeUint32LE(out, 1)                // template_id
	// template_offset: chunk-relative offset of the TemplateNode (right after this node).
	writeUint32LE(out, binXMLChunkOffset+fragHeaderSize+templInstSize)

	// 3. TemplateNode header (24 bytes).
	// python-evtx layout: next_offset(4) + GUID(16, first 4B also = template_id) + data_length(4).
	// tag_length() in python-evtx returns 0x18 = 24.
	writeUint32LE(out, 0) // next_offset (no chaining)
	writeUint32LE(out, 1) // GUID bytes [0:4] (= template_id)
	out.Write(make([]byte, 12)) // GUID bytes [4:16] (zeros)
	writeUint32LE(out, uint32(len(tbody))) // data_length

	// 4. Template body.
	out.Write(tbody)

	// 5. Substitution array.
	writeSubstitutionArray(out, subs)

	return out.Bytes()
}

// buildTemplateBody constructs the BinXML template body with NormalSubstitution
// tokens (0x0D) as placeholders for event values.
//
// Substitution indices:
//
//	0:  ProviderName  (STRING)
//	1:  EventID       (UINT16)
//	2:  Level         (UINT16)
//	3:  SystemTime    (FILETIME)
//	4:  Computer      (STRING)
//	5+2i:  Data[i] Name attr  (STRING)   — 12 data fields
//	6+2i:  Data[i] value      (STRING)
//
// Total: 5 + 12*2 = 29 substitutions.
func buildTemplateBody(baseOffset uint32) []byte {
	b := &bytes.Buffer{}

	// <Event>
	writeOpenElement(b, "Event", false, baseOffset)
	b.WriteByte(binXMLCloseElement)

	//   <System>
	writeOpenElement(b, "System", false, baseOffset)
	b.WriteByte(binXMLCloseElement)

	//     <Provider Name="%0"/>
	writeOpenElement(b, "Provider", true, baseOffset)
	writeAttributeSub(b, "Name", 0, binXMLTypeString, baseOffset)
	b.WriteByte(binXMLCloseElement)
	b.WriteByte(binXMLEndElement)

	//     <EventID>%1</EventID>
	writeOpenElement(b, "EventID", false, baseOffset)
	b.WriteByte(binXMLCloseElement)
	writeSubstitution(b, 1, binXMLTypeUint16)
	b.WriteByte(binXMLEndElement)

	//     <Level>%2</Level>
	writeOpenElement(b, "Level", false, baseOffset)
	b.WriteByte(binXMLCloseElement)
	writeSubstitution(b, 2, binXMLTypeUint16)
	b.WriteByte(binXMLEndElement)

	//     <TimeCreated SystemTime="%3"/>
	writeOpenElement(b, "TimeCreated", true, baseOffset)
	writeAttributeSub(b, "SystemTime", 3, binXMLTypeFiletime, baseOffset)
	b.WriteByte(binXMLCloseElement)
	b.WriteByte(binXMLEndElement)

	//     <Computer>%4</Computer>
	writeOpenElement(b, "Computer", false, baseOffset)
	b.WriteByte(binXMLCloseElement)
	writeSubstitution(b, 4, binXMLTypeString)
	b.WriteByte(binXMLEndElement)

	//   </System>
	b.WriteByte(binXMLEndElement)

	//   <EventData>
	writeOpenElement(b, "EventData", false, baseOffset)
	b.WriteByte(binXMLCloseElement)

	//     12 Data elements: <Data Name="%N">%N+1</Data>
	for i := 0; i < 12; i++ {
		nameIdx := uint16(5 + i*2)
		valueIdx := uint16(6 + i*2)
		writeOpenElement(b, "Data", true, baseOffset)
		writeAttributeSub(b, "Name", nameIdx, binXMLTypeString, baseOffset)
		b.WriteByte(binXMLCloseElement)
		writeSubstitution(b, valueIdx, binXMLTypeString)
		b.WriteByte(binXMLEndElement)
	}

	//   </EventData>
	b.WriteByte(binXMLEndElement)

	// </Event>
	b.WriteByte(binXMLEndElement)

	// EndOfStream (terminates template body's _children loop).
	b.WriteByte(0x00)

	return b.Bytes()
}

// collectSubstitutions gathers all 29 substitution values from a WindowsEvent.
func collectSubstitutions(e WindowsEvent) []substitutionEntry {
	dataFields := []struct {
		name  string
		value string
	}{
		{"SubjectUserSid", e.SubjectUserSID},
		{"SubjectUserName", e.SubjectUsername},
		{"SubjectDomainName", e.SubjectDomain},
		{"SubjectLogonId", e.SubjectLogonID},
		{"ObjectServer", "Security"},
		{"ObjectType", e.ObjectType},
		{"ObjectName", e.ObjectName},
		{"HandleId", e.HandleID},
		{"AccessList", e.Accesses},
		{"AccessMask", e.AccessMask},
		{"ProcessId", ""},
		{"ProcessName", ""},
	}

	subs := make([]substitutionEntry, 0, 29)

	// Sub 0: ProviderName (STRING)
	subs = append(subs, substitutionEntry{binXMLTypeString, encodeSubString(e.ProviderName)})
	// Sub 1: EventID (UINT16)
	subs = append(subs, substitutionEntry{binXMLTypeUint16, uint16LEBytes(uint16(e.EventID))})
	// Sub 2: Level (UINT16)
	subs = append(subs, substitutionEntry{binXMLTypeUint16, uint16LEBytes(0)})
	// Sub 3: SystemTime (FILETIME)
	subs = append(subs, substitutionEntry{binXMLTypeFiletime, uint64LEBytes(toFILETIME(e.TimeCreated))})
	// Sub 4: Computer (STRING)
	subs = append(subs, substitutionEntry{binXMLTypeString, encodeSubString(e.Computer)})

	// Sub 5..28: Data field names and values (pairs).
	for _, df := range dataFields {
		subs = append(subs, substitutionEntry{binXMLTypeString, encodeSubString(df.name)})
		subs = append(subs, substitutionEntry{binXMLTypeString, encodeSubString(df.value)})
	}

	return subs
}

// writeSubstitutionArray writes the substitution array after the template body.
//
// Format:
//
//	[count: 4B LE]
//	[value_spec × count: WORD size + BYTE type + BYTE pad = 4B each]
//	[value_data: concatenated raw bytes]
func writeSubstitutionArray(b *bytes.Buffer, subs []substitutionEntry) {
	writeUint32LE(b, uint32(len(subs)))

	// Value specs.
	for _, s := range subs {
		writeUint16LE(b, uint16(len(s.data)))
		b.WriteByte(s.typ)
		b.WriteByte(0x00) // padding
	}

	// Value data.
	for _, s := range subs {
		b.Write(s.data)
	}
}

// ---------------------------------------------------------------------------
// BinXML token writers (used by buildTemplateBody)
// ---------------------------------------------------------------------------

// writeOpenElement writes an OpenStartElement token with inline NameNode.
//
// Layout without attrs (0x01):
//
//	[token: 1B] [dep_id: 2B] [data_size: 4B] [name_offset: 4B] [NameNode]
//
// Layout with attrs (0x41):
//
//	[token: 1B] [dep_id: 2B] [data_size: 4B] [name_offset: 4B] [attr_list_size: 4B] [NameNode]
//
// python-evtx checks flags() & 0x04 and adds 4 to tag_length for the attribute_list_size
// field, which must appear BEFORE the inline NameNode.
func writeOpenElement(b *bytes.Buffer, name string, hasAttrs bool, binXMLBase uint32) {
	tokenPos := uint32(b.Len())
	if hasAttrs {
		b.WriteByte(binXMLOpenElementAttrs) // 0x41
	} else {
		b.WriteByte(binXMLOpenElement) // 0x01
	}
	writeUint16LE(b, 0) // dependency_id
	writeUint32LE(b, 0) // data_size (unused by python-evtx)

	headerSize := uint32(11) // token(1) + dep_id(2) + data_size(4) + name_offset(4)
	if hasAttrs {
		headerSize = 15 // + attr_list_size(4)
	}
	nameNodeOffset := binXMLBase + tokenPos + headerSize
	writeUint32LE(b, nameNodeOffset)

	if hasAttrs {
		writeUint32LE(b, 0) // attribute_list_size (python-evtx ignores the value)
	}

	writeNameNode(b, name)
}

// writeAttributeSub writes an Attribute token with inline NameNode, followed by
// a NormalSubstitution token as the attribute's value.
//
// Layout: [token: 1B] [name_offset: 4B] [NameNode] [0x0D subIdx subType]
func writeAttributeSub(b *bytes.Buffer, name string, subIndex uint16, subType byte, binXMLBase uint32) {
	tokenPos := uint32(b.Len())
	b.WriteByte(binXMLAttribute)
	nameNodeOffset := binXMLBase + tokenPos + 5 // 5 = token(1) + offset(4)
	writeUint32LE(b, nameNodeOffset)
	writeNameNode(b, name)
	writeSubstitution(b, subIndex, subType)
}

// writeSubstitution writes a NormalSubstitution token (4 bytes).
//
// Layout: [token: 0x0D] [index: 2B LE] [type: 1B]
func writeSubstitution(b *bytes.Buffer, index uint16, valueType byte) {
	b.WriteByte(binXMLNormalSubstitution)
	writeUint16LE(b, index)
	b.WriteByte(valueType)
}

// writeNameNode writes a NameNode inline in the BinXML stream.
//
// Layout: [next_offset: 4B = 0] [hash: 2B] [char_count: 2B] [UTF-16LE chars] [null: 2B]
func writeNameNode(b *bytes.Buffer, name string) {
	u16 := utf16.Encode([]rune(name))
	writeUint32LE(b, 0)                      // next_offset (no chaining)
	writeUint16LE(b, uint16(sdbmHash(name))) // SDBM hash
	writeUint16LE(b, uint16(len(u16)))       // string_length
	for _, c := range u16 {
		writeUint16LE(b, c)
	}
	writeUint16LE(b, 0) // null terminator
}

// encodeSubString encodes a string as raw UTF-16LE with null terminator
// for use in the substitution value data.
func encodeSubString(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	buf := make([]byte, len(u16)*2+2) // +2 for null terminator
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	return buf
}

// ---------------------------------------------------------------------------
// Little-endian helpers
// ---------------------------------------------------------------------------

func sdbmHash(s string) uint32 {
	var h uint32
	for _, c := range []byte(s) {
		h = uint32(c) + (h << 6) + (h << 16) - h
	}
	return h
}

func writeUint16LE(b *bytes.Buffer, v uint16) {
	_ = b.WriteByte(byte(v))
	_ = b.WriteByte(byte(v >> 8))
}

func writeUint32LE(b *bytes.Buffer, v uint32) {
	_ = b.WriteByte(byte(v))
	_ = b.WriteByte(byte(v >> 8))
	_ = b.WriteByte(byte(v >> 16))
	_ = b.WriteByte(byte(v >> 24))
}

func uint16LEBytes(v uint16) []byte {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, v)
	return buf
}

func uint64LEBytes(v uint64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return buf
}
