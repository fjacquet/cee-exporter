package evtx

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildGELF(t *testing.T) {
	e := WindowsEvent{
		EventID:         4663,
		ProviderName:    "PowerStore-CEPA",
		Computer:        "nas01.corp.local",
		Channel:         "Security",
		TimeCreated:     time.Unix(1700000000, 0),
		SubjectUsername: "testuser",
		SubjectDomain:   "DOMAIN",
		SubjectUserSID:  "S-1-5-21-1234",
		SubjectLogonID:  "0x3e7",
		ObjectName:      "/share/file.txt",
		ObjectType:      "File",
		AccessMask:      "0x2",
		Accesses:        "WriteData (or AddFile)",
		ClientAddr:      "10.0.0.5",
		CEPAEventType:   "CEPP_FILE_WRITE",
	}

	payload, err := buildGELF(e)
	if err != nil {
		t.Fatalf("buildGELF returned error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}

	// Assert all required GELF 1.1 fields are present.
	requiredFields := []string{
		"version", "host", "short_message", "timestamp", "level",
		"_event_id", "_object_name", "_account_name", "_account_domain",
		"_client_address", "_access_mask", "_cepa_event_type",
	}
	for _, field := range requiredFields {
		if _, ok := m[field]; !ok {
			t.Errorf("required GELF field %q is missing", field)
		}
	}

	// Assert specific values.
	if v, ok := m["version"]; !ok || v != "1.1" {
		t.Errorf("version: expected \"1.1\", got %v", v)
	}
	if v, ok := m["host"]; !ok || v != "nas01.corp.local" {
		t.Errorf("host: expected \"nas01.corp.local\", got %v", v)
	}
	if v, ok := m["level"]; !ok || v != float64(6) {
		t.Errorf("level: expected float64(6), got %v (%T)", v, v)
	}
	if v, ok := m["_event_id"]; !ok || v != float64(4663) {
		t.Errorf("_event_id: expected float64(4663), got %v (%T)", v, v)
	}
	if v, ok := m["_cepa_event_type"]; !ok || v != "CEPP_FILE_WRITE" {
		t.Errorf("_cepa_event_type: expected \"CEPP_FILE_WRITE\", got %v", v)
	}
	if v, ok := m["timestamp"]; !ok {
		t.Error("timestamp field is missing")
	} else if ts, ok := v.(float64); !ok || ts <= 0 {
		t.Errorf("timestamp: expected float64 > 0, got %v (%T)", v, v)
	}

	// Assert GELF 1.1 reserved field _id is NOT present.
	if _, ok := m["_id"]; ok {
		t.Error("GELF 1.1: _id field is reserved and must not be set")
	}
}

func TestBuildGELFBytesFields(t *testing.T) {
	// Case 1: BytesRead=0, BytesWritten=0 — neither field should appear.
	e1 := WindowsEvent{
		EventID:       4663,
		CEPAEventType: "CEPP_FILE_WRITE",
		BytesRead:     0,
		BytesWritten:  0,
	}
	p1, err := buildGELF(e1)
	if err != nil {
		t.Fatalf("buildGELF returned error: %v", err)
	}
	var m1 map[string]interface{}
	if err := json.Unmarshal(p1, &m1); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if _, ok := m1["_bytes_read"]; ok {
		t.Error("_bytes_read should be omitted when BytesRead == 0")
	}
	if _, ok := m1["_bytes_written"]; ok {
		t.Error("_bytes_written should be omitted when BytesWritten == 0")
	}

	// Case 2: BytesRead=1024, BytesWritten=4096 — both fields must be present with correct values.
	e2 := WindowsEvent{
		EventID:       4663,
		CEPAEventType: "CEPP_FILE_WRITE",
		BytesRead:     1024,
		BytesWritten:  4096,
	}
	p2, err := buildGELF(e2)
	if err != nil {
		t.Fatalf("buildGELF returned error: %v", err)
	}
	var m2 map[string]interface{}
	if err := json.Unmarshal(p2, &m2); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if v, ok := m2["_bytes_read"]; !ok || v != float64(1024) {
		t.Errorf("_bytes_read: expected float64(1024), got %v (%T)", v, v)
	}
	if v, ok := m2["_bytes_written"]; !ok || v != float64(4096) {
		t.Errorf("_bytes_written: expected float64(4096), got %v (%T)", v, v)
	}
}

// TestBuildGELFShortMessageTruncation exercises the 250-byte cap in buildGELF
// across its boundary.
//
// The name has always promised this; the body did not deliver it. It used a
// 15-byte ObjectName, so short_message came to 34 bytes and the `len(msg) >
// 250` branch was never entered — deleting the truncation entirely left the
// test green. The cases below are chosen so that each one fails under a
// distinct off-by-one: an over-length input catches removal and an
// off-by-one bound, exactly-250 catches a `>=` in place of `>`, and 251
// catches a bound of 251.
func TestBuildGELFShortMessageTruncation(t *testing.T) {
	// shortMessageFor builds an ObjectName whose resulting short_message is
	// exactly n bytes: "CEPP_FILE_WRITE on " is the fixed prefix.
	const prefix = "CEPP_FILE_WRITE on "
	objectNameFor := func(n int) string {
		if n < len(prefix) {
			t.Fatalf("cannot build a %d-byte short_message; prefix is %d", n, len(prefix))
		}
		return strings.Repeat("a", n-len(prefix))
	}

	tests := []struct {
		name        string
		shortMsgLen int
		wantLen     int
	}{
		{"under_cap_is_untouched", 34, 34},
		{"exactly_250_is_untouched", 250, 250},
		{"251_is_cut_to_250", 251, 250},
		{"far_over_cap_is_cut_to_250", 4000, 250},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objectName := objectNameFor(tc.shortMsgLen)
			e := WindowsEvent{
				ObjectName:    objectName,
				CEPAEventType: "CEPP_FILE_WRITE",
			}
			full := e.ShortMessage()
			if len(full) != tc.shortMsgLen {
				t.Fatalf("test setup wrong: ShortMessage is %d bytes, want %d",
					len(full), tc.shortMsgLen)
			}

			payload, err := buildGELF(e)
			if err != nil {
				t.Fatalf("buildGELF returned error: %v", err)
			}
			var m map[string]interface{}
			if err := json.Unmarshal(payload, &m); err != nil {
				t.Fatalf("payload is not valid JSON: %v", err)
			}

			sm, ok := m["short_message"].(string)
			if !ok {
				t.Fatalf("short_message: got %v (%T), want string",
					m["short_message"], m["short_message"])
			}
			if len(sm) != tc.wantLen {
				t.Errorf("short_message length = %d, want %d", len(sm), tc.wantLen)
			}
			// Truncation must cut the tail, never rewrite the head: the event
			// type and the start of the path are what a Graylog search matches.
			if !strings.HasPrefix(full, sm) {
				t.Errorf("short_message is not a prefix of %q", full[:min(len(full), 60)])
			}
			if !strings.HasPrefix(sm, prefix) {
				t.Errorf("short_message lost its %q prefix: %q", prefix, sm[:min(len(sm), 40)])
			}
		})
	}
}

// TestGELFWriteBatchTCPFraming reads the batch back off the wire. TCP GELF
// frames are null-terminated and concatenated into ONE write; a framing bug
// here is invisible locally and shows up as unparseable messages at Graylog.
func TestGELFWriteBatchTCPFraming(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type result struct {
		frames [][]byte
		writes int
	}
	results := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var all []byte
		writes := 0
		buf := make([]byte, 65536)
		for {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := c.Read(buf)
			if n > 0 {
				writes++
				all = append(all, buf[:n]...)
			}
			if err != nil {
				break
			}
			if bytes.Count(all, []byte{0x00}) >= 3 {
				break
			}
		}
		frames := bytes.Split(bytes.TrimRight(all, "\x00"), []byte{0x00})
		results <- result{frames: frames, writes: writes}
	}()

	host, port := splitHostPort(t, ln.Addr().String())
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
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

	got := <-results
	if len(got.frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(got.frames))
	}
	for i, f := range got.frames {
		var m map[string]any
		if err := json.Unmarshal(f, &m); err != nil {
			t.Fatalf("frame %d is not valid JSON: %v", i, err)
		}
		if m["version"] != "1.1" {
			t.Errorf("frame %d version = %v, want 1.1", i, m["version"])
		}
	}
}

// TestGELFWriteBatchUDPIsOneDatagramPerEvent is the assertion that stops
// someone "optimising" UDP into a concatenation later. A GELF datagram is one
// message; concatenating produces garbage at the collector, and the chunked
// format (0x1e 0x0f magic) is not implemented here.
func TestGELFWriteBatchUDPIsOneDatagramPerEvent(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()

	count := make(chan int, 1)
	go func() {
		buf := make([]byte, 65535)
		n := 0
		for n < 3 {
			_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := pc.ReadFrom(buf); err != nil {
				break
			}
			n++
		}
		count <- n
	}()

	host, port := splitHostPort(t, pc.LocalAddr().String())
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "udp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	batch := []WindowsEvent{{EventID: 4663}, {EventID: 4660}, {EventID: 4670}}
	if err := w.WriteBatch(context.Background(), batch); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	if got := <-count; got != 3 {
		t.Errorf("received %d datagrams, want 3 — UDP must not concatenate", got)
	}
}

// countingConn wraps a net.Conn and counts Write calls. The batch deliverable
// is "K events become ONE write", and nothing observable at the sink can
// prove that — TCP may split one Write across reads or coalesce several.
// Counting at the writer is exact and deterministic.
type countingConn struct {
	net.Conn
	mu     sync.Mutex
	writes int
}

func (c *countingConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.Conn.Write(b)
}

func (c *countingConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

// TestGELFWriteBatchTCPSingleWrite is the actual batching guard: it asserts
// the write count at the writer, not at the sink. TCP is a stream — one
// Write may split across reads, and several writes may coalesce into one —
// so a read/write count observed at the sink is flaky in both directions.
// Counting Write calls on the wrapped net.Conn is exact.
func TestGELFWriteBatchTCPSingleWrite(t *testing.T) {
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
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
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

// TestGELFWriteBatchUDPWritesOnePerEvent is the other half of the batching
// guard: it catches someone later "optimising" UDP into a concatenation,
// which would produce garbage at the collector — a GELF datagram is one
// message.
func TestGELFWriteBatchUDPWritesOnePerEvent(t *testing.T) {
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
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "udp"})
	if err != nil {
		t.Fatal(err)
	}

	cc := &countingConn{Conn: w.conn}
	w.conn = cc

	batch := []WindowsEvent{{EventID: 4663}, {EventID: 4660}, {EventID: 4670}}
	writeErr := w.WriteBatch(context.Background(), batch)

	_ = w.Close()
	_ = pc.Close()
	wg.Wait()

	if writeErr != nil {
		t.Fatalf("WriteBatch: %v", writeErr)
	}
	if got := cc.count(); got != 3 {
		t.Errorf("3 events produced %d writes, want 3 — UDP must not concatenate", got)
	}
}

func TestBuildGELFValidJSON(t *testing.T) {
	payload, err := buildGELF(WindowsEvent{})
	if err != nil {
		t.Fatalf("buildGELF returned error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("zero-value WindowsEvent produced invalid JSON: %v", err)
	}
	if v, ok := m["version"]; !ok || v != "1.1" {
		t.Errorf("version: expected \"1.1\", got %v", v)
	}
}
