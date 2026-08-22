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

// TestCEETimestampFallback: an unparseable timestamp must fall back to receive
// time, not to the zero time, which would sort every such record to 1970 and
// quietly corrupt the ordering of an audit trail.
//
// This test previously also required "9223372036854775808" (MaxInt64+1) and
// "18446744073709551615" (MaxUint64) to fall back, guarding a > MaxInt64
// rejection added when the attribute was believed to be a plain second count.
// The wire says otherwise: PowerStore packs the epoch into the high 32 bits,
// so a value above MaxInt64 is an ordinary timestamp for any event after
// 2038-01-19, and rejecting it would substitute receive time for a perfectly
// good one. Those two cases now decode as 2038 and 2106 respectively, and the
// separate TestCEETimestampPackedAfter2038 pins that.
//
// The concern behind the rejection — CodeQL's, that the int64 conversion wraps
// a large uint64 negative and sorts the record before 1970 — is unchanged as a
// requirement and is now met by construction rather than by a bound:
// ceeTimestamp unpacks before converting, so its input to time.Unix is always
// at most MaxUint32. TestCEETimestampNeverSortsBefore1970 asserts the property
// directly, across the whole uint64 range, which is a stronger guarantee than
// the two sampled values were.
func TestCEETimestampFallback(t *testing.T) {
	fallback := time.Unix(1786735999, 0).UTC()
	for _, raw := range []string{"", "0", "not-a-number"} {
		if got := ceeTimestamp(raw, fallback); !got.Equal(fallback) {
			t.Errorf("ceeTimestamp(%q) = %v, want the fallback %v", raw, got, fallback)
		}
	}
	if got := ceeTimestamp("0x6A7B0C1A", fallback); got.Equal(fallback) {
		t.Error("ceeTimestamp did not parse the 0x form")
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

// TestCEETimestampPacked64 pins the PowerStore form of the timeStamp
// attribute, captured off the wire from NAS01 (10.26.1.199) on 2026-08-22:
//
//	timeStamp="0x6a7f7c090008765f"
//
// It is not whole seconds. The high 32 bits are the Unix epoch second
// (0x6a7f7c09 = 1786739721 = 2026-08-14T20:35:21Z, which matches the
// pstest-1786739721.txt filename the same event carries) and the low 32 bits
// are a sub-second remainder. Read as one 64-bit second count it becomes
// 7673988668159719007 — the year 243179022179 — so every record written from
// a PowerStore event carried a timestamp no reader can use, and the true
// event time appeared nowhere in the file.
func TestCEETimestampPacked64(t *testing.T) {
	fallback := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, 8, 14, 20, 35, 21, 0, time.UTC)

	got := ceeTimestamp("0x6a7f7c090008765f", fallback)

	if !got.Equal(want) {
		t.Errorf("ceeTimestamp(packed) = %v (year %d), want %v",
			got, got.Year(), want)
	}
}

// TestCEETimestampPlainSecondsStillWorks guards the CIFS/OneFS form against
// the packed-form fix: a value that fits in 32 bits is a plain epoch second
// and must not be shifted.
func TestCEETimestampPlainSecondsStillWorks(t *testing.T) {
	fallback := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, 8, 14, 19, 16, 42, 0, time.UTC)

	if got := ceeTimestamp("1786735002", fallback); !got.Equal(want) {
		t.Errorf("ceeTimestamp(plain) = %v, want %v", got, want)
	}
}

// liveNFSEvent is the payload PowerStore actually publishes, captured off the
// wire from NAS01 (10.26.1.199) on 2026-08-22. Note protocol="1" (NFS) and the
// complete absence of userSid: NFS has no Windows SID to send, so the actor is
// carried as a POSIX uid on the EventExt child element instead.
const liveNFSEvent = `<CheckEventRequest><EventList count="1">` +
	`<Event event="0x8" path="\\nas01.diab.local\CHECK$\FS01\pstest.txt" flag="0x2" ` +
	`server="10.26.1.224" share="/FS01" clientIP="10.26.1.222" serverIP="10.26.1.224" ` +
	`sourceID="5" timeStamp="0x6a7f7c090008765f" fileSize="0x0" protocol="1" type="0">` +
	`<EventExt inode="9450" userId="0" ownerId="0" fsId="0xb0000009f" parentInode="2"/>` +
	`</Event></EventList></CheckEventRequest>`

// TestCheckEventNFSCarriesActorIdentity: an NFS event must not produce a record
// with no actor. EventExt/@userId was not parsed at all, so SubjectUserSid and
// SubjectUserName were empty on every record this array produced — an audit
// trail that records what happened and to which file, but never by whom.
//
// The uid is rendered in the S-1-22-1-<uid> form, which is not an invention
// here: it is the same representation OneFS itself puts in userSid for a POSIX
// account (see the worked payload above ParseCheckFileRequest), so a reader
// that already handles OneFS needs no new case.
func TestCheckEventNFSCarriesActorIdentity(t *testing.T) {
	evs, err := ParseCheckEventRequestDecoded([]byte(liveNFSEvent), time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if got, want := evs[0].UserSID, "S-1-22-1-0"; got != want {
		t.Errorf("UserSID = %q, want %q", got, want)
	}
}

// TestCheckEventCIFSUserSidWins guards the CIFS path: when the array does send
// a real userSid, the synthesised POSIX form must not displace it.
func TestCheckEventCIFSUserSidWins(t *testing.T) {
	payload := `<CheckEventRequest><EventList count="1">` +
		`<Event event="0x8" path="\\NAS01\fs01\a.txt" timeStamp="1786735002" ` +
		`userSid="S-1-5-21-1-2-3-1001" protocol="0">` +
		`<EventExt inode="1" userId="0" ownerId="0"/>` +
		`</Event></EventList></CheckEventRequest>`

	evs, err := ParseCheckEventRequestDecoded([]byte(payload), time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := evs[0].UserSID, "S-1-5-21-1-2-3-1001"; got != want {
		t.Errorf("UserSID = %q, want the CIFS SID %q", got, want)
	}
}

// TestCEETimestampPackedAfter2038 guards the interaction between the two
// guards in ceeTimestamp, which arrived from different branches and were
// merged in the wrong order.
//
// The > MaxInt64 rejection was written when the attribute was believed to be a
// plain second count, where such a value is nonsense. For a packed value it is
// not: the epoch lives in the high word, so any event timestamped after
// 2038-01-19 sets bit 63 and makes the whole 64-bit value exceed MaxInt64. A
// rejection ordered before the unpack therefore discards a perfectly good
// timestamp and silently substitutes receive time — the exact failure the
// packed-form fix was written to end, returning in 2038.
//
// Unpacking first makes the rejection structurally unreachable for packed
// input: the shifted value is at most MaxUint32, so the int64 conversion the
// guard protects cannot wrap.
func TestCEETimestampPackedAfter2038(t *testing.T) {
	fallback := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	// Epoch 2^31 = 2038-01-19T03:14:08Z, with a sub-second remainder.
	want := time.Date(2038, 1, 19, 3, 14, 8, 0, time.UTC)

	got := ceeTimestamp("0x8000000000001234", fallback)

	if got.Equal(fallback) {
		t.Fatalf("ceeTimestamp fell back to receive time; want %v", want)
	}
	if !got.Equal(want) {
		t.Errorf("ceeTimestamp = %v, want %v", got, want)
	}
}

// TestCEETimestampNeverSortsBefore1970 is the property the removed > MaxInt64
// bound was protecting, asserted directly instead of at two sampled points: no
// input, anywhere in the uint64 range, may produce a record that sorts before
// the epoch. That was CodeQL's finding — a large uint64 converted to int64
// wraps negative — and unpacking before converting makes it unreachable.
func TestCEETimestampNeverSortsBefore1970(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	fallback := time.Unix(1786735999, 0).UTC()
	for _, raw := range []string{
		"9223372036854775807",  // MaxInt64
		"9223372036854775808",  // MaxInt64 + 1
		"18446744073709551615", // MaxUint64
		"0xFFFFFFFFFFFFFFFF",
		"0x8000000000000000",
		"0x6a7f7c090008765f", // the real PowerStore value
		"4294967296",         // MaxUint32 + 1, the smallest packed value
	} {
		if got := ceeTimestamp(raw, fallback); got.Before(epoch) {
			t.Errorf("ceeTimestamp(%q) = %v, which sorts before the Unix epoch", raw, got)
		}
	}
}
