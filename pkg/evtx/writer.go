// Package evtx provides writer implementations for Windows Event Log output.
// The Writer interface abstracts multiple backends: Win32 EventLog (Windows),
// pure-Go binary EVTX (Linux stub), GELF (Graylog), and multi-target fan-out.
package evtx

import (
	"context"
	"fmt"
	"net"
	"strconv"
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

// ShortMessage returns "<CEPAEventType> on <ObjectName>" — the summary string
// used by every textual writer (GELF short_message, syslog MSG, Beats message).
func (e WindowsEvent) ShortMessage() string {
	return fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName)
}

// hostPort joins a host and port into a dial address, preferring strconv.Itoa
// over fmt.Sprintf for the integer conversion.
func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// sendWithRetry runs send once; on failure it invokes reconnect and retries
// send one more time. It's the common send/retry loop extracted from the
// gelf, syslog, and beats writers.
func sendWithRetry(send, reconnect func() error) error {
	err := send()
	if err == nil {
		return nil
	}
	if rerr := reconnect(); rerr != nil {
		return fmt.Errorf("send+reconnect: %w / %w", err, rerr)
	}
	if err2 := send(); err2 != nil {
		return fmt.Errorf("send after reconnect: %w", err2)
	}
	return nil
}
