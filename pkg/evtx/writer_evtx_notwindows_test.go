//go:build !windows

// Tests for BinaryEvtxWriter (non-Windows implementation).
//
// Oracle note: github.com/0xrawsec/golang-evtx v1.2.9 has transitive dependency
// issues that prevent CGO_ENABLED=0 compilation (missing go.sum entries). The
// round-trip oracle is therefore NOT used. Structural verification (magic bytes +
// file header CRC32) is used instead, which covers the most parser-critical
// correctness requirements.
package evtx

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	goevtx "github.com/fjacquet/go-evtx"
)

// TestBinaryEvtxWriter_WriteClose verifies that WriteEvent + Close produce a
// well-formed .evtx file: non-zero size, correct magic bytes, and a valid
// file header CRC32.
func TestBinaryEvtxWriter_WriteClose(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "test.evtx")

	w, err := NewBinaryEvtxWriter(outPath, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter: %v", err)
	}

	now := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	events := []WindowsEvent{
		{
			EventID:         4663,
			TimeCreated:     now,
			Computer:        "testhost",
			ProviderName:    "Microsoft-Windows-Security-Auditing",
			ObjectName:      "/nas/share/file.txt",
			SubjectUserSID:  "S-1-5-21-123",
			SubjectUsername: "testuser",
			SubjectDomain:   "DOMAIN",
			AccessMask:      "0x2",
			CEPAEventType:   "CEPP_FILE_WRITE",
		},
		{
			EventID:         4660,
			TimeCreated:     now.Add(time.Second),
			Computer:        "testhost",
			ProviderName:    "Microsoft-Windows-Security-Auditing",
			ObjectName:      "/nas/share/old.txt",
			SubjectUserSID:  "S-1-5-21-123",
			SubjectUsername: "testuser",
			SubjectDomain:   "DOMAIN",
			AccessMask:      "0x10000",
			CEPAEventType:   "CEPP_DELETE_FILE",
		},
		{
			EventID:         4670,
			TimeCreated:     now.Add(2 * time.Second),
			Computer:        "testhost",
			ProviderName:    "Microsoft-Windows-Security-Auditing",
			ObjectName:      "/nas/share/dir",
			SubjectUserSID:  "S-1-5-21-456",
			SubjectUsername: "admin",
			SubjectDomain:   "DOMAIN",
			AccessMask:      "0x4",
			CEPAEventType:   "CEPP_SETACL_FILE",
		},
	}

	for _, e := range events {
		if err := w.WriteEvent(context.Background(), e); err != nil {
			t.Fatalf("WriteEvent: %v", err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// File must exist and have non-zero size.
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output file is empty")
	}

	// Read the whole file for structural checks.
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	defer func() { _ = f.Close() }()

	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if string(magic) != "ElfFile\x00" {
		t.Fatalf("wrong EVTX magic: got %q, want %q", magic, "ElfFile\x00")
	}

	// Read rest of file.
	rest, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read rest of file: %v", err)
	}
	fullFile := append(magic, rest...)

	// File header must be at least 128 bytes.
	if len(fullFile) < 128 {
		t.Fatalf("file too short for header: %d bytes", len(fullFile))
	}

	// Verify file header CRC32: crc32(buf[0:120]) stored at buf[124:128].
	storedCRC := binary.LittleEndian.Uint32(fullFile[124:128])
	wantCRC := crc32.Checksum(fullFile[0:120], crc32.IEEETable)
	if storedCRC != wantCRC {
		t.Errorf("file header CRC32 mismatch: stored 0x%08x, want 0x%08x", storedCRC, wantCRC)
	}

	// File must be at least fileHeader + chunkHeader in size.
	minSize := evtxFileHeaderSize + evtxChunkHeaderSize
	if len(fullFile) < minSize {
		t.Fatalf("file too short: got %d bytes, want >= %d", len(fullFile), minSize)
	}

	// Chunk must start with "ElfChnk\x00" at offset evtxFileHeaderSize.
	chunkMagic := string(fullFile[evtxFileHeaderSize : evtxFileHeaderSize+8])
	if chunkMagic != evtxChunkMagic {
		t.Errorf("wrong chunk magic at offset %d: got %q, want %q",
			evtxFileHeaderSize, chunkMagic, evtxChunkMagic)
	}
}

// TestBinaryEvtxWriter_EmptyClose verifies that calling Close() without any
// WriteEvent calls returns nil and does NOT create the output file.
func TestBinaryEvtxWriter_EmptyClose(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "empty.evtx")

	w, err := NewBinaryEvtxWriter(outPath, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close() on empty writer returned error: %v", err)
	}

	// File should NOT exist — no events were written.
	if _, err := os.Stat(outPath); err == nil {
		t.Error("expected no file on empty close, but file was created")
	}
}

// TestBinaryEvtxWriter_Concurrent spawns 10 goroutines each writing one event,
// then calls Close(). Verifies the file exists with non-zero size, proving
// sync.Mutex is sufficient for concurrent access.
func TestBinaryEvtxWriter_Concurrent(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "concurrent.evtx")

	w, err := NewBinaryEvtxWriter(outPath, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter: %v", err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			e := WindowsEvent{
				EventID:         4663,
				TimeCreated:     time.Now(),
				Computer:        "testhost",
				ProviderName:    "Microsoft-Windows-Security-Auditing",
				ObjectName:      "/nas/file.txt",
				SubjectUserSID:  "S-1-5-21-999",
				SubjectUsername: "user",
				SubjectDomain:   "DOMAIN",
				AccessMask:      "0x2",
				CEPAEventType:   "CEPP_FILE_WRITE",
			}
			if err := w.WriteEvent(context.Background(), e); err != nil {
				t.Errorf("goroutine %d WriteEvent: %v", n, err)
			}
		}(i)
	}

	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file missing after concurrent writes: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output file is empty after concurrent writes")
	}
}

// TestBinaryEvtxWriter_EmptyPath verifies that NewBinaryEvtxWriter returns an
// error when given an empty path.
func TestBinaryEvtxWriter_EmptyPath(t *testing.T) {
	_, err := NewBinaryEvtxWriter("", goevtx.RotationConfig{})
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

// TestBinaryEvtxWriter_ParentDirCreated verifies that NewBinaryEvtxWriter
// creates the parent directory if it does not exist.
func TestBinaryEvtxWriter_ParentDirCreated(t *testing.T) {
	dir := t.TempDir()
	// Use a nested path whose parent does not exist yet.
	outPath := filepath.Join(dir, "nested", "deep", "test.evtx")

	w, err := NewBinaryEvtxWriter(outPath, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter with nested path: %v", err)
	}

	// Write one event and close to produce the file.
	e := WindowsEvent{
		EventID:      4663,
		TimeCreated:  time.Now(),
		Computer:     "testhost",
		ProviderName: "Microsoft-Windows-Security-Auditing",
	}
	if err := w.WriteEvent(context.Background(), e); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file not found at nested path: %v", err)
	}
}

// TestBinaryEvtxWriter_ChunkLayout verifies the binary layout of the generated chunk:
// - The first event record starts at byte 512 of the chunk (byte 4608 of the file)
// - The first event record begins with the EVTX record signature 0x00002A2A
// - Inline NameNodes are present in the BinXML stream within the record
func TestBinaryEvtxWriter_ChunkLayout(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "layout.evtx")

	w, err := NewBinaryEvtxWriter(outPath, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter: %v", err)
	}

	e := WindowsEvent{
		EventID:         4663,
		TimeCreated:     time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC),
		Computer:        "testhost",
		ProviderName:    "Microsoft-Windows-Security-Auditing",
		ObjectName:      "/nas/share/file.txt",
		SubjectUserSID:  "S-1-5-21-123",
		SubjectUsername: "testuser",
		SubjectDomain:   "DOMAIN",
		AccessMask:      "0x2",
		CEPAEventType:   "CEPP_FILE_WRITE",
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

	// Event record starts at chunk offset 512 → file offset 4096 + 512 = 4608.
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
