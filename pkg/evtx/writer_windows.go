//go:build windows

// Windows-only writer: delegates to the Win32 EventLog API via
// golang.org/x/sys/windows/svc/eventlog.
//
// An EventSource named "PowerStore-CEPA" is registered under the Application
// log on first start, with EventMessageFile pointing at the exporter's own
// executable. cee-exporter.exe carries a compiled message resource (see
// pkg/evtx/messages.mc and rsrc_windows_amd64.syso) defining descriptions for
// event IDs 4660, 4663 and 4670, so Event Viewer and forwarders built on the
// Event Log API render the real payload rather than "The description for
// Event ID N ... cannot be found".
//
// Registration requires Administrator privileges; writes do not. When
// registration fails, the writer logs a warning naming the consequence and
// continues — events are still written, they just render without their
// description until the exporter is run once with sufficient rights.
//
// Upgrade path: builds before v5.0 registered the source with
// InstallAsEventCreate, pointing EventMessageFile at EventCreate.exe (whose
// message table stops at ID 1000). eventlog.Install will not repoint an
// existing source, so ensureEventSource detects the mismatch and
// re-registers.
package evtx

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/eventlog"
)

const win32SourceName = DefaultProviderName

// eventLogKey is where Windows stores event source registration.
const eventLogKey = `SYSTEM\CurrentControlSet\Services\EventLog\Application\` + win32SourceName

// Win32EventLogWriter writes events to the Windows Application event log.
type Win32EventLogWriter struct {
	log *eventlog.Log
}

// NewWin32EventLogWriter registers the event source (if needed) and opens it.
func NewWin32EventLogWriter() (*Win32EventLogWriter, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("win32 locate executable: %w", err)
	}

	if err := ensureEventSource(exe); err != nil {
		// Registration needs Administrator rights. Without them the exporter
		// still writes events; they render with placeholder description text.
		// Degrading loudly beats refusing to start.
		slog.Warn("win32_source_registration_failed",
			"source", win32SourceName,
			"err", err,
			"consequence", "events will render as \"The description for Event ID N cannot be found\" until the exporter is run once as Administrator")
	}

	l, err := eventlog.Open(win32SourceName)
	if err != nil {
		return nil, fmt.Errorf("win32 open event log source %q: %w", win32SourceName, err)
	}

	slog.Info("win32_writer_ready", "source", win32SourceName, "message_file", exe)
	return &Win32EventLogWriter{log: l}, nil
}

// ensureEventSource registers the event source against exePath, repointing an
// existing registration when it names a different file.
//
// eventlog.Install is a no-op when the source already exists, so an upgrade
// from a build that used InstallAsEventCreate would keep EventMessageFile
// pointing at EventCreate.exe and keep rendering placeholder text forever.
// Detect that case and re-register.
//
// Both paths need Administrator rights, exactly as first-time registration
// always has. When they are absent the caller logs a warning naming the
// consequence rather than failing to start — a running exporter writing
// badly-rendered events is more useful than one that refuses to run.
func ensureEventSource(exePath string) error {
	current, err := registeredMessageFile()
	switch {
	case err != nil:
		// Not registered at all — normal first run.
	case current == exePath:
		return nil
	default:
		slog.Info("win32_source_repointing",
			"source", win32SourceName,
			"from", current,
			"to", exePath,
			"reason", "registered message file does not match this executable")
		if err := eventlog.Remove(win32SourceName); err != nil {
			return fmt.Errorf("win32 remove stale event source %q: %w", win32SourceName, err)
		}
	}

	if err := eventlog.Install(win32SourceName, exePath, true,
		eventlog.Info|eventlog.Warning|eventlog.Error); err != nil {
		return fmt.Errorf("win32 install event source %q: %w", win32SourceName, err)
	}
	return nil
}

// registeredMessageFile returns the EventMessageFile currently registered for
// the source, or an error when the source does not exist.
func registeredMessageFile() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, eventLogKey, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close() //nolint:errcheck

	v, _, err := k.GetStringValue("EventMessageFile")
	return v, err
}

// WriteEvent writes a single event via ReportEvent.
// The insertion strings are formatted to match the layout of a Windows
// Security audit event. Combined with the message resource compiled into
// this executable (see the package comment above), Event Viewer and other
// readers built on the Event Log API resolve a real description for
// 4660/4663/4670 instead of the former placeholder text.
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

// WriteBatch writes each event in turn. Win32 ReportEvent takes one event and
// offers no batch form, so this exists to satisfy the interface rather than to
// gain throughput.
func (w *Win32EventLogWriter) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	return writeBatchSerially(ctx, w, events)
}

// Close releases the event log handle.
func (w *Win32EventLogWriter) Close() error {
	return w.log.Close()
}

// formatWin32Message produces the insertion string in the same
// Subject/Object/Access Request Information layout Windows uses for its own
// Security-auditing events (4660/4663/4670). Combined with the message
// resource compiled into this executable, that layout is what lets Event
// Viewer and other Event Log API readers render a real description instead
// of placeholder text — see docs/windows-verification.md for the verified
// before/after. Whether a specific SIEM's content pack, built to parse a
// genuine Security-log event, treats this output as equivalent is a
// separate, unverified claim; an earlier version of this comment made it
// ("expected by SIEM content packs") and no longer does.
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
