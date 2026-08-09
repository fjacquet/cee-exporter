package evtx

import (
	"encoding/json"
	"strings"
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
