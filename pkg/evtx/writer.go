// Package evtx provides writer implementations for Windows Event Log output.
// The Writer interface abstracts multiple backends: Win32 EventLog (Windows),
// pure-Go binary EVTX (Linux stub), GELF (Graylog), and multi-target fan-out.
package evtx

import (
	"context"
	"time"
)

// WindowsEvent is the normalized event structure that all writers consume.
// It carries both the Windows semantic fields and CEPA-specific metadata.
type WindowsEvent struct {
	// Core Windows event fields
	EventID      int
	ProviderName string
	Computer     string
	Channel      string // e.g. "Security"
	TimeCreated  time.Time

	// Subject (who performed the action)
	SubjectUserSID  string
	SubjectUsername string
	SubjectDomain   string
	SubjectLogonID  string

	// Object (what was accessed)
	ObjectType string // "File"
	ObjectName string // absolute file path

	// Access
	AccessMask string // hex, e.g. "0x2"
	Accesses   string // human-readable, e.g. "WriteData (or AddFile)"

	// Process context
	ProcessID int
	HandleID  string

	// Network context (from CEPA)
	ClientAddr string

	// I/O statistics (populated on CEPP_CLOSE_MODIFIED)
	BytesRead      int64
	BytesWritten   int64
	NumberOfReads  int64
	NumberOfWrites int64

	// Raw CEPA event type, preserved for debugging and GELF _cepa_event_type field
	CEPAEventType string
}

// Writer is the output backend interface.  All writers must be safe for
// concurrent use from multiple goroutines.
type Writer interface {
	// WriteEvent writes a single Windows event to the backend.
	// Implementations must be non-blocking from the caller's perspective
	// (i.e. they should not hold up the HTTP handler goroutine for more
	// than a few milliseconds).
	WriteEvent(ctx context.Context, e WindowsEvent) error

	// Close flushes any pending events and releases resources.
	Close() error
}
