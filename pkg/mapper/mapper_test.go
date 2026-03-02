package mapper

import (
	"testing"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/parser"
)

func TestMapEventID(t *testing.T) {
	tests := []struct {
		name        string
		cepaType    string
		wantEventID int
		wantMask    string
	}{
		{"create_file", "CEPP_CREATE_FILE", 4663, "0x2"},
		{"create_directory", "CEPP_CREATE_DIRECTORY", 4663, "0x4"},
		{"file_read", "CEPP_FILE_READ", 4663, "0x1"},
		{"file_write", "CEPP_FILE_WRITE", 4663, "0x2"},
		{"delete_file", "CEPP_DELETE_FILE", 4660, "0x10000"},
		{"delete_directory", "CEPP_DELETE_DIRECTORY", 4660, "0x10000"},
		{"setacl_file", "CEPP_SETACL_FILE", 4670, "0x40000"},
		{"setacl_directory", "CEPP_SETACL_DIRECTORY", 4670, "0x40000"},
		{"close_modified", "CEPP_CLOSE_MODIFIED", 4663, "0x2"},
		{"unknown_type", "CEPP_UNKNOWN_CUSTOM", 4663, "0x0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := parser.CEPAEvent{
				EventType: tt.cepaType,
				Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			}
			we := Map(e, "testhost")

			if we.EventID != tt.wantEventID {
				t.Errorf("Map(%q).EventID = %d, want %d", tt.cepaType, we.EventID, tt.wantEventID)
			}
			if we.AccessMask != tt.wantMask {
				t.Errorf("Map(%q).AccessMask = %q, want %q", tt.cepaType, we.AccessMask, tt.wantMask)
			}
		})
	}
}

func TestMapFieldPropagation(t *testing.T) {
	e := parser.CEPAEvent{
		EventType:    "CEPP_FILE_WRITE",
		FilePath:     "/nas/data/report.pdf",
		Username:     "bob",
		Domain:       "CORP",
		ClientAddr:   "192.168.1.10",
		BytesRead:    100,
		BytesWritten: 200,
		Timestamp:    time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
	}

	we := Map(e, "nas01.corp.local")

	if we.Computer != "nas01.corp.local" {
		t.Errorf("Computer = %q, want %q", we.Computer, "nas01.corp.local")
	}
	if we.ObjectName != "/nas/data/report.pdf" {
		t.Errorf("ObjectName = %q, want %q", we.ObjectName, "/nas/data/report.pdf")
	}
	if we.SubjectUsername != "bob" {
		t.Errorf("SubjectUsername = %q, want %q", we.SubjectUsername, "bob")
	}
	if we.SubjectDomain != "CORP" {
		t.Errorf("SubjectDomain = %q, want %q", we.SubjectDomain, "CORP")
	}
	if we.ClientAddr != "192.168.1.10" {
		t.Errorf("ClientAddr = %q, want %q", we.ClientAddr, "192.168.1.10")
	}
	if we.BytesRead != 100 {
		t.Errorf("BytesRead = %d, want 100", we.BytesRead)
	}
	if we.BytesWritten != 200 {
		t.Errorf("BytesWritten = %d, want 200", we.BytesWritten)
	}
	if we.ProviderName != "PowerStore-CEPA" {
		t.Errorf("ProviderName = %q, want %q", we.ProviderName, "PowerStore-CEPA")
	}
	if we.Channel != "Security" {
		t.Errorf("Channel = %q, want %q", we.Channel, "Security")
	}
	if we.CEPAEventType != "CEPP_FILE_WRITE" {
		t.Errorf("CEPAEventType = %q, want %q", we.CEPAEventType, "CEPP_FILE_WRITE")
	}
}

func TestMapHostnameFallback(t *testing.T) {
	e := parser.CEPAEvent{
		EventType: "CEPP_FILE_READ",
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	we := Map(e, "")

	if we.Computer == "" {
		t.Errorf("Map with empty hostname: Computer is empty, expected os.Hostname() fallback")
	}
}
