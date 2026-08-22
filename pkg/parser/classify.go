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
// Every Is* predicate calls DecodeBody on the whole body just to read its root
// element, so dispatching through four predicates and then parsing decoded the
// same payload five times. Measured on a 1000-event UTF-16LE batch (698 KB):
// 41.2 ms and 47.6 MB per request, of which 25.5 ms and 43.7 MB — 62% of the
// time and 92% of the allocations — was redundant transcoding. DecodeBody
// allocates roughly 12.5x the body size each time it runs, and pkg/server
// accepts bodies up to 64 MiB against a ~3 s CEPA deadline.
//
// The Is* predicates remain for callers holding a raw body; they are unchanged
// and still decode. Classify is what the dispatcher should use.
func Classify(body []byte) (Dialect, []byte, error) {
	decoded, err := DecodeBody(body)
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

// rootIs reports whether an already-decoded payload's root element has the
// given name. It is the single implementation; rootElementIs decodes and then
// calls it.
//
// Prefix-matching the root rather than searching the document is what stops an
// event whose file path contains the word from being taken for a handshake --
// and the name must END at the element, or <RegisterRequestBatch> is answered
// as a RegisterRequest and <CheckFileRequestV2> gets a reply from a dialect it
// is not.
func rootIs(decoded []byte, name string) bool {
	trimmed := bytes.TrimSpace(decoded)
	// Skip an optional XML declaration: <?xml ...?>
	if bytes.HasPrefix(trimmed, []byte("<?xml")) {
		if idx := bytes.Index(trimmed, []byte("?>")); idx >= 0 {
			trimmed = bytes.TrimSpace(trimmed[idx+2:])
		}
	}
	if !bytes.HasPrefix(trimmed, []byte("<"+name)) {
		return false
	}
	rest := trimmed[len("<"+name):]
	if len(rest) == 0 {
		// "<RegisterRequest" and nothing else: unterminated, so not a
		// well-formed root element.
		return false
	}
	switch rest[0] {
	case '>':
		return true
	case '/', ' ', '\t', '\r', '\n':
		// The name ends here, but the start tag still has to be closed. A
		// body that stops inside it -- mid-attribute, or on the space after
		// the name -- is truncated, and a truncated payload is not a
		// handshake: answering it would be inventing a request that was
		// never fully sent, the same reasoning that makes decodeUTF16Pairs
		// reject an odd trailing byte. Parse reports it instead.
		return bytes.IndexByte(rest, '>') >= 0
	default:
		// Any other byte continues the element name -- a different element.
		return false
	}
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
