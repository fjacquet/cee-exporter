// Package mapper translates CEPAEvent values into WindowsEvent values using
// the semantic mapping table confirmed by the architecture analysis.
//
// Mapping table (source: docs/PowerStore_ CEPA_CEE vers EVTX.txt):
//
//	CEPP_CREATE_FILE / CEPP_CREATE_DIRECTORY → EventID 4663 (WriteData access)
//	CEPP_FILE_READ                           → EventID 4663 (ReadData access)
//	CEPP_FILE_WRITE                          → EventID 4663 (WriteData access)
//	CEPP_DELETE_FILE                         → EventID 4660 (object deleted)
//	CEPP_SETACL_FILE                         → EventID 4670 (permissions changed)
//	CEPP_RENAME_FILE                         → EventID 4663 (WriteData access)
//	CEPP_CLOSE_MODIFIED                      → EventID 4663 (composite — WriteData)
//	(unknown)                                → EventID 4663 (default)
package mapper

import (
	"fmt"
	"os"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/parser"
)

// cepaToEventID maps CEPA event type strings to Windows Event IDs.
var cepaToEventID = map[string]int{
	"CEPP_CREATE_FILE":        4663,
	"CEPP_CREATE_DIRECTORY":   4663,
	"CEPP_FILE_READ":          4663,
	"CEPP_FILE_READ_DIR":      4663,
	"CEPP_FILE_WRITE":         4663,
	"CEPP_CLOSE_MODIFIED":     4663,
	"CEPP_RENAME_FILE":        4663,
	"CEPP_RENAME_DIRECTORY":   4663,
	"CEPP_DELETE_FILE":        4660,
	"CEPP_DELETE_DIRECTORY":   4660,
	"CEPP_SETACL_FILE":        4670,
	"CEPP_SETACL_DIRECTORY":   4670,
}

// accessMaskFor returns the Windows access mask hex string for an event type.
var accessMaskFor = map[string]string{
	"CEPP_FILE_READ":        "0x1",   // ReadData (or ListDirectory)
	"CEPP_FILE_READ_DIR":    "0x1",
	"CEPP_CREATE_FILE":      "0x2",   // WriteData (or AddFile)
	"CEPP_CREATE_DIRECTORY": "0x4",   // AppendData (or AddSubdirectory)
	"CEPP_FILE_WRITE":       "0x2",   // WriteData (or AddFile)
	"CEPP_CLOSE_MODIFIED":   "0x2",
	"CEPP_RENAME_FILE":      "0x2",
	"CEPP_RENAME_DIRECTORY": "0x2",
	"CEPP_DELETE_FILE":      "0x10000", // DELETE
	"CEPP_DELETE_DIRECTORY": "0x10000",
	"CEPP_SETACL_FILE":      "0x40000", // WRITE_DAC
	"CEPP_SETACL_DIRECTORY": "0x40000",
}

// accessDescFor returns a human-readable access description.
var accessDescFor = map[string]string{
	"CEPP_FILE_READ":        "ReadData (or ListDirectory)",
	"CEPP_FILE_READ_DIR":    "ReadData (or ListDirectory)",
	"CEPP_CREATE_FILE":      "WriteData (or AddFile)",
	"CEPP_CREATE_DIRECTORY": "AppendData (or AddSubdirectory)",
	"CEPP_FILE_WRITE":       "WriteData (or AddFile)",
	"CEPP_CLOSE_MODIFIED":   "WriteData (or AddFile)",
	"CEPP_RENAME_FILE":      "WriteData (or AddFile)",
	"CEPP_RENAME_DIRECTORY": "WriteData (or AddFile)",
	"CEPP_DELETE_FILE":      "DELETE",
	"CEPP_DELETE_DIRECTORY": "DELETE",
	"CEPP_SETACL_FILE":      "WRITE_DAC",
	"CEPP_SETACL_DIRECTORY": "WRITE_DAC",
}

// providerName is embedded in every generated event.
const providerName = "PowerStore-CEPA"

// Map converts a CEPAEvent to a WindowsEvent.
// The hostname parameter is the computer name embedded in the event (use the
// NAS hostname from the CEPA payload when available, fall back to os.Hostname).
func Map(e parser.CEPAEvent, hostname string) evtx.WindowsEvent {
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	eventID, ok := cepaToEventID[e.EventType]
	if !ok {
		eventID = 4663 // safe default
	}

	mask := accessMaskFor[e.EventType]
	if mask == "" {
		mask = "0x0"
	}
	desc := accessDescFor[e.EventType]
	if desc == "" {
		desc = e.EventType
	}

	return evtx.WindowsEvent{
		EventID:      eventID,
		ProviderName: providerName,
		Computer:     hostname,
		Channel:      "Security",
		TimeCreated:  e.Timestamp,

		SubjectUserSID:  e.UserSID,
		SubjectUsername: e.Username,
		SubjectDomain:   e.Domain,
		SubjectLogonID:  e.LogonID,

		ObjectType: objectType(e.FilePath),
		ObjectName: e.FilePath,

		AccessMask: mask,
		Accesses:   desc,

		ProcessID: 4, // System process (placeholder — CEPA does not expose PID)
		HandleID:  fmt.Sprintf("0x%x", e.Timestamp.UnixNano()&0xfff),

		ClientAddr: e.ClientAddr,

		BytesRead:      e.BytesRead,
		BytesWritten:   e.BytesWritten,
		NumberOfReads:  e.NumberOfReads,
		NumberOfWrites: e.NumberOfWrites,

		CEPAEventType: e.EventType,
	}
}

// objectType guesses "File" or "Directory" from the path.
func objectType(path string) string {
	if len(path) > 0 && path[len(path)-1] == '/' {
		return "Directory"
	}
	return "File"
}
