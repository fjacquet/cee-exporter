package parser

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OneFSEventAction is the Args/@action value OneFS uses for a file event, as
// opposed to OneFSHeartbeatAction for its heartbeat. Both arrive in the same
// <CheckFileRequest> element and both need a CheckFileResponse back; only this
// attribute separates them.
const OneFSEventAction = "11"

// unmappedPrefix marks an event whose OneFS eventType has no established
// meaning. The numeric value is preserved after the prefix so the record still
// carries the truth, and IsUnmappedOneFSEventType lets the caller log the gap.
const unmappedPrefix = "CEPP_ONEFS_UNMAPPED_"

// onefsEventType translates the numeric NFSEventArgs/@eventType into the CEPP_*
// vocabulary the rest of the pipeline keys on.
//
// The values are a bitmask (every one observed is a power of two). All six
// are established by measurement against a live OneFS 9.13.0.0 cluster, none
// is inferred:
//
//	8     open/create   carries desiredAccess and createDispo
//	32    delete
//	128   close after writing, carries bytesWritten and numberOfWrites
//	256   ordinary close, same attributes but zeroed
//	512   rename        emitted against the SOURCE path
//	2048  set_security  (chmod over NFS)
//
// The last three came from an isolation run on 2026-08-14: one operation per
// 10-second window — touch, mv, chmod, rm — so each landed alone and could be
// attributed. An earlier capture of the same four operations batched together
// could not separate them, and they were deliberately left unmapped rather
// than guessed. The attribution closes on itself: rm produced 32, so 512
// cannot be delete; mv produced only 512, so 512 is the rename.
//
// 8 covers open as well as create: createDispo distinguishes them and is
// preserved on the event, but the CEPP vocabulary has no separate open, and
// CEPP_CREATE_FILE is the family Dell CEE itself reports for this case.
//
// These six are also the corroboration for ceeEventType in checkevent.go, whose
// twenty-one entries come from Dell's documented ordering rather than from
// measurement: every value here lands on the same name at the same numeric code
// there. TestEventTablesCorroborate fails if either table drifts from the
// other, which would mean one of them has been edited without re-checking the
// evidence.
//
// All six map to the _FILE members of the CEPP vocabulary. Nothing in the
// payload distinguishes a file from a directory — there is no such attribute
// — so the _DIRECTORY variants are unreachable from this path, and
// mapper.objectType's trailing-slash guess is what decides ObjectType.
var onefsEventType = map[int]string{
	8:    "CEPP_CREATE_FILE",
	32:   "CEPP_DELETE_FILE",
	128:  "CEPP_CLOSE_MODIFIED",
	256:  "CEPP_CLOSE_UNMODIFIED",
	512:  "CEPP_RENAME_FILE",
	2048: "CEPP_SETACL_FILE",
}

// IsUnmappedOneFSEventType reports whether an event carries a OneFS eventType
// this package has no established meaning for. Such events are still parsed
// and still flow to the writers — losing them would be worse, because the
// CheckFileResponse has already advanced the cluster's forwarding cursor by
// the time anyone could decide to drop them — but the caller should log them
// so the gap stays visible.
func IsUnmappedOneFSEventType(eventType string) bool {
	return strings.HasPrefix(eventType, unmappedPrefix)
}

// onefsRequest mirrors the OneFS event payload. Measured on the wire from a
// 4-node OneFS 9.13.0.0 cluster (2026-08-14):
//
//	<CheckFileRequest><Args action="11" sourceIP="10.26.1.150" sourceID="2"
//	  name="<base64 UTF-16LE UNC path>" protocol="1">
//	  <Cluster id="00505692…" name="<base64>"/>
//	  <Zone id="1" name="<base64 System>"/></Args>
//	  <NFSEventArgs eventType="256" numberOfWrites="0" numberOfReads="0"
//	    bytesRead="0" bytesWritten="0" userSid="S-1-22-1-0"
//	    clientIP="10.26.1.222" serverName="<base64>" ntStatus="0x0" userId="0"
//	    timeStamp="1786734886" timeStampMicroSeconds="18481"
//	    inode="4295174245" fsId="1"/></CheckFileRequest>
type onefsRequest struct {
	XMLName xml.Name `xml:"CheckFileRequest"`
	Args    struct {
		Action   string `xml:"action,attr"`
		SourceIP string `xml:"sourceIP,attr"`
		Name     string `xml:"name,attr"`
		Protocol string `xml:"protocol,attr"`
	} `xml:"Args"`
	NFS *onefsNFSEventArgs `xml:"NFSEventArgs"`
}

type onefsNFSEventArgs struct {
	EventType             string `xml:"eventType,attr"`
	DesiredAccess         string `xml:"desiredAccess,attr"`
	CreateDispo           string `xml:"createDispo,attr"`
	NumberOfWrites        string `xml:"numberOfWrites,attr"`
	NumberOfReads         string `xml:"numberOfReads,attr"`
	BytesRead             string `xml:"bytesRead,attr"`
	BytesWritten          string `xml:"bytesWritten,attr"`
	UserSid               string `xml:"userSid,attr"`
	ClientIP              string `xml:"clientIP,attr"`
	ServerName            string `xml:"serverName,attr"`
	TimeStamp             string `xml:"timeStamp,attr"`
	TimeStampMicroSeconds string `xml:"timeStampMicroSeconds,attr"`
}

// ParseOneFSEvent decodes a OneFS <CheckFileRequest action="11"> into the same
// CEPAEvent the PowerStore path produces, so both feed one mapper and one set
// of writers rather than a parallel pipeline.
//
// receiveTime is the fallback when the payload carries no usable timestamp.
func ParseOneFSEvent(body []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	decoded, err := decodeUTF16(body)
	if err != nil {
		return nil, fmt.Errorf("decoding OneFS payload: %w", err)
	}
	return parseOneFSEventDecoded(decoded, receiveTime)
}

// parseOneFSEventDecoded is ParseOneFSEvent's body, for already-decoded input.
func parseOneFSEventDecoded(decoded []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	var r onefsRequest
	if err := xml.Unmarshal(decoded, &r); err != nil {
		return nil, fmt.Errorf("parsing OneFS CheckFileRequest: %w", err)
	}
	if r.NFS == nil {
		// SMB events presumably arrive in a sibling element, but none has ever
		// been captured. Naming what was missing beats a generic failure: the
		// caller logs the body, so an unseen element is recoverable from the
		// log rather than silently dropped.
		return nil, fmt.Errorf("OneFS event carries no <NFSEventArgs> (only NFS has been observed)")
	}

	path, err := decodeBase64UTF16(r.Args.Name)
	if err != nil {
		// A file event whose path cannot be decoded is not worth writing: the
		// object is the whole point of the record.
		return nil, fmt.Errorf("decoding OneFS event path: %w", err)
	}

	return []CEPAEvent{{
		EventType:  onefsEventTypeName(r.NFS.EventType),
		FilePath:   path,
		UserSID:    r.NFS.UserSid,
		ClientAddr: r.NFS.ClientIP,
		Timestamp:  onefsTimestamp(r.NFS.TimeStamp, r.NFS.TimeStampMicroSeconds, receiveTime),

		BytesRead:      parseInt64(r.NFS.BytesRead),
		BytesWritten:   parseInt64(r.NFS.BytesWritten),
		NumberOfReads:  parseInt64(r.NFS.NumberOfReads),
		NumberOfWrites: parseInt64(r.NFS.NumberOfWrites),
	}}, nil
}

// onefsEventTypeName resolves the numeric eventType to a CEPP_* string,
// falling back to an explicitly-unmapped label that keeps the raw value.
func onefsEventTypeName(raw string) string {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return unmappedPrefix + "INVALID"
	}
	if name, ok := onefsEventType[n]; ok {
		return name
	}
	return unmappedPrefix + strconv.Itoa(n)
}

// onefsTimestamp combines the whole-second and microsecond attributes. OneFS
// sends them separately; dropping the microseconds would collapse the four
// close events a single touch produces onto one instant and make their order
// unrecoverable.
func onefsTimestamp(sec, usec string, fallback time.Time) time.Time {
	s, err := strconv.ParseInt(strings.TrimSpace(sec), 10, 64)
	if err != nil || s <= 0 {
		return fallback
	}
	us, err := strconv.ParseInt(strings.TrimSpace(usec), 10, 64)
	if err != nil || us < 0 {
		us = 0
	}
	return time.Unix(s, us*int64(time.Microsecond)).UTC()
}

// decodeBase64UTF16 decodes the base64-of-UTF-16LE encoding OneFS uses for
// every name attribute. The path arrives UNC-shaped, e.g.
// \\powerscale1-1\onefs$\ifs\test\evtest-1786735002.txt
func decodeBase64UTF16(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty name attribute")
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	// decodeUTF16 sniffs the encoding rather than assuming; a name that is
	// somehow already UTF-8 comes back untouched instead of being mangled.
	out, err := decodeUTF16(raw)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
