package evtx

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildBeatsEvent(t *testing.T) {
	e := WindowsEvent{
		EventID:         4663,
		ProviderName:    "PowerStore-CEPA",
		Computer:        "nas01.corp.local",
		TimeCreated:     time.Unix(1700000000, 0),
		SubjectUsername: "testuser",
		SubjectDomain:   "DOMAIN",
		SubjectUserSID:  "S-1-5-21-12345",
		SubjectLogonID:  "0x12345",
		ObjectName:      "/share/file.txt",
		ObjectType:      "File",
		AccessMask:      "0x2",
		Accesses:        "WriteData",
		ClientAddr:      "10.0.0.5",
		CEPAEventType:   "CEPP_FILE_WRITE",
	}

	result := buildBeatsEvent(e)

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "timestamp_is_RFC3339Nano",
			check: func(t *testing.T) {
				ts, ok := result["@timestamp"].(string)
				if !ok || ts == "" {
					t.Errorf("@timestamp: expected non-empty string, got %v", result["@timestamp"])
				}
				if !strings.Contains(ts, "T") {
					t.Errorf("@timestamp: expected RFC3339Nano format containing 'T', got %q", ts)
				}
			},
		},
		{
			name: "message_contains_cepa_event_type",
			check: func(t *testing.T) {
				msg, ok := result["message"].(string)
				if !ok {
					t.Fatalf("message: expected string, got %T", result["message"])
				}
				if !strings.Contains(msg, "CEPP_FILE_WRITE") {
					t.Errorf("message: expected to contain CEPP_FILE_WRITE, got %q", msg)
				}
			},
		},
		{
			name: "message_contains_object_name",
			check: func(t *testing.T) {
				msg, ok := result["message"].(string)
				if !ok {
					t.Fatalf("message: expected string, got %T", result["message"])
				}
				if !strings.Contains(msg, "/share/file.txt") {
					t.Errorf("message: expected to contain /share/file.txt, got %q", msg)
				}
			},
		},
		{
			name: "event_id_equals_4663",
			check: func(t *testing.T) {
				if result["event_id"] != 4663 {
					t.Errorf("event_id: expected 4663 (int), got %v (%T)", result["event_id"], result["event_id"])
				}
			},
		},
		{
			name: "user_equals_testuser",
			check: func(t *testing.T) {
				if result["user"] != "testuser" {
					t.Errorf("user: expected testuser, got %v", result["user"])
				}
			},
		},
		{
			name: "object_name_equals_file_path",
			check: func(t *testing.T) {
				if result["object_name"] != "/share/file.txt" {
					t.Errorf("object_name: expected /share/file.txt, got %v", result["object_name"])
				}
			},
		},
		{
			name: "cepa_event_type_equals_CEPP_FILE_WRITE",
			check: func(t *testing.T) {
				if result["cepa_event_type"] != "CEPP_FILE_WRITE" {
					t.Errorf("cepa_event_type: expected CEPP_FILE_WRITE, got %v", result["cepa_event_type"])
				}
			},
		},
		{
			name: "client_address_equals_10_0_0_5",
			check: func(t *testing.T) {
				if result["client_address"] != "10.0.0.5" {
					t.Errorf("client_address: expected 10.0.0.5, got %v", result["client_address"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.check)
	}
}

// listenPlainTCP starts a TCP listener that accepts connections and greets
// each one with plaintext instead of a TLS record. A TLS client therefore
// fails at the handshake — immediately, rather than after the dialer's
// 5-second ServerHello timeout — while a plain TCP client connects fine. That
// asymmetry is what lets these tests tell "we spoke TLS" apart from "we never
// connected".
func listenPlainTCP(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	// The accept loop is itself tracked by wg. Without that, wg can sit at
	// zero while the loop is mid-Accept, so cleanup's Wait returns before the
	// wg.Add(1) for a just-accepted connection ever runs, and the handler
	// outlives the test.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer func() { _ = c.Close() }()
				// Not a TLS record: 'n' is not a valid TLS content type, so
				// the client rejects it immediately instead of waiting out
				// the dialer's 5-second ServerHello timeout.
				_, _ = c.Write([]byte("not-tls\r\n"))
				// Block until the peer goes away so a plain TCP client sees a
				// live connection rather than an immediate EOF.
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// TestBeatsWriterDialerInjection verifies that cfg.TLS actually routes the
// connection through the injected tls.Dialer.
//
// The previous version of this test injected nothing and proved nothing: both
// its cases dialled 127.0.0.1:1, where the TCP connect is refused before any
// TLS handshake can begin, so the TLS and non-TLS cases returned the same
// "connection refused" and deleting the entire TLS branch left it green. Each
// case below fails if the branch is removed or weakened.
func TestBeatsWriterDialerInjection(t *testing.T) {
	host, port := listenPlainTCP(t)

	// Reachable listener, TLS off: the SyncDial path must succeed. This is the
	// control — it is what makes the TLS failures below attributable to TLS
	// rather than to connectivity.
	t.Run("plain_tcp_connects_to_a_reachable_listener", func(t *testing.T) {
		w := &BeatsWriter{cfg: BeatsConfig{Host: host, Port: port, TLS: false}}
		if err := w.dial(context.Background()); err != nil {
			t.Fatalf("dial: got %v, want success against a reachable listener", err)
		}
		_ = w.Close()
	})

	// Same reachable listener, TLS on: must fail, and specifically at the TLS
	// layer. If the tls.Dialer injection were dropped and this fell through to
	// plain SyncDial, the dial would succeed and this test would fail.
	t.Run("tls_handshakes_rather_than_connecting_in_the_clear", func(t *testing.T) {
		w := &BeatsWriter{cfg: BeatsConfig{Host: host, Port: port, TLS: true}}
		err := w.dial(context.Background())
		if err == nil {
			_ = w.Close()
			t.Fatal("dial succeeded against a non-TLS listener: " +
				"the connection was made in the clear, so TLS was not negotiated")
		}
		// The peer never sends a TLS record, so the client either times out
		// waiting for ServerHello or rejects what it reads. Either way the
		// failure must not be a connect failure.
		if strings.Contains(err.Error(), "connection refused") {
			t.Errorf("dial: got a connect error (%v); expected a TLS-layer failure", err)
		}
	})

	// The doc comment on dial() claims the caller's context is honoured so
	// that shutdown cancels a slow dial. Only the TLS branch can honour it —
	// lumberv2.SyncDial takes no context — so this pins the one path that can.
	t.Run("tls_dial_honours_a_cancelled_context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		w := &BeatsWriter{cfg: BeatsConfig{Host: host, Port: port, TLS: true}}
		done := make(chan error, 1)
		go func() { done <- w.dial(ctx) }()

		select {
		case err := <-done:
			if err == nil {
				_ = w.Close()
				t.Fatal("dial succeeded with an already-cancelled context")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("dial: got %v, want a wrapped context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("dial ignored the cancelled context and blocked; " +
				"the 5-second NetDialer timeout is not a substitute")
		}
	})
}
