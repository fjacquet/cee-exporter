// Package parser decodes CEE/CEPA XML payloads from Dell PowerStore into
// strongly-typed CEPAEvent slices.
//
// Two payload shapes are supported:
//  1. Single-event: <CEEEvent>…</CEEEvent>
//  2. VCAPS bulk batch: <EventBatch><CEEEvent>…</CEEEvent>…</EventBatch>
//
// Four non-VCAPS shapes arrive on the same URL and are detected separately —
// callers must dispatch on them before calling Parse, because none is a VCAPS
// event payload:
//
//	<RegisterRequest />    Dell CEE, opening registration
//	<HeartBeatRequest />   Dell CEE, liveness probe once registered
//	<CheckEventRequest>…   Dell CEE, event delivery
//	<CheckFileRequest>…    PowerScale (OneFS) — heartbeat AND event share this
//	                       element; use CheckFileAction to tell them apart
//
// Prefer Classify over the individual Is* predicates: it decodes the body once
// and returns both the dialect and the decoded bytes, where dispatching through
// the predicates decodes the whole payload once per candidate.
package parser

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CEPAEvent is the normalised representation of a single CEPA audit event.
type CEPAEvent struct {
	// Raw CEPA identifier, e.g. "CEPP_FILE_WRITE"
	EventType string

	// Filesystem path of the affected object
	FilePath string

	// User context
	Username string
	Domain   string
	UserSID  string
	LogonID  string

	// Network context
	ClientAddr string

	// Event timestamp (parsed from the XML or synthesised from receive time)
	Timestamp time.Time

	// I/O statistics — only meaningful for CEPP_CLOSE_MODIFIED
	BytesRead      int64
	BytesWritten   int64
	NumberOfReads  int64
	NumberOfWrites int64
}

// IsRegisterRequest returns true if the body is the CEPA handshake payload.
// Matches a <RegisterRequest> root element — guards against event payloads
// whose content (e.g. a file path) happens to contain the word.
//
// This is the PowerStore/CEE dialect. OneFS opens with a different element
// entirely — see IsCheckFileRequest.
func IsRegisterRequest(body []byte) bool {
	return rootElementIs(body, "RegisterRequest")
}

// IsCheckFileRequest returns true if the body is the OneFS (PowerScale)
// heartbeat, which is a <CheckFileRequest> rather than PowerStore's
// <RegisterRequest>.
//
// Measured on the wire from a 4-node OneFS 9.13.0.0 cluster (2026-08-12),
// 229 bytes of plain UTF-8 — not the 38-byte UTF-16LE that CEE sends:
//
//	<CheckFileRequest><Args action="9" sourceIP="10.26.1.150" sourceID="2"
//	  name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="><Cluster id="00505692…"
//	  name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/></Args></CheckFileRequest>
//
// `action="9"` is the heartbeat; `name` is the cluster name as base64 of
// UTF-16LE.
//
// The element alone does not tell you what the payload is: OneFS carries its
// *events* in the same CheckFileRequest element, distinguished only by the
// action attribute. Use CheckFileAction to tell them apart — both need a
// CheckFileResponse back, but only action 9 is a heartbeat.
func IsCheckFileRequest(body []byte) bool {
	return rootElementIs(body, "CheckFileRequest")
}

// OneFSHeartbeatAction is the Args/@action value OneFS uses for its heartbeat.
// Events use other values — action 11 was measured for NFS file events.
const OneFSHeartbeatAction = "9"

// CheckFileAction returns the Args/@action attribute of a CheckFileRequest,
// or "" if the payload cannot be decoded or carries no action.
//
// This distinction is load-bearing. A CheckFileRequest with action 11 holds a
// real audit event:
//
//	<CheckFileRequest><Args action="11" … name="<base64 UTF-16LE UNC path>"
//	  protocol="1"><Cluster …/><Zone …/></Args>
//	  <NFSEventArgs eventType="8" desiredAccess="0x100106" createDispo="0x3"
//	    userSid="S-1-22-1-1000" clientIP="10.26.1.222" userId="1000"
//	    timeStamp="1786563708" inode="4295432746" fsId="1"/></CheckFileRequest>
//
// Answering that with a heartbeat response and nothing else makes OneFS
// advance its forwarding cursor — the event is acknowledged and gone. Treating
// every CheckFileRequest as a heartbeat therefore loses events *silently*,
// which is worse than rejecting them.
func CheckFileAction(body []byte) string {
	decoded, err := decodeUTF16(body)
	if err != nil {
		return ""
	}
	return checkFileActionDecoded(decoded)
}

// checkFileActionDecoded is CheckFileAction for already-decoded input.
func checkFileActionDecoded(decoded []byte) string {
	var r struct {
		XMLName xml.Name `xml:"CheckFileRequest"`
		Args    struct {
			Action string `xml:"action,attr"`
		} `xml:"Args"`
	}
	if err := xml.Unmarshal(decoded, &r); err != nil {
		return ""
	}
	return r.Args.Action
}

// rootElementIs reports whether the payload's root element has the given
// name, after transcoding and skipping any XML declaration. Prefix-matching
// the root rather than searching the whole document is what stops an event
// whose file path happens to contain the word from being taken for a
// handshake.
func rootElementIs(body []byte, name string) bool {
	decoded, err := decodeUTF16(body)
	if err != nil {
		// A payload that cannot be decoded is not a handshake. Parse will
		// report the reason; here the only question is which branch to take.
		return false
	}
	return rootIs(decoded, name)
}

// ----------------------------------------------------------------------------
// Internal XML structures
// ----------------------------------------------------------------------------

// rawBatch is the top-level VCAPS wrapper.
type rawBatch struct {
	XMLName xml.Name   `xml:"EventBatch"`
	Events  []rawEvent `xml:"CEEEvent"`
}

// rawSingle wraps a single event at the top level.
type rawSingle struct {
	XMLName xml.Name `xml:"CEEEvent"`
	rawEvent
}

// rawEvent mirrors the CEEEvent XML structure.  Field names are case-sensitive
// to match the Dell CEPA XML schema.
type rawEvent struct {
	EventType string `xml:"EventType"`

	// Some implementations use <Timestamp>, others embed it in attributes.
	Timestamp string `xml:"Timestamp"`

	// File/object info
	FilePath string `xml:"FilePath"`

	// User identity fields
	UserSID  string `xml:"UserSID"`
	Username string `xml:"Username"`
	Domain   string `xml:"Domain"`
	LogonID  string `xml:"LogonID"`

	// Network
	ClientAddress string `xml:"ClientAddress"`

	// I/O stats (CEPP_CLOSE_MODIFIED only)
	BytesRead      string `xml:"BytesRead"`
	BytesWritten   string `xml:"BytesWritten"`
	NumberOfReads  string `xml:"NumberOfReads"`
	NumberOfWrites string `xml:"NumberOfWrites"`
}

// ----------------------------------------------------------------------------
// Parse
// ----------------------------------------------------------------------------

// Parse decodes one or more CEPA events from a raw XML body.
// The receiveTime is used as a fallback when the XML payload contains no
// timestamp.
func Parse(body []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty payload")
	}

	// CEE sends UTF-16LE without a BOM; encoding/xml cannot read it without a
	// CharsetReader, and the declaration-less payloads CEE sends give it
	// nothing to dispatch on. Transcode up front instead — see issue #32.
	decoded, err := decodeUTF16(body)
	if err != nil {
		return nil, fmt.Errorf("decoding CEPA payload: %w", err)
	}
	return parseDecoded(decoded, receiveTime)
}

// parseDecoded is Parse's body, for input that is already UTF-8. Split out so
// the dispatcher can decode once and hand the result to whichever parser wins;
// see Classify.
func parseDecoded(body []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty payload")
	}

	// Try batch first.
	var batch rawBatch
	if err := xml.Unmarshal(body, &batch); err == nil && len(batch.Events) > 0 {
		return convertAll(batch.Events, receiveTime), nil
	}

	// Try single event.
	var single rawSingle
	if err := xml.Unmarshal(body, &single); err == nil && single.EventType != "" {
		return []CEPAEvent{convert(single.rawEvent, receiveTime)}, nil
	}

	return nil, fmt.Errorf("unrecognised CEPA payload: %q…", truncate(string(body), 120))
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func convertAll(raws []rawEvent, fallback time.Time) []CEPAEvent {
	out := make([]CEPAEvent, 0, len(raws))
	for _, r := range raws {
		out = append(out, convert(r, fallback))
	}
	return out
}

func convert(r rawEvent, fallback time.Time) CEPAEvent {
	e := CEPAEvent{
		EventType:  r.EventType,
		FilePath:   r.FilePath,
		Username:   r.Username,
		Domain:     r.Domain,
		UserSID:    r.UserSID,
		LogonID:    r.LogonID,
		ClientAddr: r.ClientAddress,
		Timestamp:  parseTimestamp(r.Timestamp, fallback),

		BytesRead:      parseInt64(r.BytesRead),
		BytesWritten:   parseInt64(r.BytesWritten),
		NumberOfReads:  parseInt64(r.NumberOfReads),
		NumberOfWrites: parseInt64(r.NumberOfWrites),
	}
	return e
}

// parseTimestamp attempts several common formats before falling back.
var tsFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"20060102150405",
}

func parseTimestamp(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback
	}
	for _, f := range tsFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return fallback
}

func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
