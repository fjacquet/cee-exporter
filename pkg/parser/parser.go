// Package parser decodes CEE/CEPA XML payloads from Dell PowerStore into
// strongly-typed CEPAEvent slices.
//
// Two payload shapes are supported:
//  1. Single-event: <CEEEvent>…</CEEEvent>
//  2. VCAPS bulk batch: <EventBatch><CEEEvent>…</CEEEvent>…</EventBatch>
//
// The CEPA RegisterRequest handshake (<RegisterRequest />) is detected and
// handled separately — callers should check for it before calling Parse.
package parser

import (
	"bytes"
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
func IsRegisterRequest(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	// Skip an optional XML declaration: <?xml ...?>
	if bytes.HasPrefix(trimmed, []byte("<?xml")) {
		if idx := bytes.Index(trimmed, []byte("?>")); idx >= 0 {
			trimmed = bytes.TrimSpace(trimmed[idx+2:])
		}
	}
	return bytes.HasPrefix(trimmed, []byte("<RegisterRequest"))
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
