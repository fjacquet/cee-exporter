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
	addr := net.JoinHostPort(w.cfg.Host, strconv.Itoa(w.cfg.Port))
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

func (w *SyslogWriter) send(payload []byte) error {
	switch w.cfg.Protocol {
	case "tcp":
		// RFC 6587 §3.4.1 octet-counting: "<length> <message>"
		if _, err := fmt.Fprintf(w.conn, "%d ", len(payload)); err != nil {
			return fmt.Errorf("syslog tcp length prefix: %w", err)
		}
		if _, err := w.conn.Write(payload); err != nil {
			return fmt.Errorf("syslog tcp payload: %w", err)
		}
	default: // udp — single datagram, no framing
		if _, err := w.conn.Write(payload); err != nil {
			return fmt.Errorf("syslog udp send: %w", err)
		}
	}
	return nil
}

// WriteEvent serialises the event as RFC 5424 syslog and sends it.
func (w *SyslogWriter) WriteEvent(ctx context.Context, e WindowsEvent) error {
	payload, err := buildSyslog5424(e, w.cfg.AppName)
	if err != nil {
		return fmt.Errorf("syslog build: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.send(payload); err != nil {
		// Attempt reconnect once — mirrors GELFWriter resilience pattern.
		slog.Warn("syslog_reconnect", "reason", err)
		if rerr := w.connect(); rerr != nil {
			return fmt.Errorf("syslog send+reconnect: %w / %w", err, rerr)
		}
		if err2 := w.send(payload); err2 != nil {
			return fmt.Errorf("syslog send after reconnect: %w", err2)
		}
	}

	slog.Debug("syslog_event_sent", "event_id", e.EventID)
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
		procID = fmt.Sprintf("%d", e.ProcessID)
	}

	m := rfc5424.Message{
		Priority:  rfc5424.Daemon | rfc5424.Info,
		Timestamp: e.TimeCreated,
		Hostname:  e.Computer,
		AppName:   appName,
		ProcessID: procID,
		MessageID: fmt.Sprintf("%d", e.EventID),
		Message:   []byte(fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName)),
	}

	sdID := "audit@32473"
	m.AddDatum(sdID, "EventID", fmt.Sprintf("%d", e.EventID))
	m.AddDatum(sdID, "User", e.SubjectUsername)
	m.AddDatum(sdID, "Domain", e.SubjectDomain)
	m.AddDatum(sdID, "Object", e.ObjectName)
	m.AddDatum(sdID, "AccessMask", e.AccessMask)
	m.AddDatum(sdID, "ClientAddr", e.ClientAddr)
	m.AddDatum(sdID, "CEPAType", e.CEPAEventType)

	return m.MarshalBinary()
}
