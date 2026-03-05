//go:build !windows

// BinaryEvtxWriter — thin adapter for non-Windows platforms.
//
// Translates WindowsEvent to map[string]string and delegates all
// EVTX binary format encoding to github.com/fjacquet/go-evtx.
package evtx

import (
	"context"
	"fmt"
	"time"

	goevtx "github.com/fjacquet/go-evtx"
)

// BinaryEvtxWriter writes Windows .evtx binary format files on non-Windows platforms.
// All exported methods are safe for concurrent use.
type BinaryEvtxWriter struct {
	w *goevtx.Writer
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
func (b *BinaryEvtxWriter) Close() error {
	return b.w.Close()
}

// Rotate triggers an immediate rotation of the active .evtx file.
// The current chunk is finalized to disk, the file is renamed to a
// timestamped archive, and a fresh file is opened.
// Safe for concurrent use. Called by the SIGHUP handler in main.go.
func (b *BinaryEvtxWriter) Rotate() error {
	return b.w.Rotate()
}

// windowsEventToFields translates a WindowsEvent to the map[string]string
// expected by go-evtx's WriteRecord.
func windowsEventToFields(e WindowsEvent) map[string]string {
	return map[string]string{
		"ProviderName":      e.ProviderName,
		"Computer":          e.Computer,
		"TimeCreated":       e.TimeCreated.UTC().Format(time.RFC3339Nano),
		"SubjectUserSid":    e.SubjectUserSID,
		"SubjectUserName":   e.SubjectUsername,
		"SubjectDomainName": e.SubjectDomain,
		"SubjectLogonId":    e.SubjectLogonID,
		"ObjectServer":      "Security",
		"ObjectType":        e.ObjectType,
		"ObjectName":        e.ObjectName,
		"HandleId":          e.HandleID,
		"AccessList":        e.Accesses,
		"AccessMask":        e.AccessMask,
		"ProcessId":         fmt.Sprintf("%d", e.ProcessID),
		"ProcessName":       "",
	}
}
