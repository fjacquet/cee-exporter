package parser

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// unmappedCEEPrefix marks a CEE event code with no established meaning, the
// same discipline pkg/parser/onefs.go uses: keep the raw value after the
// prefix so the record still carries the truth, and let the caller log the gap
// rather than inventing a mapping.
const unmappedCEEPrefix = "CEPP_CEE_UNMAPPED_"

// ceeEventType translates the numeric CheckEventRequest Event/@event code into
// the CEPP_* vocabulary pkg/mapper keys on.
//
// The codes are a BITMASK, one bit per event in the order the storage platform
// lists them. That order and its extent are documented rather than guessed:
// Dell's Unity CLI prints the full post-event selection as
//
//	post-Events: OpenFileNoAccess, OpenFileRead, OpenFileWrite, CreateFile,
//	  CreateDir, DeleteFile, DeleteDir, CloseModified, CloseUnmodified,
//	  RenameFile, RenameDir, SetAclFile, SetAclDir, OpenDir, CloseDir,
//	  FileRead, FileWrite, SetSecFile, SetSecDir, OpenFileReadOffline,
//	  OpenFileWriteOffline (0x1fffff)
//
// (Dell KB 000194250). Twenty-one names, and 0x1fffff is exactly twenty-one
// bits — so name N occupies bit N. PowerStore's REST API returns the same
// twenty-one flags in the same order under file_events_settings, which is an
// independent statement of the ordering.
//
// **Confirmed by measurement: bit 3 (0x8) is CreateFile.** Verified 2026-08-22
// against PowerStore diabps01/NAS01 — a file created on an SMB share arrived as
// event="0x00000008" carrying that file's UNC path.
//
// **Corroborated independently for six bits.** onefsEventType in onefs.go was
// resolved by an isolation run against a live OneFS 9.13.0.0 cluster — a
// different array family, a different protocol path, a different numbering
// scheme on its face — and all six of its measured values land on the same
// names at the same numeric codes as this table: 8 create, 32 delete, 128
// close-modified, 256 close-unmodified, 512 rename, 2048 set-ACL.
// TestEventTablesCorroborate pins that agreement. Two independent derivations
// agreeing on six of twenty-one is much better evidence for the *ordering
// hypothesis* than one measured bit alone.
//
// **The remaining fifteen are documented, not measured.** They rest on the
// ordering above being the wire order. An isolation run — one operation per
// capture window, as was done for OneFS — would settle each of them, and is
// the right way to promote this table from documented to measured. Until then
// a wrong entry writes a wrong EventID into an audit trail, so treat anything
// outside the six corroborated codes and 0x8 as provisional.
var ceeEventType = map[uint64]string{
	0x000001: "CEPP_OPEN_FILE_NOACCESS",
	0x000002: "CEPP_OPEN_FILE_READ",
	0x000004: "CEPP_OPEN_FILE_WRITE",
	0x000008: "CEPP_CREATE_FILE", // measured
	0x000010: "CEPP_CREATE_DIRECTORY",
	0x000020: "CEPP_DELETE_FILE",
	0x000040: "CEPP_DELETE_DIRECTORY",
	0x000080: "CEPP_CLOSE_MODIFIED",
	0x000100: "CEPP_CLOSE_UNMODIFIED",
	0x000200: "CEPP_RENAME_FILE",
	0x000400: "CEPP_RENAME_DIRECTORY",
	0x000800: "CEPP_SETACL_FILE",
	0x001000: "CEPP_SETACL_DIRECTORY",
	0x002000: "CEPP_OPEN_DIRECTORY",
	0x004000: "CEPP_CLOSE_DIRECTORY",
	0x008000: "CEPP_FILE_READ",
	0x010000: "CEPP_FILE_WRITE",
	0x020000: "CEPP_SETSEC_FILE",
	0x040000: "CEPP_SETSEC_DIRECTORY",
	0x080000: "CEPP_OPEN_FILE_READ",  // offline variant, same access
	0x100000: "CEPP_OPEN_FILE_WRITE", // offline variant, same access
}

// IsCheckEventRequest reports whether the body is a Dell CEE event delivery.
//
// This is CEE's own dialect, and it is a third thing — neither the
// <RegisterRequest /> handshake CEE opens with, nor the <CheckFileRequest>
// OneFS uses for both its heartbeat and its events. Shape recovered from
// CCheckEventRequest::GetXmlRequest() in libCEPPAPIWrapper.so:
//
//	<CheckEventRequest><EventList count="1">
//	  <Event event="0x…" path="…" flag="0x…" server="…" share="…"
//	    clientIP="…" serverIP="…" timeStamp="…" userSid="…" ownerSid="…"
//	    fileSize="0x…" newName="…" desiredAccess="0x…" createDispo="0x…"
//	    ntStatus="0x…" relativePath="…" encodingType="…" encodedPath="…"
//	    encodedRelativePath="…" encodedNewName="…"/>
//	</EventList></CheckEventRequest>
func IsCheckEventRequest(body []byte) bool {
	return rootElementIs(body, "CheckEventRequest")
}

type checkEventRequest struct {
	XMLName   xml.Name `xml:"CheckEventRequest"`
	EventList struct {
		Count  string          `xml:"count,attr"`
		Events []checkEventXML `xml:"Event"`
	} `xml:"EventList"`
}

// eventExtXML is the EventExt child element, observed on every PowerStore
// event and absent from the CIFS samples:
//
//	<EventExt inode="9450" userId="0" ownerId="0" fsId="0xb0000009f" parentInode="2"/>
//
// Only the ids are consumed. inode, fsId and parentInode identify the object
// rather than the actor, and ObjectName already names it by path.
type eventExtXML struct {
	UserID  string `xml:"userId,attr"`
	OwnerID string `xml:"ownerId,attr"`
}

type checkEventXML struct {
	Event         string `xml:"event,attr"`
	Path          string `xml:"path,attr"`
	Flag          string `xml:"flag,attr"`
	Server        string `xml:"server,attr"`
	Share         string `xml:"share,attr"`
	ClientIP      string `xml:"clientIP,attr"`
	ServerIP      string `xml:"serverIP,attr"`
	TimeStamp     string `xml:"timeStamp,attr"`
	UserSid       string `xml:"userSid,attr"`
	OwnerSid      string `xml:"ownerSid,attr"`
	FileSize      string `xml:"fileSize,attr"`
	NewName       string `xml:"newName,attr"`
	DesiredAccess string `xml:"desiredAccess,attr"`
	CreateDispo   string `xml:"createDispo,attr"`
	NTStatus      string `xml:"ntStatus,attr"`
	RelativePath  string `xml:"relativePath,attr"`

	// Fields beyond the seven convertCheckEvent reads are declared
	// deliberately: this struct is the machine-checked record of the wire
	// shape, and the ones not yet consumed (newName on a rename, desiredAccess
	// and createDispo on an open, ntStatus on a failure) are what the
	// still-unmeasured event codes will need. encoding/xml ignores undeclared
	// attributes, so they cost nothing at runtime.
	//
	// CEE sends the path twice when it cannot represent it in the document's
	// encoding: encodingType names the scheme and the encoded* attributes
	// carry the real values.
	EncodingType        string `xml:"encodingType,attr"`
	EncodedPath         string `xml:"encodedPath,attr"`
	EncodedRelativePath string `xml:"encodedRelativePath,attr"`

	// EventExt is where an NFS event carries its actor. NFS has no Windows
	// SID to put in userSid, so PowerStore sends the POSIX ids on this child
	// element instead and omits userSid entirely.
	Ext            eventExtXML `xml:"EventExt"`
	EncodedNewName string      `xml:"encodedNewName,attr"`
}

// ParseCheckEventRequest decodes a CEE <CheckEventRequest> into the same
// CEPAEvent values the OneFS and VCAPS paths produce, so all three feed one
// mapper and one set of writers.
//
// receiveTime is the fallback when an event carries no usable timestamp.
func ParseCheckEventRequest(body []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	decoded, err := DecodeBody(body)
	if err != nil {
		return nil, fmt.Errorf("decoding CEE CheckEventRequest: %w", err)
	}
	return parseCheckEventRequestDecoded(decoded, receiveTime)
}

// parseCheckEventRequestDecoded is ParseCheckEventRequest's body, for
// already-decoded input.
func parseCheckEventRequestDecoded(decoded []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	var r checkEventRequest
	if err := xml.Unmarshal(decoded, &r); err != nil {
		return nil, fmt.Errorf("parsing CEE CheckEventRequest: %w", err)
	}
	if len(r.EventList.Events) == 0 {
		// count="0" is legitimate — CEE sends EVENT_ADMIN_RESYNC notices with
		// no event body. Naming it beats a generic failure, because the caller
		// logs the payload's redacted structure and an empty list is not a
		// defect.
		return nil, fmt.Errorf("CEE CheckEventRequest carries no <Event> (count=%q)", r.EventList.Count)
	}

	out := make([]CEPAEvent, 0, len(r.EventList.Events))
	for _, e := range r.EventList.Events {
		out = append(out, convertCheckEvent(e, receiveTime))
	}
	return out, nil
}

func convertCheckEvent(e checkEventXML, fallback time.Time) CEPAEvent {
	return CEPAEvent{
		EventType:  ceeEventTypeName(e.Event),
		FilePath:   ceeEventPath(e),
		UserSID:    ceeUserSID(e),
		ClientAddr: e.ClientIP,
		Timestamp:  ceeTimestamp(e.TimeStamp, fallback),

		// CEE's Event element carries no read/write counters — those exist
		// only on the OneFS NFSEventArgs and the VCAPS CEEEvent. fileSize is
		// the object's size, not bytes transferred, so it is deliberately not
		// mapped onto BytesWritten: that field asserts I/O volume and would
		// be a fabrication here.
	}
}

// ceeEventPath prefers the encoded form when CEE supplies one. A path CEE
// could not represent in the document encoding is exactly the path most worth
// getting right — non-ASCII filenames — and the plain attribute is lossy in
// that case by construction.
func ceeEventPath(e checkEventXML) string {
	if e.EncodedPath != "" {
		if p, err := decodeCEEEncoded(e.EncodedPath, e.EncodingType); err == nil && p != "" {
			return p
		}
		// Fall through to the plain attribute rather than returning nothing:
		// a lossy path beats an empty one, and the handler logs the payload's
		// redacted structure.
	}
	return e.Path
}

// decodeCEEEncoded decodes an encoded* attribute. Only base64 has been
// observed as an encodingType in this family (it is what OneFS uses for every
// name attribute); an unrecognised scheme is an error rather than a silent
// pass-through, so a path is never reported as decoded when it was not.
func decodeCEEEncoded(s, encodingType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(encodingType)) {
	case "", "base64", "base64utf16", "utf16":
		// Same encoding OneFS uses for every name attribute, so the same
		// decoder: if CEE turns out to use unpadded or URL-safe base64, that
		// is one place to fix rather than two that share no name.
		return decodeBase64UTF16(s)
	default:
		return "", fmt.Errorf("unknown encodingType %q", encodingType)
	}
}

// ceeEventTypeName resolves the numeric event code to a CEPP_* string. Codes
// arrive 0x-prefixed; both forms are accepted because nothing guarantees the
// prefix on every attribute.
func ceeEventTypeName(raw string) string {
	n, err := parseCEEHex(raw)
	if err != nil {
		return unmappedCEEPrefix + "INVALID"
	}
	if name, ok := ceeEventType[n]; ok {
		return name
	}
	return unmappedCEEPrefix + strconv.FormatUint(n, 10)
}

// IsUnmappedCEEEventType reports whether an event carries a CEE event code
// this package has no established meaning for, so the caller can log the gap.
func IsUnmappedCEEEventType(eventType string) bool {
	return strings.HasPrefix(eventType, unmappedCEEPrefix)
}

func parseCEEHex(raw string) (uint64, error) { return parseCEENum(raw, 16) }

// parseCEENum strips an optional 0x prefix and parses the rest as hex; without
// the prefix it uses unprefixedBase, which differs by attribute (event codes
// are hex either way, timestamps are decimal).
//
// strconv.ParseUint(s, 0, 64) is NOT a valid shortcut: base 0 reads an
// unprefixed "200" as decimal, and TestCEEEventTypeMapping pins it as hex.
func parseCEENum(raw string, unprefixedBase int) (uint64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, unprefixedBase, 64)
}

// ceeTimestamp reads the timeStamp attribute, in either of the two forms CEE
// sends, and falls back to receive time rather than to the zero time, which
// would sort every unusable record to 1970.
//
// The CIFS/OneFS form is whole seconds since the Unix epoch, decimal:
//
//	timeStamp="1786735002"
//
// PowerStore sends a packed 64-bit value instead, hex, captured off the wire
// from NAS01 on 2026-08-22:
//
//	timeStamp="0x6a7f7c090008765f"
//	            ^^^^^^^^ epoch second   ^^^^^^^^ sub-second remainder
//
// The high 32 bits are the epoch second (0x6a7f7c09 = 1786739721 =
// 2026-08-14T20:35:21Z, corroborated by the pstest-1786739721.txt filename in
// the same event). Reading all 64 bits as a second count yields the year
// 243179022179, which is what shipped before this split existed.
//
// The low word is deliberately discarded. It is a sub-second remainder whose
// unit is not documented by Dell and could not be determined from the capture
// — every event in it replayed one identical timestamp, so there was no
// variation to measure the scale against. Second resolution is what CEPA
// promises and what the audit record needs; inventing a microsecond or
// nanosecond reading from one sample would be a fabrication.
func ceeTimestamp(raw string, fallback time.Time) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fallback
	}
	// Accept the 0x form too: several sibling attributes carry it, and there
	// is no guarantee this one never will.
	n, err := parseCEENum(s, 10)
	if err != nil || n == 0 {
		return fallback
	}
	// A value too large to be a plain epoch second is the packed form. The
	// discriminator is exact rather than heuristic: a real epoch second stays
	// inside 32 bits until the year 2106, and the packed form always carries a
	// non-zero epoch in its high word, so it always exceeds 32 bits.
	//
	// This runs before any range rejection, and the order is load-bearing. An
	// earlier guard rejected n > MaxInt64 outright, written when the attribute
	// was believed to be a plain second count — where such a value is nonsense
	// and the int64 conversion below would wrap it negative, landing the record
	// before 1970. For a packed value it is not nonsense: the epoch is in the
	// high word, so every event after 2038-01-19 sets bit 63 and exceeds
	// MaxInt64. Rejecting first would have discarded a good timestamp for
	// receive time from 2038 on.
	//
	// Unpacking first makes that rejection unnecessary rather than merely
	// reordered: the shifted value is at most MaxUint32, and a value that was
	// already inside 32 bits is unchanged, so n is always <= MaxUint32 here and
	// the conversion cannot wrap.
	if n > math.MaxUint32 {
		n >>= 32
	}
	return time.Unix(int64(n), 0).UTC()
}

// IsHeartBeatRequest reports whether the body is CEE's post-registration
// liveness probe.
//
// This is the third thing CEE sends over HTTP, after <RegisterRequest /> and
// alongside <CheckEventRequest>. Its literal sits in libCEPPAPIWrapper.so
// immediately beside CHttpClient's other request bodies:
//
//	<RegisterRequest />
//	<HeartBeatRequest />
//	CHttpClient
//	hbStatus=
//	ntStatus=
//
// It had never been seen on the wire because registration never succeeded, so
// CEE never got as far as sending one.
func IsHeartBeatRequest(body []byte) bool {
	return rootElementIs(body, "HeartBeatRequest")
}

// ceeUserSID resolves the event's actor. CIFS events carry a real Windows SID
// in userSid; NFS events carry no userSid at all and put a POSIX uid on
// EventExt instead, which left every NFS-sourced record with an empty subject.
//
// The uid is rendered S-1-22-1-<uid>, the well-known mapping for a POSIX
// account and the same form OneFS puts in userSid natively (see the worked
// payload above ParseCheckFileRequest) — so this is a translation into a
// representation already present in this protocol, not a synthetic identity.
// uid 0 is a legitimate actor and must render, hence the empty-string test
// rather than a zero test.
func ceeUserSID(e checkEventXML) string {
	if e.UserSid != "" {
		return e.UserSid
	}
	if e.Ext.UserID != "" {
		return "S-1-22-1-" + e.Ext.UserID
	}
	return ""
}
