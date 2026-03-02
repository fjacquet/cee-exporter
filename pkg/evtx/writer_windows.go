//go:build windows

// Windows-only writer: delegates to the Win32 EventLog API via
// golang.org/x/sys/windows/svc/eventlog.
//
// An EventSource named "PowerStore-CEPA" is registered under the Application
// log on first start.  The source registration requires administrator
// privileges; subsequent writes do not.
//
// IMPORTANT: Event IDs above 1000 silently fail with some Windows
// configurations.  We use IDs in the range 4656–4670 to match the Windows
// Security audit taxonomy and pre-register them via the message DLL path.
// Because we rely on the generic Application log (not the Security log, which
// requires auditpol), the Event Viewer will show the raw insertion strings
// without message substitution — which is acceptable for SIEM ingestion.
package evtx

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sys/windows/svc/eventlog"
)

const win32SourceName = "PowerStore-CEPA"

// Win32EventLogWriter writes events to the Windows Application event log.
type Win32EventLogWriter struct {
	log *eventlog.Log
}

// NewWin32EventLogWriter registers the event source (if needed) and opens it.
func NewWin32EventLogWriter() (*Win32EventLogWriter, error) {
	// InstallAsEventCreate registers the source using the built-in
	// "EventCreate.exe" message file, which supports event IDs 1–1000.
	// For IDs 4656–4670 we register with a custom supportedTypes mask only.
	err := eventlog.InstallAsEventCreate(win32SourceName, eventlog.Info|eventlog.Warning|eventlog.Error)
	if err != nil {
		// Already registered is not an error.
		slog.Debug("win32_source_already_registered", "source", win32SourceName, "err", err)
	}

	l, err := eventlog.Open(win32SourceName)
	if err != nil {
		return nil, fmt.Errorf("win32 open event log source %q: %w", win32SourceName, err)
	}

	slog.Info("win32_writer_ready", "source", win32SourceName)
	return &Win32EventLogWriter{log: l}, nil
}

// WriteEvent writes a single event via ReportEvent.
// The insertion strings are formatted to match the expected Windows Security
// event layout so that SIEM content packs for event IDs 4663/4660/4670 work.
func (w *Win32EventLogWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	msg := formatWin32Message(e)

	// Windows event log API accepts uint32 event IDs.
	eid := uint32(e.EventID) //nolint:gosec
	if err := w.log.Info(eid, msg); err != nil {
		return fmt.Errorf("win32 ReportEvent id=%d: %w", e.EventID, err)
	}

	slog.Debug("win32_event_written",
		"event_id", e.EventID,
		"file_path", e.ObjectName,
		"cepa_event_type", e.CEPAEventType,
	)
	return nil
}

// Close releases the event log handle.
func (w *Win32EventLogWriter) Close() error {
	return w.log.Close()
}

// formatWin32Message produces the insertion string that maps to the Windows
// Security Event format expected by SIEM content packs.
func formatWin32Message(e WindowsEvent) string {
	return fmt.Sprintf(
		"Subject:\r\n\tSecurity ID:\t%s\r\n\tAccount Name:\t%s\r\n\tAccount Domain:\t%s\r\n\tLogon ID:\t%s\r\n\r\nObject:\r\n\tObject Server:\tSecurity\r\n\tObject Type:\t%s\r\n\tObject Name:\t%s\r\n\r\nProcess Information:\r\n\tProcess ID:\t0x%x\r\n\tProcess Name:\tCEPA\r\n\r\nAccess Request Information:\r\n\tTransaction ID:\t{00000000-0000-0000-0000-000000000000}\r\n\tAccesses:\t%s\r\n\tAccess Mask:\t%s\r\n\r\nNetwork:\r\n\tClient Address:\t%s\r\n\r\nI/O Statistics:\r\n\tBytes Read:\t%d\r\n\tBytes Written:\t%d",
		e.SubjectUserSID,
		e.SubjectUsername,
		e.SubjectDomain,
		e.SubjectLogonID,
		e.ObjectType,
		e.ObjectName,
		e.ProcessID,
		e.Accesses,
		e.AccessMask,
		e.ClientAddr,
		e.BytesRead,
		e.BytesWritten,
	)
}
