//go:build !windows

// BinaryEvtxWriter — STUB for v1.
//
// Writing valid binary .evtx files from scratch requires full BinXML
// serialisation (chunk headers with CRC32, FILETIME encoding, BinXML token
// stream, string table).  This is a significant engineering effort (~500-1500
// LOC) and is deferred to v1.x.
//
// For v1, if you need Graylog compatibility, configure the GELFWriter instead.
// If you need Windows Event Log files on Linux, see the roadmap note in
// .planning/PROJECT.md.
//
// The stub satisfies the Writer interface so the binary compiles and the
// code path is exercised in tests.
package evtx

import (
	"context"
	"fmt"
	"log/slog"
)

// BinaryEvtxWriter is a placeholder.  WriteEvent logs the event at DEBUG
// level and returns an error so callers know output is not being persisted.
type BinaryEvtxWriter struct {
	outputDir string
}

// NewBinaryEvtxWriter creates the stub writer.
func NewBinaryEvtxWriter(outputDir string) (*BinaryEvtxWriter, error) {
	slog.Warn("binary_evtx_writer_stub",
		"message", "BinaryEvtxWriter is a stub — no .evtx files will be written",
		"output_dir", outputDir,
		"recommendation", "use GELFWriter for Graylog integration",
	)
	return &BinaryEvtxWriter{outputDir: outputDir}, nil
}

// WriteEvent logs the event and returns a not-implemented error.
func (w *BinaryEvtxWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	slog.Debug("binary_evtx_stub_event",
		"event_id", e.EventID,
		"file_path", e.ObjectName,
		"cepa_event_type", e.CEPAEventType,
	)
	return fmt.Errorf("BinaryEvtxWriter not implemented (v1 stub) — configure type=gelf instead")
}

// Close is a no-op.
func (w *BinaryEvtxWriter) Close() error { return nil }
