// BeatsWriter — all platforms.
// Sends Lumberjack v2 frames to Logstash or Graylog Beats Input.
// TLS: injected via SyncDialWith with tls.Dialer (go-lumber has no TLS Option).
// Thread safety: sync.Mutex serialises SyncClient.Send (SyncClient is not thread-safe).
// Reconnect: SyncClient closed and recreated on send failure (SyncClient cannot recover).
package evtx

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	lumberv2 "github.com/elastic/go-lumber/client/v2"
)

// BeatsConfig controls the BeatsWriter behaviour.
type BeatsConfig struct {
	Host string // Logstash/Graylog Beats Input host
	Port int    // Default 5044
	TLS  bool   // Wrap connection in TLS
}

// BeatsWriter implements Writer.
// It forwards events using the Lumberjack v2 (Beats) protocol.
type BeatsWriter struct {
	cfg    BeatsConfig
	mu     sync.Mutex
	client *lumberv2.SyncClient
}

// NewBeatsWriter creates a BeatsWriter and establishes the initial connection.
func NewBeatsWriter(cfg BeatsConfig) (*BeatsWriter, error) {
	if cfg.Port == 0 {
		cfg.Port = 5044
	}
	w := &BeatsWriter{cfg: cfg}
	if err := w.dial(context.Background()); err != nil {
		return nil, fmt.Errorf("beats_writer: initial dial: %w", err)
	}
	slog.Info("beats_writer_ready",
		"host", cfg.Host,
		"port", cfg.Port,
		"tls", cfg.TLS,
	)
	return w, nil
}

// dial establishes the underlying TCP/TLS connection and creates a new SyncClient.
// For TLS connections a tls.Dialer is injected via SyncDialWith because go-lumber
// has no built-in TLS option. The caller's context is honoured so that shutdown
// cancels a slow dial instead of waiting for the 5-second Dialer timeout.
func (w *BeatsWriter) dial(ctx context.Context) error {
	addr := net.JoinHostPort(w.cfg.Host, strconv.Itoa(w.cfg.Port))

	var (
		cl  *lumberv2.SyncClient
		err error
	)

	if w.cfg.TLS {
		tlsDialer := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: 5 * time.Second},
			Config:    &tls.Config{MinVersion: tls.VersionTLS12},
		}
		cl, err = lumberv2.SyncDialWith(
			func(network, address string) (net.Conn, error) {
				return tlsDialer.DialContext(ctx, network, address)
			},
			addr,
			lumberv2.Timeout(30*time.Second),
		)
	} else {
		cl, err = lumberv2.SyncDial(addr, lumberv2.Timeout(30*time.Second))
	}

	if err != nil {
		return fmt.Errorf("beats dial %s: %w", addr, err)
	}
	w.client = cl
	return nil
}

// WriteEvent serialises the event as a Lumberjack v2 frame and sends it.
// If the send fails the client is closed, a new connection is dialled, and the
// send is retried once.
func (w *BeatsWriter) WriteEvent(ctx context.Context, e WindowsEvent) error {
	event := buildBeatsEvent(e)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.client.Send([]interface{}{event}); err != nil {
		slog.Warn("beats_reconnect", "reason", err)
		_ = w.client.Close()
		if rerr := w.dial(ctx); rerr != nil {
			return fmt.Errorf("beats send+reconnect: %w / %w", err, rerr)
		}
		if _, err2 := w.client.Send([]interface{}{event}); err2 != nil {
			return fmt.Errorf("beats send after reconnect: %w", err2)
		}
	}

	slog.Debug("beats_event_sent",
		"event_id", e.EventID,
		"cepa_event_type", e.CEPAEventType,
	)
	return nil
}

// Close closes the underlying Lumberjack client connection.
func (w *BeatsWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client != nil {
		return w.client.Close()
	}
	return nil
}

// buildBeatsEvent maps a WindowsEvent to a Beats-compatible map.
// The @timestamp field is formatted as RFC3339Nano (UTC).
func buildBeatsEvent(e WindowsEvent) map[string]interface{} {
	return map[string]interface{}{
		"@timestamp":      e.TimeCreated.UTC().Format(time.RFC3339Nano),
		"message":         fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName),
		"event_id":        e.EventID,
		"provider":        e.ProviderName,
		"computer":        e.Computer,
		"user":            e.SubjectUsername,
		"domain":          e.SubjectDomain,
		"user_sid":        e.SubjectUserSID,
		"logon_id":        e.SubjectLogonID,
		"object_name":     e.ObjectName,
		"object_type":     e.ObjectType,
		"access_mask":     e.AccessMask,
		"accesses":        e.Accesses,
		"client_address":  e.ClientAddr,
		"cepa_event_type": e.CEPAEventType,
	}
}
