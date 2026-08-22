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

// DefaultProviderName is the event source name Windows resolves the compiled
// message resource against (see pkg/evtx/messages.mc). It lives here, not in
// pkg/mapper, because both the Win32 writer (writer_windows.go) and the
// mapper (which imports pkg/evtx) must agree on the exact same string, and
// pkg/evtx is the only package both of them can import without creating an
// import cycle (pkg/mapper -> pkg/evtx, never the reverse).
const DefaultProviderName = "PowerStore-CEPA"

// DefaultChannel is the Windows event log channel recorded on events that do
// not name one. "Security" is the channel event IDs 4660, 4663 and 4670
// belong to, and it is what pkg/mapper already sets on every mapped event.
// A record with an empty channel renders as <Channel></Channel> and Windows
// resolves LogName to the empty string, which leaves the record belonging to
// no log at all.
const DefaultChannel = "Security"

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

	// WriteBatch writes several events as one unit. For a backend with a
	// framed wire protocol this is one lock acquisition and one write for the
	// whole batch, which is the entire throughput win: measured, sixteen
	// workers against the per-event path bought 1.4x, because every write
	// serialised through one mutex.
	//
	// Mandatory rather than an optional interface the caller type-asserts.
	// An optional one lets MultiWriter silently fall back to the per-event
	// loop when it forgets to implement it, which is precisely how Rotate
	// silently did nothing for `type = "multi"` (see writer_multi.go). A
	// missing method must be a compile error.
	//
	// Backends whose underlying API takes one record at a time should
	// implement this as writeBatchSerially(ctx, w, events).
	WriteBatch(ctx context.Context, events []WindowsEvent) error

	// Close flushes any pending events and releases resources.
	Close() error
}

// writeBatchSerially writes each event in turn through w.WriteEvent. It is the
// WriteBatch implementation for backends with no batch API of their own: the
// Win32 ReportEvent call and go-evtx's WriteRecord both take one record.
// Their throughput is unchanged by batching and the spec says so — EVTX's
// ceiling is fsync inside go-evtx, which this repository cannot batch away.
//
// The first error stops the batch. The events after it are the caller's to
// re-send, and continuing would report a partial write as a whole-batch
// failure either way.
func writeBatchSerially(ctx context.Context, w Writer, events []WindowsEvent) error {
	for i := range events {
		if err := w.WriteEvent(ctx, events[i]); err != nil {
			return fmt.Errorf("batch event %d/%d: %w", i+1, len(events), err)
		}
	}
	return nil
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
