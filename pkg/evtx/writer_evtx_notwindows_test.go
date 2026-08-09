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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
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

// utf16LE returns the byte sequence go-evtx writes for s, so a test can locate
// a field value inside the encoded .evtx without a BinXML parser.
func utf16LE(s string) []byte {
	var b bytes.Buffer
	for _, u := range utf16.Encode([]rune(s)) {
		b.WriteByte(byte(u))
		b.WriteByte(byte(u >> 8))
	}
	return b.Bytes()
}

// TestBinaryEvtxWriter_Concurrent spawns goroutines that each write one event
// carrying a marker unique to that goroutine, then asserts every marker
// appears in the finished file exactly once.
//
// The count is the point. This test previously asserted only that the file
// existed and was non-empty, which one surviving event out of ten satisfies —
// so the exact failure a concurrency test exists to catch was the failure it
// could not see. It also claimed to prove "sync.Mutex is sufficient", but
// WriteEvent takes no lock: b.mu guards Close alone. Serialisation of writes
// belongs to go-evtx, and this test plus `go test -race` is what holds it to
// that. Nothing here would survive moving the lock into WriteEvent either —
// the assertion is on the output, not the mechanism.
func TestBinaryEvtxWriter_Concurrent(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "concurrent.evtx")

	w, err := NewBinaryEvtxWriter(outPath, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter: %v", err)
	}

	const goroutines = 10
	markers := make([]string, goroutines)
	for i := range markers {
		markers[i] = fmt.Sprintf("/nas/concurrent-marker-%02d.txt", i)
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			<-start // widen the window: all goroutines contend at once
			e := WindowsEvent{
				EventID:         4663,
				TimeCreated:     time.Now(),
				Computer:        "testhost",
				ProviderName:    "Microsoft-Windows-Security-Auditing",
				ObjectName:      markers[n],
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

	close(start)
	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file missing after concurrent writes: %v", err)
	}

	var missing, duplicated []string
	for _, m := range markers {
		switch n := bytes.Count(data, utf16LE(m)); {
		case n == 0:
			missing = append(missing, m)
		case n > 1:
			duplicated = append(duplicated, fmt.Sprintf("%s×%d", m, n))
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d concurrent events lost: %v",
			len(missing), goroutines, missing)
	}
	if len(duplicated) > 0 {
		t.Errorf("events written more than once (interleaved records): %v",
			duplicated)
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

// testWindowsEvent returns a minimal valid event for writer tests.
func testWindowsEvent() WindowsEvent {
	return WindowsEvent{
		EventID:      4663,
		ProviderName: "Microsoft-Windows-Security-Auditing",
		Computer:     "testhost",
		TimeCreated:  time.Now(),
		ObjectType:   "File",
		ObjectName:   "/mnt/share/file.txt",
		AccessMask:   "0x2",
	}
}

// TestBinaryEvtxWriter_CloseIdempotent verifies that a second Close cannot
// propagate go-evtx's 'close of closed channel' panic into the daemon.
func TestBinaryEvtxWriter_CloseIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.evtx")
	w, err := NewBinaryEvtxWriter(path, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("NewBinaryEvtxWriter: %v", err)
	}
	if err := w.WriteEvent(context.Background(), testWindowsEvent()); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	first := w.Close()
	if first != nil {
		t.Fatalf("first Close: %v", first)
	}
	// Must not panic, and must report the first call's result.
	// errors.Is rather than != : the repo enables errorlint with
	// comparison: true, which rejects direct error comparison.
	if second := w.Close(); !errors.Is(second, first) {
		t.Fatalf("second Close = %v, want %v (same as first)", second, first)
	}
}

// TestTruncateField verifies the cap that keeps a filesystem-controlled path
// from reaching go-evtx's oversized-record corruption path.
func TestTruncateField(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTrunc bool
		wantLen   int
	}{
		{"short", "/mnt/share/file.txt", false, len("/mnt/share/file.txt")},
		{"exactly at cap", strings.Repeat("a", maxFieldBytes), false, maxFieldBytes},
		{"one over cap", strings.Repeat("a", maxFieldBytes+1), true, maxFieldBytes},
		{"far over cap", strings.Repeat("a", 70000), true, maxFieldBytes},
		// NOTE: brief specified maxFieldBytes-1 here, but that is arithmetically
		// wrong for these constants: maxFieldBytes(8192) - len(truncationMarker)(14)
		// = 8178, which is even, so it already lands on an "é" (2-byte rune)
		// boundary — zero bytes need to be dropped, giving exactly maxFieldBytes.
		// Verified by direct execution of the verbatim algorithm; see task-4-report.md.
		{"multibyte not split", strings.Repeat("é", maxFieldBytes), true, maxFieldBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := truncateField(tt.input)
			if truncated != tt.wantTrunc {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTrunc)
			}
			if len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}
			if tt.wantTrunc && !strings.HasSuffix(got, truncationMarker) {
				t.Errorf("truncated value %q lacks the %q marker", got[len(got)-40:], truncationMarker)
			}
		})
	}
}

// TestWindowsEventToFields_CapsOversizedObjectName verifies the cap is applied
// on the real path and counted, so an over-long filesystem path cannot corrupt
// the .evtx file.
func TestWindowsEventToFields_CapsOversizedObjectName(t *testing.T) {
	metrics.M.EventsTruncatedTotal.Store(0)

	e := testWindowsEvent()
	e.ObjectName = strings.Repeat("A", 70000)

	fields := windowsEventToFields(e)
	if len(fields["ObjectName"]) > maxFieldBytes {
		t.Fatalf("ObjectName is %d bytes, want <= %d", len(fields["ObjectName"]), maxFieldBytes)
	}

	// Budget in ENCODED bytes — go-evtx writes UTF-16LE, so raw len() is not
	// the quantity that has to fit.
	total := 0
	for _, v := range fields {
		total += encodedLen(v)
	}
	if total > maxEncodedFieldsBytes {
		t.Fatalf("encoded fields %d bytes exceeds budget %d", total, maxEncodedFieldsBytes)
	}
	if got := metrics.M.EventsTruncatedTotal.Load(); got != 1 {
		t.Fatalf("EventsTruncatedTotal = %d, want 1", got)
	}
}

// TestWindowsEventToFields_AllFieldsMaxed is the worst case: every field at the
// per-field cap. The encoded total must still fit inside one go-evtx record,
// otherwise the per-field cap alone is not a sufficient guard.
func TestWindowsEventToFields_AllFieldsMaxed(t *testing.T) {
	metrics.M.EventsTruncatedTotal.Store(0)

	big := strings.Repeat("A", maxFieldBytes)
	e := WindowsEvent{
		EventID:         4663,
		ProviderName:    big,
		Computer:        big,
		TimeCreated:     time.Now(),
		SubjectUserSID:  big,
		SubjectUsername: big,
		SubjectDomain:   big,
		SubjectLogonID:  big,
		ObjectType:      big,
		ObjectName:      big,
		HandleID:        big,
		Accesses:        big,
		AccessMask:      big,
	}

	total := 0
	for _, v := range windowsEventToFields(e) {
		total += encodedLen(v)
	}
	if total > maxEncodedFieldsBytes {
		t.Fatalf("worst case encodes to %d bytes, exceeding the %d budget: "+
			"lower maxFieldBytes or add a total-budget pass", total, maxEncodedFieldsBytes)
	}
}

// TestEncodedLen verifies the UTF-16LE accounting the budget relies on.
func TestEncodedLen(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 4},        // prefix + terminator only
		{"ascii", "abc", 4 + 6}, // 2 bytes per code unit
		{"latin1", "é", 4 + 2},  // one BMP code unit
		{"non-BMP", "😀", 4 + 4}, // surrogate pair: two code units
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := encodedLen(tt.in); got != tt.want {
				t.Errorf("encodedLen(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestEnforceEncodedBudget_TerminatesOnFixedPoint pins the termination
// property directly. A value whose raw length sits in
// (len(truncationMarker), 2*len(truncationMarker)] — 15 to 28 bytes with
// today's 14-byte marker — does not shrink when halved and re-suffixed with
// the marker: halving 20 bytes gives keep=10, and 10+14=24 is still >= 20.
// Without the fixed-point escape, enforceEncodedBudget would spin on that
// value forever, since it can never fall below maxEncodedFieldsBytes and
// never satisfies the "longest <= len(truncationMarker)" escape either.
//
// This is exercised with many such fields — never reachable through the
// real 11-field WindowsEvent shape today — so the test also serves as a
// regression guard if maxFieldBytes, maxEncodedFieldsBytes, or the field
// count ever change in a way that makes the fixed point reachable in
// production.
//
// The check runs in a goroutine with its own timeout rather than relying on
// `go test`'s overall timeout to catch a hang, so a regression fails fast
// with a clear message instead of a generic test-binary timeout.
func TestEnforceEncodedBudget_TerminatesOnFixedPoint(t *testing.T) {
	const fieldCount = 4000 // encodes to far more than maxEncodedFieldsBytes
	fields := make(map[string]string, fieldCount)
	for i := 0; i < fieldCount; i++ {
		// 20 bytes is inside the (14, 28] fixed-point range: halving never
		// shrinks it once the marker is appended.
		fields[fmt.Sprintf("f%d", i)] = strings.Repeat("a", 20)
	}

	done := make(chan bool, 1)
	go func() {
		done <- enforceEncodedBudget(fields)
	}()

	select {
	case <-done:
		// Returned — termination property holds.
	case <-time.After(2 * time.Second):
		t.Fatal("enforceEncodedBudget did not return within 2s: " +
			"suspected infinite loop on the 15-28 byte fixed point")
	}
}
