package evtx

import (
	"encoding/json"
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

func TestBuildGELFShortMessageTruncation(t *testing.T) {
	e := WindowsEvent{
		ObjectName:    "/share/file.txt",
		CEPAEventType: "CEPP_FILE_WRITE",
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
	if !ok || sm == "" {
		t.Errorf("short_message: expected non-empty string, got %v (%T)", m["short_message"], m["short_message"])
	}

	const prefix = "CEPP_FILE_WRITE on"
	if len(sm) < len(prefix) || sm[:len(prefix)] != prefix {
		t.Errorf("short_message: expected to start with %q, got %q", prefix, sm)
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
