package parser

import (
	"testing"
	"time"
)

// registerRequestUTF16LE is the exact 38-byte body captured from CEE 9.2.0.0
// on the wire (tcpdump, RHEL 9.8 → cee-exporter, 2026-08-11):
//
//	PUT / HTTP/1.1
//	Accept-Charset: utf-16
//	Content-Type: text/xml
//	Content-Length: 38
//
//	<.R.e.g.i.s.t.e.r.R.e.q.u.e.s.t. ./.>.
//
// 19 characters, 38 bytes, UTF-16LE, no BOM. Written out byte by byte rather
// than transcoded from a Go string: a fixture built with the same encoder the
// production code uses would pass even if both were wrong about what CEE sends.
var registerRequestUTF16LE = []byte{
	'<', 0, 'R', 0, 'e', 0, 'g', 0, 'i', 0, 's', 0, 't', 0, 'e', 0, 'r', 0,
	'R', 0, 'e', 0, 'q', 0, 'u', 0, 'e', 0, 's', 0, 't', 0, ' ', 0, '/', 0,
	'>', 0,
}

func TestIsRegisterRequest_RealCEEPayloads(t *testing.T) {
	// Sanity-check the fixture against the measured length before relying on
	// it: Content-Length was 38.
	if len(registerRequestUTF16LE) != 38 {
		t.Fatalf("fixture is %d bytes, want the 38 measured on the wire", len(registerRequestUTF16LE))
	}

	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{"utf16le no bom (as CEE sends it)", registerRequestUTF16LE, true},
		{"utf16le with bom", append([]byte{0xFF, 0xFE}, registerRequestUTF16LE...), true},
		{"utf16be with bom", append([]byte{0xFE, 0xFF}, utf16BE("<RegisterRequest />")...), true},
		{"utf16be no bom", utf16BE("<RegisterRequest />"), true},
		{"utf8 (still supported)", []byte(`<RegisterRequest/>`), true},
		{"utf8 with declaration", []byte("<?xml version=\"1.0\"?>\n<RegisterRequest/>"), true},
		{"utf16le event payload is not a handshake", utf16LE(`<CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent>`), false},
		{"utf8 event payload is not a handshake", []byte(`<CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent>`), false},
		{"empty", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRegisterRequest(tc.body); got != tc.want {
				t.Errorf("IsRegisterRequest(%q) = %v, want %v", truncate(string(tc.body), 60), got, tc.want)
			}
		})
	}
}

func TestParse_UTF16Payloads(t *testing.T) {
	receiveTime := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)

	const single = `<CEEEvent>` +
		`<EventType>CEPP_FILE_WRITE</EventType>` +
		`<FilePath>/mnt/share/file.txt</FilePath>` +
		`<Username>bob</Username>` +
		`</CEEEvent>`

	const batch = `<EventBatch>` +
		`<CEEEvent><EventType>CEPP_FILE_READ</EventType><FilePath>/a</FilePath></CEEEvent>` +
		`<CEEEvent><EventType>CEPP_DELETE_FILE</EventType><FilePath>/b</FilePath></CEEEvent>` +
		`</EventBatch>`

	cases := []struct {
		name      string
		body      []byte
		wantCount int
		wantFirst string
	}{
		{"single utf16le no bom", utf16LE(single), 1, "CEPP_FILE_WRITE"},
		{"single utf16le with bom", append([]byte{0xFF, 0xFE}, utf16LE(single)...), 1, "CEPP_FILE_WRITE"},
		{"single utf16be with bom", append([]byte{0xFE, 0xFF}, utf16BE(single)...), 1, "CEPP_FILE_WRITE"},
		{"single utf8 (still supported)", []byte(single), 1, "CEPP_FILE_WRITE"},
		{"single utf16be no bom", utf16BE(single), 1, "CEPP_FILE_WRITE"},
		{"batch utf16le", utf16LE(batch), 2, "CEPP_FILE_READ"},
		{"batch utf16le with bom", append([]byte{0xFF, 0xFE}, utf16LE(batch)...), 2, "CEPP_FILE_READ"},
		{"batch utf16be with bom", append([]byte{0xFE, 0xFF}, utf16BE(batch)...), 2, "CEPP_FILE_READ"},
		{"batch utf8 (still supported)", []byte(batch), 2, "CEPP_FILE_READ"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, err := Parse(tc.body, receiveTime)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if len(events) != tc.wantCount {
				t.Fatalf("parsed %d events, want %d", len(events), tc.wantCount)
			}
			if events[0].EventType != tc.wantFirst {
				t.Errorf("first EventType = %q, want %q", events[0].EventType, tc.wantFirst)
			}
			if events[0].FilePath == "" {
				t.Errorf("FilePath is empty — the decode dropped element content")
			}
		})
	}
}

// TestDecodeUTF16_LeavesUTF8Alone guards the detection heuristic. A UTF-8
// payload must pass through untouched, including one whose bytes could be
// mistaken for UTF-16 by a careless check.
func TestDecodeUTF16_LeavesUTF8Alone(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"ascii xml", `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent>`},
		{"utf8 with accents in a path", `<CEEEvent><FilePath>/mnt/partagé/fichier.txt</FilePath></CEEEvent>`},
		{"single byte", "<"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeUTF16([]byte(tc.body))
			if err != nil {
				t.Fatalf("decodeUTF16 returned error on a UTF-8 payload: %v", err)
			}
			if string(got) != tc.body {
				t.Errorf("decodeUTF16 altered a UTF-8 payload:\n got %q\nwant %q", got, tc.body)
			}
		})
	}
}

// utf16LE encodes s as UTF-16LE without a BOM. Test helper only — the
// wire-captured fixture above is deliberately not built with it.
func utf16LE(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// utf16BE encodes s as UTF-16BE without a BOM. Test helper only.
func utf16BE(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

// TestParse_TruncatedUTF16Rejected is the guard against a partial write
// becoming a whole audit record. A valid UTF-16 event followed by one stray
// byte decodes to well-formed XML if the odd byte is silently dropped — the
// parser would then accept a payload that was never fully sent.
func TestParse_TruncatedUTF16Rejected(t *testing.T) {
	const event = `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType><FilePath>/a</FilePath></CEEEvent>`

	whole := utf16LE(event)
	truncated := append(append([]byte{}, whole...), 0x3C) // one trailing byte

	// The whole payload must still parse, or this test proves nothing about
	// the trailing byte specifically.
	if _, err := Parse(whole, time.Now()); err != nil {
		t.Fatalf("the untruncated payload must parse, got: %v", err)
	}

	events, err := Parse(truncated, time.Now())
	if err == nil {
		t.Fatalf("Parse accepted a truncated UTF-16 payload, returning %d events; want an error", len(events))
	}
	if events != nil {
		t.Errorf("Parse returned %d events alongside an error, want nil", len(events))
	}

	// The handshake check must take the same view rather than guessing.
	if IsRegisterRequest(append(append([]byte{}, registerRequestUTF16LE...), 0x3C)) {
		t.Error("IsRegisterRequest accepted a truncated handshake payload")
	}
}
