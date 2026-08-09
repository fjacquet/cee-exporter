//go:build !windows

// BinaryEvtxWriter — thin adapter for non-Windows platforms.
//
// Translates WindowsEvent to map[string]string and delegates all
// EVTX binary format encoding to github.com/fjacquet/go-evtx.
package evtx

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
	goevtx "github.com/fjacquet/go-evtx"
)

// BinaryEvtxWriter writes Windows .evtx binary format files on non-Windows platforms.
//
// All exported methods are safe for concurrent use, but not all of them are
// made so here. mu covers the closed/closeErr pair and nothing else; WriteEvent
// and Rotate hold no lock and rely on goevtx.Writer serialising its own state.
// That division is load-bearing — a reader who assumes mu protects the write
// path will misjudge what a change to it can break.
//
// What actually holds the delegated half to its claim is narrower than the
// claim: TestBinaryEvtxWriter_Concurrent under `go test -race` exercises
// concurrent WriteEvent only. Concurrent Rotate, and WriteEvent interleaved
// with Rotate, are covered by nothing here and rest on go-evtx's own tests.
type BinaryEvtxWriter struct {
	w *goevtx.Writer

	// mu guards closed and closeErr only.
	mu       sync.Mutex
	closed   bool
	closeErr error
}

// NewBinaryEvtxWriter creates a BinaryEvtxWriter that will write to evtxPath.
//
// cfg controls periodic checkpoint-write behaviour. Pass goevtx.RotationConfig{}
// to disable the background goroutine (FlushIntervalSec defaults to 0 = disabled).
func NewBinaryEvtxWriter(evtxPath string, cfg goevtx.RotationConfig) (*BinaryEvtxWriter, error) {
	if evtxPath == "" {
		return nil, fmt.Errorf("binary_evtx_writer: evtxPath must be non-empty")
	}
	w, err := goevtx.New(evtxPath, cfg)
	if err != nil {
		return nil, fmt.Errorf("binary_evtx_writer: %w", err)
	}
	return &BinaryEvtxWriter{w: w}, nil
}

// WriteEvent encodes e as a BinXML event record and delegates to go-evtx.
func (b *BinaryEvtxWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	return b.w.WriteRecord(e.EventID, windowsEventToFields(e))
}

// Close flushes all buffered events to disk and finalises the .evtx file.
//
// Close is idempotent. go-evtx versions before v0.6.0 panic with
// "close of closed channel" on a second Close, which would take the daemon
// down during shutdown; the guard here is kept even after that is fixed
// upstream, because the queue and the signal handler can both reach it.
func (b *BinaryEvtxWriter) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return b.closeErr
	}
	b.closed = true
	b.closeErr = b.w.Close()
	return b.closeErr
}

// Rotate triggers an immediate rotation of the active .evtx file.
// The current chunk is finalized to disk, the file is renamed to a
// timestamped archive, and a fresh file is opened.
// Safe for concurrent use. Called by the SIGHUP handler in main.go.
func (b *BinaryEvtxWriter) Rotate() error {
	return b.w.Rotate()
}

// Field size caps. go-evtx rejects any record whose encoded BinXML payload
// exceeds the chunk capacity; before v0.6.0 it silently truncated instead,
// producing a file whose CRCs verify but which no parser can read. ObjectName
// is a filesystem-controlled path, so the cap is enforced here rather than
// trusting the library.
//
// The budget must be computed in ENCODED bytes, not Go string bytes. go-evtx
// encodes every value as UTF-16LE with a 2-byte length prefix and a 2-byte
// terminator, so an ASCII string of n bytes costs 2n+4 on the wire — and a
// non-BMP rune costs 4 bytes per code point. Budgeting against raw len()
// under-counts by more than 2x and would let a record through that go-evtx
// then rejects.
const (
	// maxRecordBytes mirrors go-evtx's per-record payload capacity:
	// 65024 chunk payload - 24 record header - 4 trailing size.
	maxRecordBytes = 64996
	// binXMLOverheadBytes reserves room for the BinXML template, the
	// substitution array and the element framing that wrap the values.
	// Measured at ~1.5 KiB; 4 KiB is a deliberately generous margin.
	binXMLOverheadBytes = 4096
	// maxEncodedFieldsBytes is the total UTF-16LE budget for all field values.
	maxEncodedFieldsBytes = maxRecordBytes - binXMLOverheadBytes // 60900
	// maxFieldBytes caps any single field value in Go string bytes. 8 KiB is
	// far above any real filesystem path (PATH_MAX is 4096 on Linux) while
	// leaving the total budget reachable only by pathological input.
	maxFieldBytes = 8192
	// truncationMarker is appended in place of the removed bytes.
	truncationMarker = "…[truncated]"
)

// encodedLen returns the number of bytes go-evtx will write for s: a 2-byte
// length prefix, two bytes per UTF-16 code unit, and a 2-byte terminator.
// Non-BMP runes encode as surrogate pairs and therefore cost 4 bytes.
func encodedLen(s string) int {
	return 4 + 2*len(utf16.Encode([]rune(s)))
}

// truncateField caps s at maxFieldBytes Go string bytes, replacing the tail
// with truncationMarker. It reports whether truncation occurred.
func truncateField(s string) (string, bool) {
	if len(s) <= maxFieldBytes {
		return s, false
	}
	keep := maxFieldBytes - len(truncationMarker)
	// Do not split a multi-byte rune.
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + truncationMarker, true
}

// enforceEncodedBudget shrinks values until the whole field set fits in one
// go-evtx record. It repeatedly halves the largest remaining value, which
// converges quickly and preserves the small fields that carry the identity of
// the event (SID, logon ID, access mask) over the one that is merely long.
//
// It reports whether anything was shortened.
func enforceEncodedBudget(fields map[string]string) bool {
	total := 0
	for _, v := range fields {
		total += encodedLen(v)
	}
	if total <= maxEncodedFieldsBytes {
		return false
	}

	for total > maxEncodedFieldsBytes {
		// Find the longest remaining value.
		var key string
		longest := 0
		for k, v := range fields {
			if len(v) > longest {
				key, longest = k, len(v)
			}
		}
		if longest <= len(truncationMarker) {
			// Nothing left to reclaim; the template overhead reserve absorbs
			// the remainder.
			break
		}

		keep := longest / 2
		for keep > 0 && !utf8.RuneStart(fields[key][keep]) {
			keep--
		}
		// For any value whose length sits in (len(truncationMarker),
		// 2*len(truncationMarker)], halving it and appending the marker does
		// not shrink it — it can even grow it, converging to a fixed point
		// instead of vanishing. That range is unreachable with today's field
		// count and caps, but this function must not rely on that staying
		// true: without this check a future change to maxFieldBytes,
		// maxEncodedFieldsBytes, or the field count could spin forever with
		// no error and no log. Since key is already the longest remaining
		// value, no shorter field can shrink either, so nothing further can
		// be reclaimed.
		if keep+len(truncationMarker) >= longest {
			break
		}
		before := encodedLen(fields[key])
		fields[key] = fields[key][:keep] + truncationMarker
		total += encodedLen(fields[key]) - before
	}
	return true
}

// windowsEventToFields translates a WindowsEvent to the map[string]string
// expected by go-evtx's WriteRecord, capping any oversized value and
// guaranteeing the encoded set fits in one record.
func windowsEventToFields(e WindowsEvent) map[string]string {
	truncated := false
	// Named clip, not cap: cap is a Go builtin and shadowing it here would
	// confuse every later reader of this function.
	clip := func(s string) string {
		v, t := truncateField(s)
		truncated = truncated || t
		return v
	}

	fields := map[string]string{
		"ProviderName":      clip(e.ProviderName),
		"Computer":          clip(e.Computer),
		"TimeCreated":       e.TimeCreated.UTC().Format(time.RFC3339Nano),
		"SubjectUserSid":    clip(e.SubjectUserSID),
		"SubjectUserName":   clip(e.SubjectUsername),
		"SubjectDomainName": clip(e.SubjectDomain),
		"SubjectLogonId":    clip(e.SubjectLogonID),
		"ObjectServer":      "Security",
		"ObjectType":        clip(e.ObjectType),
		"ObjectName":        clip(e.ObjectName),
		"HandleId":          clip(e.HandleID),
		"AccessList":        clip(e.Accesses),
		"AccessMask":        clip(e.AccessMask),
		"ProcessId":         fmt.Sprintf("%d", e.ProcessID),
		"ProcessName":       "",
	}

	// Second pass: the per-field cap bounds one long value, not eleven.
	if enforceEncodedBudget(fields) {
		truncated = true
	}

	if truncated {
		metrics.M.EventsTruncatedTotal.Add(1)
		slog.Warn("evtx_field_truncated",
			"event_id", e.EventID,
			"cepa_event_type", e.CEPAEventType,
			"max_field_bytes", maxFieldBytes,
			"max_encoded_bytes", maxEncodedFieldsBytes,
		)
	}
	return fields
}
