package evtx

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// syslogTestEvent is the fixture for the buildSyslog5424 tests. Every field
// carries a value distinct from every other, so an assertion on one cannot be
// satisfied by another leaking into its place.
func syslogTestEvent() WindowsEvent {
	return WindowsEvent{
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
}

// TestBuildSyslog5424 verifies that buildSyslog5424 emits every audit@32473
// structured-data parameter, as a full Key="Value" pair.
//
// The previous version asserted bare substrings: "EventID", "User", "Object",
// plus three values. Three of the seven SD-PARAMs — Domain, AccessMask,
// ClientAddr — went unmentioned, so deleting their AddDatum calls left the
// test green under a name that claimed "all required fields present". The
// bare-key form was weak even for the keys it did name: "EventID" matches the
// key alone, so emitting the wrong value, or none, also passed.
//
// Asserting the rendered Key="Value" pair fixes both. Because the fixture
// gives every field a distinct value, swapping two AddDatum arguments fails
// too — the wrong pairing no longer appears anywhere in the payload.
func TestBuildSyslog5424(t *testing.T) {
	e := syslogTestEvent()

	payload, err := buildSyslog5424(e, "cee-exporter")
	if err != nil {
		t.Fatalf("buildSyslog5424 returned error: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("buildSyslog5424 returned empty payload")
	}
	msg := string(payload)

	// One entry per AddDatum call in buildSyslog5424. Adding a parameter there
	// without adding it here leaves it unverified, which is how the three
	// missing ones got in.
	sdParams := []string{
		`EventID="4663"`,
		`User="testuser"`,
		`Domain="DOMAIN"`,
		`Object="/share/file.txt"`,
		`AccessMask="0x2"`,
		`ClientAddr="10.0.0.5"`,
		`CEPAType="CEPP_FILE_WRITE"`,
	}
	for _, want := range sdParams {
		if !strings.Contains(msg, want) {
			t.Errorf("missing SD-PARAM %s\npayload: %s", want, msg)
		}
	}

	// The header fields, which no assertion covered beyond hostname and
	// app-name appearing somewhere in the string.
	header := []struct{ name, want string }{
		{"PRI + version", "<30>1 "},
		{"timestamp", "2023-11-14T22:13:20Z"},
		{"hostname", " nas01.corp.local "},
		{"app-name", " cee-exporter "},
		{"MSGID (the event ID)", " 4663 ["},
		{"SD-ID", "[audit@32473 "},
	}
	for _, h := range header {
		if !strings.Contains(msg, h.want) {
			t.Errorf("%s: expected %q\npayload: %s", h.name, h.want, msg)
		}
	}

	// The MSG body is ShortMessage, the same summary every textual writer uses.
	if !strings.HasSuffix(msg, "] "+e.ShortMessage()) {
		t.Errorf("payload does not end with the structured data followed by %q\npayload: %s",
			e.ShortMessage(), msg)
	}
}

// TestBuildSyslog5424ProcID covers the one branch in buildSyslog5424 that the
// test above cannot reach: ProcessID 0 renders as the RFC 5424 nil value "-",
// any other value renders as the number.
func TestBuildSyslog5424ProcID(t *testing.T) {
	tests := []struct {
		name      string
		processID int
		want      string
	}{
		{"zero_becomes_nil_value", 0, " cee-exporter - 4663 "},
		{"nonzero_is_rendered", 4242, " cee-exporter 4242 4663 "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := syslogTestEvent()
			e.ProcessID = tc.processID

			payload, err := buildSyslog5424(e, "cee-exporter")
			if err != nil {
				t.Fatalf("buildSyslog5424 returned error: %v", err)
			}
			if !strings.Contains(string(payload), tc.want) {
				t.Errorf("expected PROCID field %q\npayload: %s", tc.want, payload)
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
