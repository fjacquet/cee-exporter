package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxRedactedPayload bounds the diagnostic rendered into a log line. A CEPA
// body may be up to 64 MiB and a VCAPS batch carries thousands of events; the
// shape of the first few hundred bytes is what identifies a malformed payload,
// and the rest only floods the log.
const maxRedactedPayload = 512

// redactPayload renders a payload for a diagnostic log with its values removed.
//
// Element and attribute NAMES are kept: a parse failure is diagnosed from the
// shape of the document — which element arrived, which attributes it carried.
// Every attribute value and text node is dropped, because on this protocol
// those are the audit record itself: file paths, usernames, client addresses
// and SIDs. Logging a failed payload verbatim published all of it to whatever
// ships the logs, at a lower classification than the audit trail the events are
// on their way to.
//
// The result is bounded by maxRedactedPayload and marked when truncated.
func redactPayload(b []byte) string {
	var out strings.Builder
	inTag := false
	truncated := false
	i := 0

	for i < len(b) {
		if out.Len() >= maxRedactedPayload {
			truncated = true
			break
		}
		c := b[i]
		switch {
		case !inTag && c == '<':
			inTag = true
			out.WriteByte(c)
			i++
		case !inTag:
			// A text node between elements. Skip it, but record that content
			// was there — an empty gap and a dropped value look different.
			start := i
			for i < len(b) && b[i] != '<' {
				i++
			}
			if len(bytes.TrimSpace(b[start:i])) > 0 {
				out.WriteString("...")
			}
		case c == '>':
			inTag = false
			out.WriteByte(c)
			i++
		case c == '"' || c == '\'':
			quote := c
			i++
			for i < len(b) && b[i] != quote {
				i++
			}
			if i < len(b) {
				i++ // consume the closing quote
			}
			out.WriteString(`"..."`)
		default:
			out.WriteByte(c)
			i++
		}
	}

	s := out.String()
	if !truncated && len(s) <= maxRedactedPayload {
		return s
	}
	// The loop checks the bound before a write that can emit several bytes, so
	// it may overshoot slightly. Cut to the bound exactly, then back off any
	// partial rune the cut created — an element name is not guaranteed ASCII.
	if len(s) > maxRedactedPayload {
		s = strings.ToValidUTF8(s[:maxRedactedPayload], "")
	}
	return s + "...(truncated)"
}

// payloadDigest identifies a payload across log lines without reproducing it,
// so a burst of failures can be told apart from one failure retried, and a
// report can be matched against a capture held elsewhere.
func payloadDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
