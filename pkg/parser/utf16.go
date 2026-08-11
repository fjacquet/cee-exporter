package parser

import (
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeUTF16 converts a UTF-16 CEPA payload to UTF-8, returning the input
// unchanged if it is not UTF-16.
//
// Dell CEE sends UTF-16LE without a BOM. Measured on the wire against CEE
// 9.2.0.0 (2026-08-11), the RegisterRequest handshake is 38 bytes for a
// 19-character document, with `Accept-Charset: utf-16` in the request headers:
//
//	<.R.e.g.i.s.t.e.r.R.e.q.u.e.s.t. ./.>.
//
// Before this existed, IsRegisterRequest and Parse compared raw ASCII bytes,
// so every handshake fell through to the parse-error path and every UTF-16
// event payload was dropped after being ACKed with HTTP 200 (see issue #32).
//
// Decoding happens once, at the head of both entry points, so the two cannot
// disagree about what a payload says.
func decodeUTF16(body []byte) ([]byte, error) {
	if len(body) < 2 {
		return body, nil
	}

	switch {
	case body[0] == 0xFF && body[1] == 0xFE:
		return decodeUTF16Pairs(body[2:], true)
	case body[0] == 0xFE && body[1] == 0xFF:
		return decodeUTF16Pairs(body[2:], false)
	}

	// No BOM — which is how CEE actually sends it, so this is the live path,
	// not a fallback. A CEPA document always starts with '<', so the first
	// code unit is 0x003C: little-endian puts the zero byte second,
	// big-endian first. Anything else is treated as UTF-8 and returned
	// untouched, which keeps ASCII and UTF-8 payloads working.
	switch {
	case body[0] != 0x00 && body[1] == 0x00:
		return decodeUTF16Pairs(body, true)
	case body[0] == 0x00 && body[1] != 0x00:
		return decodeUTF16Pairs(body, false)
	}

	return body, nil
}

// decodeUTF16Pairs converts UTF-16 code units to UTF-8.
//
// An odd trailing byte is an error, not something to discard. Dropping it
// would let a truncated payload decode to a well-formed XML prefix, which the
// parser would then accept as a complete event — a partial write silently
// becoming a whole audit record. Better to reject the payload than to invent
// one that was never sent.
func decodeUTF16Pairs(b []byte, littleEndian bool) ([]byte, error) {
	if len(b)%2 != 0 {
		return nil, fmt.Errorf("incomplete UTF-16 payload: %d bytes is not a whole number of code units", len(b))
	}

	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if littleEndian {
			units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
		} else {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		}
	}

	runes := utf16.Decode(units)
	out := make([]byte, 0, len(runes)*utf8.UTFMax)
	for _, r := range runes {
		out = utf8.AppendRune(out, r)
	}
	return out, nil
}
