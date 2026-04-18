package evtx

import (
	"context"
	"strings"
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

func TestBeatsWriterDialerInjection(t *testing.T) {
	unreachable := "127.0.0.1:1"

	tests := []struct {
		name string
		cfg  BeatsConfig
	}{
		{
			name: "plain_tcp_returns_error_on_unreachable",
			cfg: BeatsConfig{
				Host: "127.0.0.1",
				Port: 1,
				TLS:  false,
			},
		},
		{
			name: "tls_returns_error_on_unreachable",
			cfg: BeatsConfig{
				Host: "127.0.0.1",
				Port: 1,
				TLS:  true,
			},
		},
	}

	_ = unreachable // used via cfg Host/Port fields

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &BeatsWriter{cfg: tc.cfg}
			err := w.dial(context.Background())
			if err == nil {
				t.Errorf("dial: expected error on unreachable address, got nil")
			}
			// Verify no panic occurred (test completion is sufficient)
		})
	}
}
