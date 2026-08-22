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
	cfg  GELFConfig
	mu   sync.Mutex
	conn net.Conn
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
	addr := hostPort(w.cfg.Host, w.cfg.Port)
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

// writeDeadline bounds how long a single Write may block before returning an
// error. Prevents a stalled Graylog from pinning all queue workers.
const writeDeadline = 5 * time.Second

// sendRaw writes b with a deadline and adds no framing. The caller has already
// framed the payload, which is what lets the batch path concatenate K frames
// into a single Write.
func (w *GELFWriter) sendRaw(b []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := w.conn.Write(b)
	return err
}

// retrySend runs send, reconnecting and retrying once on failure. Caller must
// hold w.mu.
func (w *GELFWriter) retrySend(send func() error) error {
	return sendWithRetry(send, func() error {
		slog.Warn("gelf_reconnect")
		return w.connect()
	})
}

// WriteEvent serialises the event as GELF JSON and sends it.
func (w *GELFWriter) WriteEvent(_ context.Context, e WindowsEvent) error {
	payload, err := buildGELF(e)
	if err != nil {
		return fmt.Errorf("gelf build: %w", err)
	}
	if w.cfg.Protocol == "tcp" {
		payload = append(payload, 0x00)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.retrySend(func() error { return w.sendRaw(payload) }); err != nil {
		return fmt.Errorf("gelf %w", err)
	}

	slog.Debug("gelf_event_sent",
		"event_id", e.EventID,
		"file_path", e.ObjectName,
		"cepa_event_type", e.CEPAEventType,
	)
	return nil
}

// WriteBatch sends every event in one lock acquisition.
//
// On TCP that is a single Write carrying K null-terminated frames, which is
// where the throughput comes from: the per-event path took the mutex K times
// and issued K writes, and measured, sixteen workers against it bought 1.4x.
//
// On UDP the events go as K separate datagrams, by protocol — a GELF datagram
// is one message. The win there is the lock, taken once instead of K times,
// which is roughly half the per-event cost since buildGELF already runs
// outside it.
func (w *GELFWriter) WriteBatch(_ context.Context, events []WindowsEvent) error {
	if len(events) == 0 {
		return nil
	}

	// Build before locking: this is the half of the cost that parallelises,
	// and holding the lock across it would give the batch back nothing.
	payloads := make([][]byte, 0, len(events))
	total := 0
	for i := range events {
		p, err := buildGELF(events[i])
		if err != nil {
			return fmt.Errorf("gelf build event %d/%d: %w", i+1, len(events), err)
		}
		payloads = append(payloads, p)
		total += len(p) + 1
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cfg.Protocol == "tcp" {
		// append on a preallocated slice rather than bytes.Buffer: Buffer.Write
		// returns an error that errcheck requires handling, for a write that
		// cannot fail.
		frame := make([]byte, 0, total)
		for _, p := range payloads {
			frame = append(frame, p...)
			frame = append(frame, 0x00)
		}
		if err := w.retrySend(func() error { return w.sendRaw(frame) }); err != nil {
			return fmt.Errorf("gelf batch %w", err)
		}
		slog.Debug("gelf_batch_sent", "events", len(events), "bytes", len(frame))
		return nil
	}

	for i, p := range payloads {
		if err := w.retrySend(func() error { return w.sendRaw(p) }); err != nil {
			return fmt.Errorf("gelf batch event %d/%d: %w", i+1, len(events), err)
		}
	}
	slog.Debug("gelf_batch_sent", "events", len(events), "datagrams", len(events))
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

	msg := e.ShortMessage()
	if len(msg) > 250 {
		msg = msg[:250]
	}

	m := map[string]interface{}{
		"version":          "1.1",
		"host":             e.Computer,
		"short_message":    msg,
		"timestamp":        ts,
		"level":            6, // Informational
		"_event_id":        e.EventID,
		"_provider":        e.ProviderName,
		"_object_name":     e.ObjectName,
		"_object_type":     e.ObjectType,
		"_account_name":    e.SubjectUsername,
		"_account_domain":  e.SubjectDomain,
		"_account_sid":     e.SubjectUserSID,
		"_logon_id":        e.SubjectLogonID,
		"_client_address":  e.ClientAddr,
		"_access_mask":     e.AccessMask,
		"_accesses":        e.Accesses,
		"_cepa_event_type": e.CEPAEventType,
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
