package parser

import (
	"testing"
	"time"
)

// checkFileCloseOneFS is the ordinary-close event a single `touch` produced,
// captured on the wire from node powerscale1-1 on 2026-08-14. It is the same
// element and action as checkFileEventOneFS but a different eventType, which
// is the whole reason the numeric attribute has to be read rather than the
// action alone.
const checkFileCloseOneFS = `<CheckFileRequest><Args action="11" sourceIP="10.26.1.150" sourceID="2" name="XABcAHAAbwB3AGUAcgBzAGMAYQBsAGUAMQAtADEAXABvAG4AZQBmAHMAJABcAGkAZgBzAFwAdABlAHMAdABcAGUAdgB0AGUAcwB0AC0AMQA3ADgANgA3ADMANQAwADAAMgAuAHQAeAB0AA==" protocol="1"><Cluster id="00505692f33595217c6ab005f128c9b4c9f9" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/><Zone id="1" name="UwB5AHMAdABlAG0A"/></Args><NFSEventArgs eventType="256" numberOfWrites="0" numberOfReads="0" bytesRead="0" bytesWritten="0" userSid="S-1-22-1-0" clientIP="10.26.1.222" serverName="cABvAHcAZQByAHMAYwBhAGwAZQAxAC0AMQA=" ntStatus="0x0" userId="0" timeStamp="1786734886" timeStampMicroSeconds="18481" inode="4295174245" fsId="1"/></CheckFileRequest>`

// TestParseOneFSEvent_CreateFromRealCapture pins every field against a payload
// taken off the wire, not one written to match the parser.
func TestParseOneFSEvent_CreateFromRealCapture(t *testing.T) {
	fallback := time.Unix(0, 0).UTC()

	events, err := ParseOneFSEvent([]byte(checkFileEventOneFS), fallback)
	if err != nil {
		t.Fatalf("ParseOneFSEvent returned an error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]

	if want := "CEPP_CREATE_FILE"; e.EventType != want {
		t.Errorf("EventType = %q, want %q", e.EventType, want)
	}
	// The path is base64 of UTF-16LE and arrives UNC-shaped. Getting this
	// wrong yields an event with no object, which is an audit record about
	// nothing.
	if want := `\\powerscale1.diab.local\onefs$\ifs\test\evtest-1786563822.txt`; e.FilePath != want {
		t.Errorf("FilePath = %q, want %q", e.FilePath, want)
	}
	if want := "S-1-22-1-1000"; e.UserSID != want {
		t.Errorf("UserSID = %q, want %q", e.UserSID, want)
	}
	// clientIP is the NFS client that acted, not sourceIP (the cluster node
	// that forwarded). Confusing the two attributes them makes every event look
	// like it came from the array.
	if want := "10.26.1.222"; e.ClientAddr != want {
		t.Errorf("ClientAddr = %q, want %q", e.ClientAddr, want)
	}
	if want := time.Unix(1786563708, 859323*int64(time.Microsecond)).UTC(); !e.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %s, want %s", e.Timestamp, want)
	}
}

// TestParseOneFSEvent_CloseIsNotACreate guards the eventType lookup. Both
// payloads carry action="11"; only the numeric eventType separates a create
// from a close, and collapsing them would file every close as a write access.
func TestParseOneFSEvent_CloseIsNotACreate(t *testing.T) {
	events, err := ParseOneFSEvent([]byte(checkFileCloseOneFS), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ParseOneFSEvent returned an error: %v", err)
	}
	e := events[0]

	if want := "CEPP_CLOSE_UNMODIFIED"; e.EventType != want {
		t.Errorf("EventType = %q, want %q", e.EventType, want)
	}
	if want := `\\powerscale1-1\onefs$\ifs\test\evtest-1786735002.txt`; e.FilePath != want {
		t.Errorf("FilePath = %q, want %q", e.FilePath, want)
	}
	if IsUnmappedOneFSEventType(e.EventType) {
		t.Error("eventType 256 reported as unmapped; it is one of the three established values")
	}
}

// TestParseOneFSEvent_IsolationRunTypes pins the three resolved by the
// 2026-08-14 isolation run — one operation per 10-second window, so each
// eventType landed alone and could be attributed. Regressing any of these
// would silently refile deletes as renames in an audit trail.
func TestParseOneFSEvent_IsolationRunTypes(t *testing.T) {
	for raw, want := range map[string]string{
		"32":   "CEPP_DELETE_FILE",
		"512":  "CEPP_RENAME_FILE",
		"2048": "CEPP_SETACL_FILE",
	} {
		body := []byte(`<CheckFileRequest><Args action="11" name="XABcAGEAXABiAA=="/><NFSEventArgs eventType="` + raw + `" clientIP="10.26.1.222" timeStamp="1786734886"/></CheckFileRequest>`)

		events, err := ParseOneFSEvent(body, time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("eventType %s: ParseOneFSEvent returned an error: %v", raw, err)
		}
		if got := events[0].EventType; got != want {
			t.Errorf("eventType %s: EventType = %q, want %q", raw, got, want)
		}
		if IsUnmappedOneFSEventType(events[0].EventType) {
			t.Errorf("eventType %s: reported as unmapped, but it was measured", raw)
		}
	}
}

// TestParseOneFSEvent_UnknownTypeIsLabelledNotGuessed covers the values that
// remain unobserved. They must reach the writers — the CheckFileResponse has
// already advanced the cluster's cursor, so a dropped event is gone — but they
// must not claim a meaning nobody has measured.
func TestParseOneFSEvent_UnknownTypeIsLabelledNotGuessed(t *testing.T) {
	for _, raw := range []string{"1", "4", "1024", "4096"} {
		body := []byte(`<CheckFileRequest><Args action="11" sourceIP="10.26.1.150" name="XABcAGEAXABiAA=="/><NFSEventArgs eventType="` + raw + `" clientIP="10.26.1.222" timeStamp="1786734886"/></CheckFileRequest>`)

		events, err := ParseOneFSEvent(body, time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("eventType %s: ParseOneFSEvent returned an error: %v", raw, err)
		}
		e := events[0]

		if want := "CEPP_ONEFS_UNMAPPED_" + raw; e.EventType != want {
			t.Errorf("eventType %s: EventType = %q, want %q", raw, e.EventType, want)
		}
		if !IsUnmappedOneFSEventType(e.EventType) {
			t.Errorf("eventType %s: not reported as unmapped, so the gap would never be logged", raw)
		}
	}
}

// TestParseOneFSEvent_MissingEventArgs: an event element this build has never
// seen — an SMB one, most likely — must fail by name rather than produce an
// empty record. The caller logs the payload, so the format stays recoverable.
func TestParseOneFSEvent_MissingEventArgs(t *testing.T) {
	body := []byte(`<CheckFileRequest><Args action="11" name="XABcAGEAXABiAA=="/><SMBEventArgs eventType="8"/></CheckFileRequest>`)

	if _, err := ParseOneFSEvent(body, time.Unix(0, 0).UTC()); err == nil {
		t.Fatal("an event with no NFSEventArgs parsed successfully; it should be reported, not silently emptied")
	}
}

// TestParseOneFSEvent_UndecodablePathRejected: the object is the point of the
// record, so a name that will not decode is a failure rather than an event
// with an empty path.
func TestParseOneFSEvent_UndecodablePathRejected(t *testing.T) {
	body := []byte(`<CheckFileRequest><Args action="11" name="!!!not-base64!!!"/><NFSEventArgs eventType="8" timeStamp="1786734886"/></CheckFileRequest>`)

	if _, err := ParseOneFSEvent(body, time.Unix(0, 0).UTC()); err == nil {
		t.Fatal("an undecodable path parsed successfully")
	}
}

// TestParseOneFSEvent_TimestampFallback: a payload with no usable timestamp
// takes the receive time rather than the Unix epoch, which would sort every
// such record to 1970 in the audit trail.
func TestParseOneFSEvent_TimestampFallback(t *testing.T) {
	fallback := time.Date(2026, 8, 14, 19, 14, 46, 0, time.UTC)
	body := []byte(`<CheckFileRequest><Args action="11" name="XABcAGEAXABiAA=="/><NFSEventArgs eventType="8" clientIP="10.26.1.222"/></CheckFileRequest>`)

	events, err := ParseOneFSEvent(body, fallback)
	if err != nil {
		t.Fatalf("ParseOneFSEvent returned an error: %v", err)
	}
	if !events[0].Timestamp.Equal(fallback) {
		t.Errorf("Timestamp = %s, want the receive time %s", events[0].Timestamp, fallback)
	}
}

// TestParseOneFSEvent_IOCountersCarried: bytesWritten/numberOfWrites are what
// separate eventType 128 from 256 on the wire, and CEPP_CLOSE_MODIFIED is the
// one event type whose I/O statistics are meaningful downstream.
func TestParseOneFSEvent_IOCountersCarried(t *testing.T) {
	body := []byte(`<CheckFileRequest><Args action="11" name="XABcAGEAXABiAA=="/><NFSEventArgs eventType="128" numberOfWrites="3" numberOfReads="1" bytesWritten="4096" bytesRead="128" timeStamp="1786734886"/></CheckFileRequest>`)

	events, err := ParseOneFSEvent(body, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ParseOneFSEvent returned an error: %v", err)
	}
	e := events[0]

	if want := "CEPP_CLOSE_MODIFIED"; e.EventType != want {
		t.Errorf("EventType = %q, want %q", e.EventType, want)
	}
	if e.BytesWritten != 4096 || e.NumberOfWrites != 3 || e.BytesRead != 128 || e.NumberOfReads != 1 {
		t.Errorf("I/O counters = read %d/%d write %d/%d, want read 128/1 write 4096/3",
			e.BytesRead, e.NumberOfReads, e.BytesWritten, e.NumberOfWrites)
	}
}
