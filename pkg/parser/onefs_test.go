package parser

import (
	"testing"
	"time"
)

// checkFileRequestOneFS is the exact heartbeat body captured on the wire from
// an OneFS 9.13.0.0 cluster (tcpdump, powerscale1-1 → CEE 9.2.0.0,
// 2026-08-12). 229 bytes, plain UTF-8, no XML declaration, no BOM —
// deliberately unlike the 38-byte UTF-16LE <RegisterRequest/> that CEE itself
// sends, which is why the two need separate detection.
//
// Written as the literal bytes that were on the wire rather than assembled
// from parts, for the same reason registerRequestUTF16LE is: a fixture built
// by the code under test proves nothing about what the cluster sends.
const checkFileRequestOneFS = `<CheckFileRequest><Args action="9" sourceIP="10.26.1.150" sourceID="2" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="><Cluster id="00505692f33595217c6ab005f128c9b4c9f9" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/></Args></CheckFileRequest>`

func TestIsCheckFileRequest_RealOneFSPayload(t *testing.T) {
	// Guard the fixture against the measured length before relying on it:
	// Content-Length on the wire was 229.
	if len(checkFileRequestOneFS) != 229 {
		t.Fatalf("fixture is %d bytes, want the 229 measured on the wire", len(checkFileRequestOneFS))
	}

	if !IsCheckFileRequest([]byte(checkFileRequestOneFS)) {
		t.Error("IsCheckFileRequest() = false for the captured OneFS heartbeat, want true")
	}

	// The two handshakes must not be confused for one another. Routing a
	// CheckFileRequest down the RegisterRequest branch would answer it with an
	// empty body, which is precisely the failure this detection exists to fix.
	if IsRegisterRequest([]byte(checkFileRequestOneFS)) {
		t.Error("IsRegisterRequest() = true for a CheckFileRequest, want false")
	}
	if IsCheckFileRequest(registerRequestUTF16LE) {
		t.Error("IsCheckFileRequest() = true for a RegisterRequest, want false")
	}
}

// checkFileEventOneFS is a real NFS file-create event captured on the wire
// from the same cluster (2026-08-12), produced by `touch` on an NFSv4.2 mount.
// It shares the CheckFileRequest root with the heartbeat and differs only in
// Args/@action — which is exactly why the action has to be inspected.
//
// The name attribute decodes (base64 of UTF-16LE) to the UNC path
// \\powerscale1.diab.local\onefs$\ifs\test\evtest-1786563822.txt
const checkFileEventOneFS = `<CheckFileRequest><Args action="11" sourceIP="10.26.1.150" sourceID="2" name="XABcAHAAbwB3AGUAcgBzAGMAYQBsAGUAMQAuAGQAaQBhAGIALgBsAG8AYwBhAGwAXABvAG4AZQBmAHMAJABcAGkAZgBzAFwAdABlAHMAdABcAGUAdgB0AGUAcwB0AC0AMQA3ADgANgA1ADYAMwA4ADIAMgAuAHQAeAB0AA==" protocol="1"><Cluster id="00505692f33595217c6ab005f128c9b4c9f9" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/><Zone id="1" name="UwB5AHMAdABlAG0A"/></Args><NFSEventArgs eventType="8" desiredAccess="0x100106" createDispo="0x3" userSid="S-1-22-1-1000" clientIP="10.26.1.222" serverName="cABvAHcAZQByAHMAYwBhAGwAZQAxAC4AZABpAGEAYgAuAGwAbwBjAGEAbAA=" ntStatus="0x0" userId="1000" timeStamp="1786563708" timeStampMicroSeconds="859323" inode="4295432746" fsId="1"/></CheckFileRequest>`

func TestCheckFileAction_DistinguishesEventFromHeartbeat(t *testing.T) {
	// Both are CheckFileRequests…
	if !IsCheckFileRequest([]byte(checkFileRequestOneFS)) {
		t.Fatal("heartbeat fixture is not recognised as a CheckFileRequest")
	}
	if !IsCheckFileRequest([]byte(checkFileEventOneFS)) {
		t.Fatal("event fixture is not recognised as a CheckFileRequest")
	}

	// …so only the action tells them apart. Getting this wrong means events
	// are acknowledged as heartbeats and silently lost, because the ACK
	// advances the cluster's forwarding cursor.
	if got := CheckFileAction([]byte(checkFileRequestOneFS)); got != OneFSHeartbeatAction {
		t.Errorf("heartbeat action = %q, want %q", got, OneFSHeartbeatAction)
	}
	if got := CheckFileAction([]byte(checkFileEventOneFS)); got == OneFSHeartbeatAction {
		t.Errorf("event action = %q, which would be treated as a heartbeat and dropped", got)
	}
	if got := CheckFileAction([]byte(checkFileEventOneFS)); got != "11" {
		t.Errorf("event action = %q, want %q as measured on the wire", got, "11")
	}
}

func TestCheckFileAction_Malformed(t *testing.T) {
	for _, tt := range []struct{ name, input string }{
		{"empty", ""},
		{"not_xml", "<not-well-formed"},
		{"no_args", `<CheckFileRequest/>`},
		{"no_action_attr", `<CheckFileRequest><Args sourceID="2"/></CheckFileRequest>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckFileAction([]byte(tt.input)); got != "" {
				t.Errorf("CheckFileAction(%q) = %q, want empty", tt.input, got)
			}
		})
	}
}

func TestIsCheckFileRequest(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "bare_element",
			input: `<CheckFileRequest/>`,
			want:  true,
		},
		{
			name:  "leading_whitespace",
			input: "  \n\t<CheckFileRequest><Args action=\"9\"/></CheckFileRequest>",
			want:  true,
		},
		{
			name:  "with_xml_declaration",
			input: `<?xml version="1.0" encoding="utf-8"?><CheckFileRequest/>`,
			want:  true,
		},
		{
			name:  "event_payload",
			input: `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent>`,
			want:  false,
		},
		{
			// The word appearing inside an event must not be mistaken for the
			// root element — a file path is attacker-influenced input.
			name:  "name_only_in_content",
			input: `<CEEEvent><FilePath>/x/<CheckFileRequest.txt</FilePath></CEEEvent>`,
			want:  false,
		},
		{
			name:  "response_not_request",
			input: `<CheckFileResponse status="0x0"/>`,
			want:  false,
		},
		{
			// A longer element that merely starts with the name is a
			// different element. Answering it with the captured
			// CheckFileResponse would be replying to a dialect never seen.
			name:  "longer_element_sharing_the_prefix",
			input: `<CheckFileRequestV2><Args action="9"/></CheckFileRequestV2>`,
			want:  false,
		},
		{
			name:  "unterminated_element",
			input: `<CheckFileRequest`,
			want:  false,
		},
		{
			// Truncated after the name: the start tag is never closed, so
			// nothing says the rest of it would have been a CheckFileRequest.
			name:  "truncated_after_name",
			input: `<CheckFileRequest `,
			want:  false,
		},
		{
			name:  "truncated_mid_attribute",
			input: `<CheckFileRequest sourceIP="10.26.1.1`,
			want:  false,
		},
		{
			name:  "empty",
			input: ``,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCheckFileRequest([]byte(tt.input)); got != tt.want {
				t.Errorf("IsCheckFileRequest(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestOneFSEventCarriesProtocolAndServer: CEPAEvent.Protocol documents itself
// as "never empty … so it cannot merge with a missing value when used as a
// metric label", and it is used as exactly that in cee_events_by_type_total.
// Only the CEE converter honoured it — OneFS parsed `protocol` off the wire and
// dropped it, so every OneFS event emitted protocol="", the artefact the
// invariant exists to prevent.
//
// serverName has the same shape: parsed, unused, and the sole source for
// cee_events_by_server_total, which was therefore empty on a PowerScale estate
// while its help text advertises it as the way to see a NAS going quiet.
func TestOneFSEventCarriesProtocolAndServer(t *testing.T) {
	evs, err := ParseOneFSEvent([]byte(checkFileCloseOneFS), time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("no events parsed")
	}
	if got := evs[0].Protocol; got == "" {
		t.Error("Protocol is empty; it is a metric label and must never be")
	}
	if got := evs[0].Server; got == "" {
		t.Error("Server is empty; cee_events_by_server_total has no other source")
	}
}
