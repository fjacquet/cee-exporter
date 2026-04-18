// GELF writer — all platforms.
//
// Sends GELF 1.1 JSON payloads to a Graylog GELF Input over TCP or UDP.
// No external dependency: stdlib encoding/json + net.
//
// GELF 1.1 spec: https://go2docs.graylog.org/current/getting_in_log_data/gelf.html
//
// Field mapping:
//
//	GELF field        → source
//	version           → "1.1" (static)
//	host              → WindowsEvent.Computer  (NAS hostname)
//	short_message     → "<EventType> on <FilePath>"
//	timestamp         → WindowsEvent.TimeCreated (float64 Unix seconds)
//	level             → 6 (informational)
//	_event_id         → WindowsEvent.EventID
//	_provider         → WindowsEvent.ProviderName
//	_object_name      → WindowsEvent.ObjectName
//	_account_name     → WindowsEvent.SubjectUsername
//	_account_domain   → WindowsEvent.SubjectDomain
//	_account_sid      → WindowsEvent.SubjectUserSID
//	_logon_id         → WindowsEvent.SubjectLogonID
//	_client_address   → WindowsEvent.ClientAddr
//	_access_mask      → WindowsEvent.AccessMask
//	_accesses         → WindowsEvent.Accesses
//	_bytes_read       → WindowsEvent.BytesRead      (omitted when 0)
//	_bytes_written    → WindowsEvent.BytesWritten   (omitted when 0)
//	_cepa_event_type  → WindowsEvent.CEPAEventType
package evtx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// GELFConfig controls the GELF writer behaviour.
type GELFConfig struct {
	Host     string // Graylog host
	Port     int    // Default 12201
	Protocol string // "tcp" or "udp"
	TLS      bool   // Wrap TCP in TLS (requires TCP)
}

// GELFWriter implements Writer.
type GELFWriter struct {
	cfg   GELFConfig
	mu    sync.Mutex
	conn  net.Conn
}

// NewGELFWriter creates a GELFWriter and opens the initial connection.
func NewGELFWriter(cfg GELFConfig) (*GELFWriter, error) {
	if cfg.Port == 0 {
		cfg.Port = 12201
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "udp"
	}
	w := &GELFWriter{cfg: cfg}
	if err := w.connect(); err != nil {
		return nil, err
	}
	slog.Info("gelf_writer_ready",
		"host", cfg.Host,
		"port", cfg.Port,
		"protocol", cfg.Protocol,
		"tls", cfg.TLS,
	)
	return w, nil
}

func (w *GELFWriter) connect() error {
	addr := net.JoinHostPort(w.cfg.Host, strconv.Itoa(w.cfg.Port))
	proto := w.cfg.Protocol

	var conn net.Conn
	var err error

	switch proto {
	case "tcp":
		if w.cfg.TLS {
			conn, err = tls.Dial("tcp", addr, &tls.Config{MinVersion: tls.VersionTLS12})
		} else {
			conn, err = net.DialTimeout("tcp", addr, 5*time.Second)
		}
	default: // udp
		conn, err = net.DialTimeout("udp", addr, 5*time.Second)
	}
	if err != nil {
		return fmt.Errorf("gelf connect %s://%s: %w", proto, addr, err)
	}
	if w.conn != nil {
		_ = w.conn.Close()
	}
	w.conn = conn
	return nil
}

// WriteEvent serialises the event as GELF JSON and sends it.
func (w *GELFWriter) WriteEvent(ctx context.Context, e WindowsEvent) error {
	payload, err := buildGELF(e)
	if err != nil {
		return fmt.Errorf("gelf build: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.send(payload); err != nil {
		// Attempt reconnect once.
		slog.Warn("gelf_reconnect", "reason", err)
		if rerr := w.connect(); rerr != nil {
			return fmt.Errorf("gelf send+reconnect: %w / %w", err, rerr)
		}
		if err2 := w.send(payload); err2 != nil {
			return fmt.Errorf("gelf send after reconnect: %w", err2)
		}
	}

	slog.Debug("gelf_event_sent",
		"event_id", e.EventID,
		"file_path", e.ObjectName,
		"cepa_event_type", e.CEPAEventType,
	)
	return nil
}

func (w *GELFWriter) send(payload []byte) error {
	switch w.cfg.Protocol {
	case "tcp":
		// GELF TCP: payload + null byte terminator
		frame := append(payload, 0x00)
		if _, err := w.conn.Write(frame); err != nil {
			return err
		}
	default: // udp — single datagram, no framing
		if _, err := w.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// Close flushes and closes the connection.
func (w *GELFWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

// ----------------------------------------------------------------------------
// GELF JSON construction
// ----------------------------------------------------------------------------

func buildGELF(e WindowsEvent) ([]byte, error) {
	ts := float64(e.TimeCreated.UnixNano()) / 1e9

	msg := fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName)
	if len(msg) > 250 {
		msg = msg[:250]
	}

	m := map[string]interface{}{
		"version":           "1.1",
		"host":              e.Computer,
		"short_message":     msg,
		"timestamp":         ts,
		"level":             6, // Informational
		"_event_id":         e.EventID,
		"_provider":         e.ProviderName,
		"_object_name":      e.ObjectName,
		"_object_type":      e.ObjectType,
		"_account_name":     e.SubjectUsername,
		"_account_domain":   e.SubjectDomain,
		"_account_sid":      e.SubjectUserSID,
		"_logon_id":         e.SubjectLogonID,
		"_client_address":   e.ClientAddr,
		"_access_mask":      e.AccessMask,
		"_accesses":         e.Accesses,
		"_cepa_event_type":  e.CEPAEventType,
	}

	if e.BytesRead > 0 {
		m["_bytes_read"] = e.BytesRead
	}
	if e.BytesWritten > 0 {
		m["_bytes_written"] = e.BytesWritten
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	// json.Encoder appends a newline; strip it for cleaner UDP datagrams.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
