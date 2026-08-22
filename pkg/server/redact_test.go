package server

import (
	"strings"
	"testing"
)

// TestRedactPayload_DropsValuesKeepsShape pins both halves of the contract. A
// diagnostic that keeps no shape cannot identify a malformed payload; one that
// keeps values republishes the audit record into the log stream.
func TestRedactPayload_DropsValuesKeepsShape(t *testing.T) {
	// A real OneFS event, values and all: UNC path, client IP, SID, username.
	const body = `<CheckFileRequest><Args action="11" ` +
		`name="\\powerscale1-1\onefs$\ifs\test\payroll-2026.xlsx" ` +
		`sourceIP="10.26.1.150"/>` +
		`<NFSEventArgs eventType="8" userSid="S-1-5-21-1-2-3-1001" ` +
		`clientIP="10.26.1.222"/>secret-text</CheckFileRequest>`

	got := redactPayload([]byte(body))

	// Every value must be gone.
	for _, leaked := range []string{
		`payroll-2026.xlsx`, `powerscale1-1`, `onefs$`,
		`10.26.1.150`, `10.26.1.222`,
		`S-1-5-21-1-2-3-1001`,
		`secret-text`,
		`"11"`, `"8"`,
	} {
		if strings.Contains(got, leaked) {
			t.Errorf("redacted payload leaked %q:\n%s", leaked, got)
		}
	}

	// Every name must remain.
	for _, kept := range []string{
		"<CheckFileRequest>", "<Args", "action=", "name=", "sourceIP=",
		"<NFSEventArgs", "eventType=", "userSid=", "clientIP=",
		"</CheckFileRequest>",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("redacted payload dropped %q, which is what diagnoses the failure:\n%s", kept, got)
		}
	}
}

// TestRedactPayload_Bounded: a CEPA body may be 64 MiB and a VCAPS batch holds
// thousands of events. The diagnostic must not carry that into the log.
func TestRedactPayload_Bounded(t *testing.T) {
	huge := "<EventBatch>" + strings.Repeat(`<CEEEvent path="/x/y/z"/>`, 10000) + "</EventBatch>"

	got := redactPayload([]byte(huge))

	if len(got) > maxRedactedPayload+len("...(truncated)") {
		t.Errorf("redacted payload is %d bytes, want <= %d", len(got), maxRedactedPayload+len("...(truncated)"))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Errorf("a truncated diagnostic must say so, got tail %q", got[max(0, len(got)-30):])
	}
	if strings.Contains(got, "/x/y/z") {
		t.Errorf("truncation must not bypass redaction:\n%s", got)
	}
}

// TestRedactPayload_Malformed: this runs only on payloads that already failed
// to parse, so unterminated quotes and tags are the expected input, not the
// edge case. It must not hang or panic on them.
func TestRedactPayload_Malformed(t *testing.T) {
	for _, body := range []string{
		``,
		`<`,
		`<Args name="unterminated`,
		`<Args name='single'/>`,
		`no tags at all`,
		`<Args>>><<<`,
		"\x00\xff<Args/>",
	} {
		got := redactPayload([]byte(body))
		if strings.Contains(got, "unterminated") || strings.Contains(got, "single") {
			t.Errorf("redactPayload(%q) leaked a value: %q", body, got)
		}
	}
}

// TestPayloadDigest_StableAndDistinct: the digest exists to tell one failure
// retried from a burst of different ones.
func TestPayloadDigest_StableAndDistinct(t *testing.T) {
	a, b := []byte(`<CheckFileRequest/>`), []byte(`<CheckEventRequest/>`)

	// Two separate calls, via variables: staticcheck rejects the same
	// expression on both sides of !=, and the point here is call stability.
	first, second := payloadDigest(a), payloadDigest(append([]byte(nil), a...))
	if first != second {
		t.Errorf("digest is not stable for identical payloads: %q vs %q", first, second)
	}
	if payloadDigest(a) == payloadDigest(b) {
		t.Error("digest does not distinguish different payloads")
	}
	if got := payloadDigest(a); len(got) != 16 {
		t.Errorf("digest = %q (%d chars), want 16 hex chars", got, len(got))
	}
}
