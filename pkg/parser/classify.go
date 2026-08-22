package parser

import (
	"bytes"
	"fmt"
	"time"
)

// Dialect names one of the payload shapes that arrive on the CEPA URL.
//
// All of them are POSTed or PUT to the same path by different publishers, and
// answering one with another's document is a silent, fatal failure — see
// pkg/server. Recognition lives here; the reply documents live in pkg/server.
type Dialect int

const (
	// DialectUnknown is a payload whose root element matches none of the
	// others. It is not an error on its own: the VCAPS event shapes are
	// recognised by Parse rather than by root element.
	DialectUnknown Dialect = iota
	DialectRegisterRequest
	DialectHeartBeatRequest
	DialectCheckFileRequest  // OneFS: heartbeat and event share this element
	DialectCheckEventRequest // Dell CEE's event delivery
)

// Classify transcodes the body once and reports which dialect it is, returning
// the decoded bytes for the caller to hand to the matching parser.
//
// This exists because transcoding is the dominant cost of handling a request.
// Every Is* predicate calls decodeUTF16 on the whole body just to read its root
// element, so dispatching through four predicates and then parsing decoded the
// same payload five times. Measured on a 1000-event UTF-16LE batch (698 KB):
// 41.2 ms and 47.6 MB per request, of which 25.5 ms and 43.7 MB — 62% of the
// time and 92% of the allocations — was redundant transcoding. decodeUTF16
// allocates roughly 12.5x the body size each time it runs, and pkg/server
// accepts bodies up to 64 MiB against a ~3 s CEPA deadline.
//
// The Is* predicates remain for callers holding a raw body; they are unchanged
// and still decode. Classify is what the dispatcher should use.
func Classify(body []byte) (Dialect, []byte, error) {
	decoded, err := decodeUTF16(body)
	if err != nil {
		return DialectUnknown, nil, fmt.Errorf("decoding CEPA payload: %w", err)
	}
	return classifyDecoded(decoded), decoded, nil
}

// classifyDecoded reports the dialect of an already-decoded payload.
func classifyDecoded(decoded []byte) Dialect {
	switch {
	case rootIs(decoded, "RegisterRequest"):
		return DialectRegisterRequest
	case rootIs(decoded, "HeartBeatRequest"):
		return DialectHeartBeatRequest
	case rootIs(decoded, "CheckFileRequest"):
		return DialectCheckFileRequest
	case rootIs(decoded, "CheckEventRequest"):
		return DialectCheckEventRequest
	default:
		return DialectUnknown
	}
}

// rootIs is rootElementIs for input that is already decoded: it prefix-matches
// the root element after skipping any XML declaration. Prefix-matching the root
// rather than searching the document is what stops an event whose file path
// contains the word from being taken for a handshake.
func rootIs(decoded []byte, name string) bool {
	trimmed := bytes.TrimSpace(decoded)
	if bytes.HasPrefix(trimmed, []byte("<?xml")) {
		if idx := bytes.Index(trimmed, []byte("?>")); idx >= 0 {
			trimmed = bytes.TrimSpace(trimmed[idx+2:])
		}
	}
	open := []byte("<" + name)
	if !bytes.HasPrefix(trimmed, open) {
		return false
	}
	// The name has to END here. Prefix-matching alone accepted
	// <CheckFileRequestExtra> as <CheckFileRequest>, so the handler answered
	// one dialect's protocol document to another's request and then parsed the
	// payload on the wrong path — both silent failures on this protocol.
	rest := trimmed[len(open):]
	if len(rest) == 0 {
		return false
	}
	return isNameDelimiter(rest[0])
}

// isNameDelimiter reports whether c can legally follow an XML element name:
// the tag closes (`>`), self-closes (`/`), or attributes follow (whitespace).
// Anything else means the name continues and this is a different element.
func isNameDelimiter(c byte) bool {
	switch c {
	case '>', '/', ' ', '\t', '\r', '\n':
		return true
	}
	return false
}

// ParseDecoded is Parse for input Classify has already decoded.
func ParseDecoded(decoded []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	return parseDecoded(decoded, receiveTime)
}

// ParseOneFSEventDecoded is ParseOneFSEvent for already-decoded input.
func ParseOneFSEventDecoded(decoded []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	return parseOneFSEventDecoded(decoded, receiveTime)
}

// ParseCheckEventRequestDecoded is ParseCheckEventRequest for already-decoded
// input.
func ParseCheckEventRequestDecoded(decoded []byte, receiveTime time.Time) ([]CEPAEvent, error) {
	return parseCheckEventRequestDecoded(decoded, receiveTime)
}

// CheckFileActionDecoded is CheckFileAction for already-decoded input.
func CheckFileActionDecoded(decoded []byte) string {
	return checkFileActionDecoded(decoded)
}
