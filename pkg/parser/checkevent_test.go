package parser

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// checkEventSingle is a <CheckEventRequest> built from the attribute list in
// CCheckEventRequest::GetXmlRequest(), recovered from libCEPPAPIWrapper.so in
// the vendored CEE 9.2.0.0 rpm.
//
// It is NOT a wire capture — no array has ever published to this consumer,
// which is the whole reason this dialect went unimplemented. It pins the
// attribute names and the decoding, not the values, and the fixture should be
// replaced by a real payload the moment one is captured.
const checkEventSingle = `<CheckEventRequest>` +
	`<EventList count="1">` +
	`<Event event="0x00000008" path="\\NAS01\fs01\test\evtest.txt" flag="0x0" ` +
	`server="NAS01" share="fs01" clientIP="10.26.1.222" serverIP="10.26.1.224" ` +
	`timeStamp="1786735002" userSid="S-1-5-21-1-2-3-1001" ownerSid="S-1-5-21-1-2-3-513" ` +
	`fileSize="0x400" newName="" desiredAccess="0x100106" createDispo="0x3" ` +
	`ntStatus="0x0" relativePath="\test\evtest.txt"/>` +
	`</EventList></CheckEventRequest>`

// utf16leBytes wraps the production encoder rather than hand-rolling a second
// one. Testing a decoder with an independent encoder guards against a shared
// misunderstanding, but utf16_test.go already pins EncodeUTF16LE against bytes
// captured from CEE, so that guard exists elsewhere.
func utf16leBytes(s string) []byte { return EncodeUTF16LE([]byte(s)) }

// TestIsCheckEventRequest_DiscriminatesDialects is the guard that matters most
// here: three dialects arrive on the same URL and each needs a different reply.
// Matching the wrong one is not a parse failure, it is a wrong answer sent
// confidently — and for OneFS, acknowledging an event as a heartbeat consumes
// it from the cluster's cursor permanently.
func TestIsCheckEventRequest_DiscriminatesDialects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"CEE event", checkEventSingle, true},
		{"CEE event with declaration", `<?xml version="1.0"?>` + checkEventSingle, true},
		{"register handshake", `<RegisterRequest />`, false},
		{"OneFS heartbeat", `<CheckFileRequest><Args action="9"/></CheckFileRequest>`, false},
		{"VCAPS batch", `<EventBatch><CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent></EventBatch>`, false},
		// A path that merely contains the word must not be taken for the
		// element — the same trap IsRegisterRequest was written to avoid.
		{"event named after the element", `<EventBatch><CEEEvent><FilePath>\CheckEventRequest\a.txt</FilePath></CEEEvent></EventBatch>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCheckEventRequest([]byte(tc.body)); got != tc.want {
				t.Errorf("IsCheckEventRequest = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseCheckEventRequest_Attributes(t *testing.T) {
	fallback := time.Unix(1, 0).UTC()
	events, err := ParseCheckEventRequest([]byte(checkEventSingle), fallback)
	if err != nil {
		t.Fatalf("ParseCheckEventRequest: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]

	if want := `\\NAS01\fs01\test\evtest.txt`; e.FilePath != want {
		t.Errorf("FilePath = %q, want %q", e.FilePath, want)
	}
	if want := "S-1-5-21-1-2-3-1001"; e.UserSID != want {
		t.Errorf("UserSID = %q, want %q", e.UserSID, want)
	}
	if want := "10.26.1.222"; e.ClientAddr != want {
		t.Errorf("ClientAddr = %q, want %q", e.ClientAddr, want)
	}
	if want := time.Unix(1786735002, 0).UTC(); !e.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, want)
	}
	// fileSize is the object's size, not bytes transferred. Mapping it onto
	// BytesWritten would assert I/O that never happened.
	if e.BytesWritten != 0 {
		t.Errorf("BytesWritten = %d, want 0: fileSize is not a transfer count", e.BytesWritten)
	}
}

// TestParseCheckEventRequest_UTF16 covers the encoding CEE actually uses. The
// UTF-8 fixtures above would all pass against a parser that never decoded
// anything, so this is the case that proves the transcode runs.
func TestParseCheckEventRequest_UTF16(t *testing.T) {
	events, err := ParseCheckEventRequest(utf16leBytes(checkEventSingle), time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("ParseCheckEventRequest on UTF-16LE: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if !strings.HasSuffix(events[0].FilePath, `evtest.txt`) {
		t.Errorf("FilePath = %q, want it to end in evtest.txt", events[0].FilePath)
	}
}

func TestParseCheckEventRequest_Batch(t *testing.T) {
	body := `<CheckEventRequest><EventList count="2">` +
		`<Event event="0x8" path="\\NAS01\fs01\a.txt" timeStamp="1786735002"/>` +
		`<Event event="0x20" path="\\NAS01\fs01\b.txt" timeStamp="1786735003"/>` +
		`</EventList></CheckEventRequest>`
	events, err := ParseCheckEventRequest([]byte(body), time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("ParseCheckEventRequest: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 — a batch must not collapse to its first entry", len(events))
	}
	if events[1].FilePath != `\\NAS01\fs01\b.txt` {
		t.Errorf("second FilePath = %q", events[1].FilePath)
	}
}

// TestParseCheckEventRequest_PrefersEncodedPath: CEE supplies encodedPath when
// the plain attribute cannot carry the name, which makes non-ASCII filenames
// exactly the case where the plain one is lossy.
func TestParseCheckEventRequest_PrefersEncodedPath(t *testing.T) {
	real := `\\NAS01\fs01\rapport-été.txt`
	enc := base64.StdEncoding.EncodeToString(utf16leBytes(real))
	body := `<CheckEventRequest><EventList count="1">` +
		`<Event event="0x8" path="\\NAS01\fs01\rapport-?t?.txt" ` +
		`encodingType="base64" encodedPath="` + enc + `" timeStamp="1786735002"/>` +
		`</EventList></CheckEventRequest>`

	events, err := ParseCheckEventRequest([]byte(body), time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("ParseCheckEventRequest: %v", err)
	}
	if events[0].FilePath != real {
		t.Errorf("FilePath = %q, want the decoded %q", events[0].FilePath, real)
	}
}

// TestParseCheckEventRequest_UndecodableEncodedPathFallsBack: a lossy path
// beats no path. The record is still worth writing and the handler logs the
// payload, so nothing is hidden.
func TestParseCheckEventRequest_UndecodableEncodedPathFallsBack(t *testing.T) {
	body := `<CheckEventRequest><EventList count="1">` +
		`<Event event="0x8" path="\\NAS01\fs01\a.txt" ` +
		`encodingType="rot13" encodedPath="!!!not-base64!!!" timeStamp="1786735002"/>` +
		`</EventList></CheckEventRequest>`
	events, err := ParseCheckEventRequest([]byte(body), time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("ParseCheckEventRequest: %v", err)
	}
	if events[0].FilePath != `\\NAS01\fs01\a.txt` {
		t.Errorf("FilePath = %q, want the plain attribute as fallback", events[0].FilePath)
	}
}

// TestCEEEventTypeMapping pins the bitmask table. Bit 3 (0x8) is the one
// confirmed against a live PowerStore; the rest come from Dell's documented
// event ordering and are provisional — see the table's comment.
func TestCEEEventTypeMapping(t *testing.T) {
	cases := []struct {
		attr string
		want string
	}{
		{"0x00000008", "CEPP_CREATE_FILE"}, // measured on the wire
		{"0x8", "CEPP_CREATE_FILE"},        // same bit, short form
		{"0x20", "CEPP_DELETE_FILE"},       // bit 5
		{"0x200", "CEPP_RENAME_FILE"},      // bit 9
		{"0x80", "CEPP_CLOSE_MODIFIED"},    // bit 7
		{"0x100", "CEPP_CLOSE_UNMODIFIED"}, // bit 8
		{"0x800", "CEPP_SETACL_FILE"},      // bit 11
		{"0x10000", "CEPP_FILE_WRITE"},     // bit 16
		{"200", "CEPP_RENAME_FILE"},        // no 0x prefix: still hex
	}
	for _, tc := range cases {
		t.Run(tc.attr, func(t *testing.T) {
			if got := ceeEventTypeName(tc.attr); got != tc.want {
				t.Errorf("ceeEventTypeName(%q) = %q, want %q", tc.attr, got, tc.want)
			}
		})
	}
}

// TestCEEEventTypeUnknownKeepsItsCode: a bit outside the documented 21 must
// still reach the writers with its raw value preserved and the gap visible.
// Losing an audit record is worse than labelling one imprecisely.
func TestCEEEventTypeUnknownKeepsItsCode(t *testing.T) {
	cases := []struct {
		attr string
		want string
	}{
		{"0x200000", "CEPP_CEE_UNMAPPED_2097152"}, // bit 21, beyond the table
		{"", "CEPP_CEE_UNMAPPED_INVALID"},
		{"zzz", "CEPP_CEE_UNMAPPED_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.attr, func(t *testing.T) {
			got := ceeEventTypeName(tc.attr)
			if got != tc.want {
				t.Errorf("ceeEventTypeName(%q) = %q, want %q", tc.attr, got, tc.want)
			}
			if !IsUnmappedCEEEventType(got) {
				t.Errorf("IsUnmappedCEEEventType(%q) = false; the gap must stay visible", got)
			}
		})
	}
}

// TestCEEEventTableIsOneBitPerEntry guards the bitmask assumption itself: every
// key must be a single bit, and no two events may share one. A duplicate or a
// multi-bit key would silently mis-label events.
func TestCEEEventTableIsOneBitPerEntry(t *testing.T) {
	for code := range ceeEventType {
		if code == 0 || code&(code-1) != 0 {
			t.Errorf("code 0x%x is not a single bit", code)
		}
		if code > 1<<20 {
			t.Errorf("code 0x%x is beyond the 21 documented events", code)
		}
	}
	if len(ceeEventType) != 21 {
		t.Errorf("table has %d entries, want 21 (the documented event set)", len(ceeEventType))
	}
}

// TestParseCheckEventRequest_EmptyListIsNamed: count="0" is legitimate (CEE
// sends EVENT_ADMIN_RESYNC notices with no event body), so the error has to
// name it rather than read as a parse failure.
func TestParseCheckEventRequest_EmptyListIsNamed(t *testing.T) {
	body := `<CheckEventRequest><EventList count="0"></EventList></CheckEventRequest>`
	_, err := ParseCheckEventRequest([]byte(body), time.Unix(1, 0).UTC())
	if err == nil {
		t.Fatal("want an error for an empty EventList")
	}
	if !strings.Contains(err.Error(), "count=") {
		t.Errorf("error %q does not report the count; the caller cannot tell an empty list from a malformed one", err)
	}
}

// TestCEETimestampFallback: an unusable timestamp must fall back to receive
// time, not to the zero time, which would sort every such record to 1970 and
// quietly corrupt the ordering of an audit trail.
func TestCEETimestampFallback(t *testing.T) {
	fallback := time.Unix(1786735999, 0).UTC()
	// The last two exceed MaxInt64. Without an upper bound the int64
	// conversion wraps them negative and the record sorts *before* 1970 —
	// worse than the zero time this fallback exists to avoid, and reported by
	// CodeQL rather than by any test here.
	for _, raw := range []string{
		"", "0", "not-a-number",
		"9223372036854775808",  // MaxInt64 + 1
		"18446744073709551615", // MaxUint64
	} {
		if got := ceeTimestamp(raw, fallback); !got.Equal(fallback) {
			t.Errorf("ceeTimestamp(%q) = %v, want the fallback %v", raw, got, fallback)
		}
	}
	if got := ceeTimestamp("0x6A7B0C1A", fallback); got.Equal(fallback) {
		t.Error("ceeTimestamp did not parse the 0x form")
	}
	// MaxInt64 itself must still parse: the bound is inclusive, and a check
	// written as >= would pass every other case in this test.
	if got := ceeTimestamp("9223372036854775807", fallback); got.Equal(fallback) {
		t.Error("ceeTimestamp rejected MaxInt64, which converts exactly")
	}
}

// TestEventTablesCorroborate pins the agreement between the two independently
// derived event tables.
//
// onefsEventType was measured on a live OneFS cluster, one operation per
// capture window. ceeEventType comes from Dell's documented event ordering with
// a single bit confirmed on a PowerStore. They are different array families and
// different documents, so their agreeing on six codes is real evidence for the
// ordering — and it is evidence that would be silently destroyed if someone
// edited either table without re-checking the other.
//
// If this fails, do not "fix" it by editing one table to match. Work out which
// derivation the new information actually supports.
func TestEventTablesCorroborate(t *testing.T) {
	checked := 0
	for code, onefsName := range onefsEventType {
		ceeName, ok := ceeEventType[uint64(code)]
		if !ok {
			t.Errorf("OneFS measured code %d (%s) has no entry in ceeEventType", code, onefsName)
			continue
		}
		if ceeName != onefsName {
			t.Errorf("code %d: onefsEventType says %q, ceeEventType says %q", code, onefsName, ceeName)
		}
		checked++
	}
	if checked != len(onefsEventType) {
		t.Errorf("corroborated %d of %d OneFS codes", checked, len(onefsEventType))
	}
}
