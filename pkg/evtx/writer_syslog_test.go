package evtx

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
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
		errCh <- w.sendRaw(frameTCP(nil, payload))
	}()

	// Read from the server side through a single bufio.Reader. sendRaw now
	// issues the prefix and payload as ONE Write (that concatenation is the
	// whole point of the batching change), so net.Pipe delivers both in one
	// underlying Read once the frame fits in the reader's buffer. Reading
	// the prefix and payload through two different readers — a
	// bufio.Scanner, then a raw server.Read — left the payload bytes
	// stranded in the Scanner's internal buffer and the raw Read blocking
	// forever; that combination is what to avoid here.
	br := bufio.NewReader(server)
	lengthStr, err := br.ReadString(' ')
	if err != nil {
		t.Fatalf("failed to read length prefix from TCP frame: %v", err)
	}
	lengthStr = strings.TrimSuffix(lengthStr, " ")

	var frameLen int
	if _, err := fmt.Sscanf(lengthStr, "%d", &frameLen); err != nil {
		t.Fatalf("expected numeric length prefix, got %q: %v", lengthStr, err)
	}

	if frameLen != len(payload) {
		t.Errorf("expected frame length %d, got %d", len(payload), frameLen)
	}

	// Read the exact number of bytes declared in the length prefix.
	buf := make([]byte, frameLen)
	n, err := io.ReadFull(br, buf)
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
		t.Errorf("sendRaw() returned error: %v", err)
	}
}

// TestSyslogWriteBatchOctetCounting reads the frames back out. One wrong
// length prefix in a concatenated batch desynchronises the receiver for
// every subsequent message, and the failure appears at the collector, not
// here.
func TestSyslogWriteBatchOctetCounting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	msgs := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		br := bufio.NewReader(c)

		var out []string
		for i := 0; i < 3; i++ {
			// RFC 6587 §3.4.1: "<decimal length> <message>"
			lenStr, err := br.ReadString(' ')
			if err != nil {
				break
			}
			n, err := strconv.Atoi(strings.TrimSpace(lenStr))
			if err != nil {
				break
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(br, payload); err != nil {
				break
			}
			out = append(out, string(payload))
		}
		msgs <- out
	}()

	host, port := splitHostPort(t, ln.Addr().String())
	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := []WindowsEvent{
		{EventID: 4663, Computer: "NAS01", ObjectName: "/a", CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 4660, Computer: "NAS01", ObjectName: "/b", CEPAEventType: "CEPP_DELETE_FILE"},
		{EventID: 4670, Computer: "NAS01", ObjectName: "/c", CEPAEventType: "CEPP_SETACL_FILE"},
	}
	if err := w.WriteBatch(context.Background(), batch); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	got := <-msgs
	if len(got) != 3 {
		t.Fatalf("recovered %d framed messages, want 3 — octet counting is wrong", len(got))
	}
	for i, m := range got {
		if !strings.HasPrefix(m, "<") {
			t.Errorf("message %d does not start with an RFC 5424 PRI: %q", i, m)
		}
	}
}

// TestSyslogWriteBatchTCPSingleWrite is the actual batching guard: it
// asserts the write count at the writer, not at the sink. TCP is a stream —
// one Write may split across reads, and several writes may coalesce into
// one — so a read/write count observed at the sink is flaky in both
// directions. Counting Write calls on the wrapped net.Conn is exact.
//
// Today's per-event path issues two syscalls per event (length prefix,
// then payload), so 3 events would be 6 writes; this batch must be 1.
func TestSyslogWriteBatchTCPSingleWrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 65536)
		for {
			if _, err := c.Read(buf); err != nil {
				return
			}
		}
	}()

	host, port := splitHostPort(t, ln.Addr().String())
	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}

	cc := &countingConn{Conn: w.conn}
	w.conn = cc

	batch := []WindowsEvent{
		{EventID: 4663, CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 4660, CEPAEventType: "CEPP_DELETE_FILE"},
		{EventID: 4670, CEPAEventType: "CEPP_SETACL_FILE"},
	}
	writeErr := w.WriteBatch(context.Background(), batch)

	_ = w.Close()
	_ = ln.Close()
	wg.Wait()

	if writeErr != nil {
		t.Fatalf("WriteBatch: %v", writeErr)
	}
	if got := cc.count(); got != 1 {
		t.Errorf("3 events produced %d writes, want 1; the batch is not being concatenated into a single write", got)
	}
}

// TestSyslogWriteBatchUDPWritesOnePerEvent guards against a future
// "optimisation" that concatenates datagrams: RFC 5426 requires one syslog
// message per UDP datagram. This assertion does not discriminate the
// writeBatchSerially stub — both produce one datagram per event, because
// UDP's win is the single lock acquisition rather than fewer writes — but it
// stands as a regression guard.
func TestSyslogWriteBatchUDPWritesOnePerEvent(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65535)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()

	host, port := splitHostPort(t, pc.LocalAddr().String())
	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "udp"})
	if err != nil {
		t.Fatal(err)
	}

	cc := &countingConn{Conn: w.conn}
	w.conn = cc

	batch := []WindowsEvent{
		{EventID: 4663, CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 4660, CEPAEventType: "CEPP_DELETE_FILE"},
		{EventID: 4670, CEPAEventType: "CEPP_SETACL_FILE"},
	}
	writeErr := w.WriteBatch(context.Background(), batch)

	_ = w.Close()
	_ = pc.Close()
	wg.Wait()

	if writeErr != nil {
		t.Fatalf("WriteBatch: %v", writeErr)
	}
	if got := cc.count(); got != 3 {
		t.Errorf("3 events produced %d writes, want 3 — UDP must send one datagram per message, never concatenated", got)
	}
}
