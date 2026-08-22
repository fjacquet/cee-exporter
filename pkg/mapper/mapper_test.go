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

// TestEventTypeTablesAreInSync guards the three parallel maps.
//
// cepaToEventID, accessMaskFor and accessDescFor are keyed by the same event
// type strings, and Map looks each up independently with a fallback. So a type
// added to one map but not the others degrades *silently*: the record is still
// written, with a plausible-looking EventID and an access mask of 0x0. Nothing
// fails, and the wrong value lands in an audit trail.
//
// The table grew by 54% in one commit and will grow again when the remaining
// CEE event codes are measured, which is exactly when a partial addition is
// most likely. This is cheaper than folding the three maps into one struct and
// buys the same guarantee.
func TestEventTypeTablesAreInSync(t *testing.T) {
	for k := range cepaToEventID {
		if _, ok := accessMaskFor[k]; !ok {
			t.Errorf("%q has an EventID but no access mask; Map would emit 0x0", k)
		}
		if _, ok := accessDescFor[k]; !ok {
			t.Errorf("%q has an EventID but no access description", k)
		}
	}
	for k := range accessMaskFor {
		if _, ok := cepaToEventID[k]; !ok {
			t.Errorf("%q has an access mask but no EventID; Map would emit the 4663 default", k)
		}
	}
	for k := range accessDescFor {
		if _, ok := cepaToEventID[k]; !ok {
			t.Errorf("%q has an access description but no EventID", k)
		}
	}
}

// TestMapCEEEventTypes covers the seven event types added for Dell CEE's own
// dialect, which TestMapEventID predates.
func TestMapCEEEventTypes(t *testing.T) {
	// wantDesc is asserted exactly, not merely checked for "not the raw event
	// type". The loose check passed CEPP_OPEN_FILE_NOACCESS while it was
	// described as "ReadAttributes" against a 0x0 mask -- a right the event
	// never carried.
	cases := []struct {
		eventType string
		wantID    int
		wantMask  string
		wantDesc  string
	}{
		{"CEPP_OPEN_FILE_NOACCESS", 4663, "0x0", "(no access requested)"},
		{"CEPP_OPEN_FILE_READ", 4663, "0x1", "ReadData (or ListDirectory)"},
		{"CEPP_OPEN_FILE_WRITE", 4663, "0x2", "WriteData (or AddFile)"},
		{"CEPP_OPEN_DIRECTORY", 4663, "0x1", "ReadData (or ListDirectory)"},
		{"CEPP_CLOSE_DIRECTORY", 4658, "0x0", "CloseHandle"},
		{"CEPP_SETSEC_FILE", 4670, "0x80000", "WRITE_OWNER"},
		{"CEPP_SETSEC_DIRECTORY", 4670, "0x80000", "WRITE_OWNER"},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			got := Map(parser.CEPAEvent{EventType: tc.eventType}, "host")
			if got.EventID != tc.wantID {
				t.Errorf("EventID = %d, want %d", got.EventID, tc.wantID)
			}
			if got.AccessMask != tc.wantMask {
				t.Errorf("AccessMask = %q, want %q", got.AccessMask, tc.wantMask)
			}
			if got.Accesses != tc.wantDesc {
				t.Errorf("Accesses = %q, want %q", got.Accesses, tc.wantDesc)
			}
		})
	}
}
