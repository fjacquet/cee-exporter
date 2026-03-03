package evtx

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestBuildSyslog5424 tests the pure buildSyslog5424 helper function.
// It verifies that the output is a valid RFC 5424 message containing all
// required audit@32473 structured-data fields.
func TestBuildSyslog5424(t *testing.T) {
	e := WindowsEvent{
		EventID:         4663,
		Computer:        "nas01.corp.local",
		TimeCreated:     time.Unix(1700000000, 0).UTC(),
		SubjectUsername: "testuser",
		SubjectDomain:   "DOMAIN",
		ObjectName:      "/share/file.txt",
		AccessMask:      "0x2",
		ClientAddr:      "10.0.0.5",
		CEPAEventType:   "CEPP_FILE_WRITE",
	}

	tests := []struct {
		name     string
		appName  string
		contains []string
	}{
		{
			name:    "all required fields present",
			appName: "cee-exporter",
			contains: []string{
				"<",               // RFC 5424 PRI field start
				"audit@32473",     // SD-ID
				"EventID",         // SD-PARAM key
				"User",            // SD-PARAM key
				"Object",          // SD-PARAM key
				"CEPP_FILE_WRITE", // SD-PARAM value for CEPAType
				"nas01.corp.local", // hostname
				"cee-exporter",    // app-name
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := buildSyslog5424(e, tc.appName)
			if err != nil {
				t.Fatalf("buildSyslog5424 returned error: %v", err)
			}
			if len(payload) == 0 {
				t.Fatal("buildSyslog5424 returned empty payload")
			}

			msg := string(payload)

			if !strings.HasPrefix(msg, "<") {
				t.Errorf("expected payload to start with '<' (RFC 5424 PRI), got: %.20q", msg)
			}

			for _, want := range tc.contains {
				if !strings.Contains(msg, want) {
					t.Errorf("expected payload to contain %q\npayload: %s", want, msg)
				}
			}
		})
	}
}

// TestSyslogTCPFraming tests that TCP octet-counting framing (RFC 6587 §3.4.1)
// prefixes the payload with a numeric byte count followed by a space.
func TestSyslogTCPFraming(t *testing.T) {
	// Create an in-memory pipe to simulate TCP connection.
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	w := &SyslogWriter{
		cfg: SyslogConfig{
			Host:     "localhost",
			Port:     514,
			Protocol: "tcp",
			AppName:  "cee-exporter",
		},
		conn: client,
	}

	payload := []byte("<165>1 2023-11-14T22:13:20Z nas01.corp.local cee-exporter - 4663 [audit@32473 EventID=\"4663\"] CEPP_FILE_WRITE on /share/file.txt")

	// Send in a goroutine so we can read from the other end.
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.send(payload)
	}()

	// Read from the server side.
	scanner := bufio.NewScanner(server)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		// Read until we find a space (end of length prefix).
		for i, b := range data {
			if b == ' ' {
				return i + 1, data[:i], nil
			}
		}
		return 0, nil, nil
	})

	if !scanner.Scan() {
		t.Fatal("failed to read length prefix from TCP frame")
	}

	lengthStr := scanner.Text()
	var frameLen int
	if _, err := fmt.Sscanf(lengthStr, "%d", &frameLen); err != nil {
		t.Fatalf("expected numeric length prefix, got %q: %v", lengthStr, err)
	}

	if frameLen != len(payload) {
		t.Errorf("expected frame length %d, got %d", len(payload), frameLen)
	}

	// Read the exact number of bytes declared in the length prefix.
	buf := make([]byte, frameLen)
	n, err := server.Read(buf)
	if err != nil {
		t.Fatalf("failed to read payload: %v", err)
	}
	if n != frameLen {
		t.Errorf("expected %d payload bytes, got %d", frameLen, n)
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("payload mismatch:\nwant: %q\ngot:  %q", payload, buf[:n])
	}

	if err := <-errCh; err != nil {
		t.Errorf("send() returned error: %v", err)
	}
}
