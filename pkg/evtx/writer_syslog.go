// SyslogWriter — all platforms.
// Sends RFC 5424 messages to a syslog receiver over UDP or TCP.
// UDP: single datagram, no framing.
// TCP: RFC 6587 §3.4.1 octet-counting framing.
// Uses github.com/crewjam/rfc5424 for message construction (cross-platform,
// unlike stdlib log/syslog which excludes Windows).
package evtx

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/crewjam/rfc5424"
)

// SyslogConfig controls the SyslogWriter behaviour.
type SyslogConfig struct {
	Host     string // Syslog receiver host
	Port     int    // Default 514
	Protocol string // "udp" or "tcp"
	AppName  string // Default "cee-exporter"
}

// SyslogWriter implements Writer.
// It sends RFC 5424 structured syslog messages to a remote receiver.
// TCP transport uses RFC 6587 §3.4.1 octet-counting framing.
type SyslogWriter struct {
	cfg  SyslogConfig
	mu   sync.Mutex
	conn net.Conn
}

// NewSyslogWriter creates a SyslogWriter and opens the initial connection.
func NewSyslogWriter(cfg SyslogConfig) (*SyslogWriter, error) {
	if cfg.Port == 0 {
		cfg.Port = 514
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "udp"
	}
	if cfg.AppName == "" {
		cfg.AppName = "cee-exporter"
	}
	w := &SyslogWriter{cfg: cfg}
	if err := w.connect(); err != nil {
		return nil, err
	}
	slog.Info("syslog_writer_ready",
		"host", cfg.Host,
		"port", cfg.Port,
		"protocol", cfg.Protocol,
	)
	return w, nil
}

func (w *SyslogWriter) connect() error {
	addr := hostPort(w.cfg.Host, w.cfg.Port)
	conn, err := net.DialTimeout(w.cfg.Protocol, addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("syslog connect %s://%s: %w", w.cfg.Protocol, addr, err)
	}
	if w.conn != nil {
		_ = w.conn.Close()
	}
	w.conn = conn
	return nil
}

// sendRaw writes b with a deadline, adding no framing — the caller has
// already framed the payload, which is what lets the batch path concatenate
// K frames into a single Write.
func (w *SyslogWriter) sendRaw(b []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := w.conn.Write(b)
	return err
}

// retrySend runs send, reconnecting and retrying once on failure. Caller
// must hold w.mu.
func (w *SyslogWriter) retrySend(send func() error) error {
	return sendWithRetry(send, func() error {
		slog.Warn("syslog_reconnect")
		return w.connect()
	})
}

// frameTCP appends payload to dst, prefixed with the RFC 6587 §3.4.1 octet
// count: "<decimal length> <message>". Uses strconv.AppendInt rather than
// fmt.Fprintf into a bytes.Buffer: bytes.Buffer.Write returns an error that
// errcheck requires handling, for a write that cannot fail.
func frameTCP(dst, payload []byte) []byte {
	dst = strconv.AppendInt(dst, int64(len(payload)), 10)
	dst = append(dst, ' ')
	return append(dst, payload...)
}

// WriteEvent serialises the event as RFC 5424 syslog and sends it.
func (w *SyslogWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	payload, err := buildSyslog5424(e, w.cfg.AppName)
	if err != nil {
		return fmt.Errorf("syslog build: %w", err)
	}
	if w.cfg.Protocol == "tcp" {
		payload = frameTCP(make([]byte, 0, len(payload)+8), payload)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.retrySend(func() error { return w.sendRaw(payload) }); err != nil {
		return fmt.Errorf("syslog %w", err)
	}

	slog.Debug("syslog_event_sent", "event_id", e.EventID)
	return nil
}

// WriteBatch sends every event in one lock acquisition.
//
// On TCP that is one Write carrying K octet-counted frames. The per-event
// path issued TWO syscalls per event — fmt.Fprintf for the length prefix,
// then Write for the payload — so a 500-event batch goes from 1000 writes to
// one.
//
// UDP stays one datagram per message (RFC 5426); only the lock is shared.
func (w *SyslogWriter) WriteBatch(_ context.Context, events []WindowsEvent) error {
	if len(events) == 0 {
		return nil
	}

	// Build before locking: this is the half of the cost that parallelises,
	// and holding the lock across it would give the batch back nothing.
	payloads := make([][]byte, 0, len(events))
	total := 0
	for i := range events {
		p, err := buildSyslog5424(events[i], w.cfg.AppName)
		if err != nil {
			return fmt.Errorf("syslog build event %d/%d: %w", i+1, len(events), err)
		}
		payloads = append(payloads, p)
		total += len(p) + 8 // payload + decimal length + space
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cfg.Protocol == "tcp" {
		frame := make([]byte, 0, total)
		for _, p := range payloads {
			frame = frameTCP(frame, p)
		}
		if err := w.retrySend(func() error { return w.sendRaw(frame) }); err != nil {
			return fmt.Errorf("syslog batch %w", err)
		}
		slog.Debug("syslog_batch_sent", "events", len(events), "bytes", len(frame))
		return nil
	}

	for i, p := range payloads {
		if err := w.retrySend(func() error { return w.sendRaw(p) }); err != nil {
			return fmt.Errorf("syslog batch event %d/%d: %w", i+1, len(events), err)
		}
	}
	slog.Debug("syslog_batch_sent", "events", len(events), "datagrams", len(events))
	return nil
}

// Close flushes and closes the connection.
func (w *SyslogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

// buildSyslog5424 constructs a RFC 5424 message from a WindowsEvent.
// The structured data element uses SD-ID "audit@32473" (Private Enterprise
// Number 32473 is the IANA example PEN used for testing per RFC 5612).
func buildSyslog5424(e WindowsEvent, appName string) ([]byte, error) {
	procID := "-"
	if e.ProcessID != 0 {
		procID = strconv.Itoa(e.ProcessID)
	}

	m := rfc5424.Message{
		Priority:  rfc5424.Daemon | rfc5424.Info,
		Timestamp: e.TimeCreated,
		Hostname:  e.Computer,
		AppName:   appName,
		ProcessID: procID,
		MessageID: strconv.Itoa(e.EventID),
		Message:   []byte(e.ShortMessage()),
	}

	sdID := "audit@32473"
	m.AddDatum(sdID, "EventID", strconv.Itoa(e.EventID))
	m.AddDatum(sdID, "User", e.SubjectUsername)
	m.AddDatum(sdID, "Domain", e.SubjectDomain)
	m.AddDatum(sdID, "Object", e.ObjectName)
	m.AddDatum(sdID, "AccessMask", e.AccessMask)
	m.AddDatum(sdID, "ClientAddr", e.ClientAddr)
	m.AddDatum(sdID, "CEPAType", e.CEPAEventType)

	return m.MarshalBinary()
}
