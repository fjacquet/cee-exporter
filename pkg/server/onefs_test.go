package server

import (
	"bytes"
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// checkFileRequestOneFS is a OneFS heartbeat reduced to the parts this layer
// routes on: the root element and Args/@action. Wire fidelity is proved once,
// against the byte-exact 229-byte capture in pkg/parser/onefs_test.go — these
// tests cover routing and response shape, so duplicating the capture across
// packages would only give two copies to keep in sync. Same split as the
// RegisterRequest tests in server_test.go, which use a bare literal while
// pkg/parser holds the captured UTF-16LE bytes.
const checkFileRequestOneFS = `<CheckFileRequest><Args action="9" sourceIP="10.26.1.150" sourceID="2"/></CheckFileRequest>`

// TestServeHTTP_CheckFileRequest_AnswersWithResponse is the mirror image of
// TestServeHTTP_RegisterRequest_EmptyBody, and the two must not drift into
// agreement. PowerStore requires an empty body and treats XML as fatal; OneFS
// requires a CheckFileResponse and treats an empty body as fatal — it logs
// "Error while parsing CEE CheckFileResponse", then STATUS_DATA_ERROR, and
// stops publishing altogether. A regression that answered OneFS with an empty
// body would be invisible in production except as total silence.
func TestServeHTTP_CheckFileRequest_AnswersWithResponse(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkFileRequestOneFS))
	req.RemoteAddr = "10.26.1.150:42256"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("response body is empty; OneFS treats that as STATUS_DATA_ERROR and stops publishing")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/xml" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/xml")
	}

	// The cluster parses this, so it has to be well-formed XML — not merely
	// non-empty.
	var probe struct {
		XMLName xml.Name `xml:"CheckFileResponse"`
		Status  string   `xml:"status,attr"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatalf("response is not well-formed CheckFileResponse XML: %v\nbody: %s", err, rec.Body.String())
	}

	// The status attribute is the vcstatus the cluster reports back. Measured:
	// 0x1 surfaced as VC_ERROR_SETUP and 0x16 as VC_ERROR_CEPP_NOT_FOUND, so
	// any non-zero value here is an error as far as OneFS is concerned.
	if probe.Status != "0x0" {
		t.Errorf("status = %q, want %q — a non-zero status is reported by OneFS as a vcstatus error", probe.Status, "0x0")
	}
}

// TestServeHTTP_CheckFileRequest_NotTreatedAsEvent guards the routing: the
// handshake must never reach the event path. Before it was recognised it fell
// through to Parse, which rejected it as an unrecognised payload — the
// original bug.
func TestServeHTTP_CheckFileRequest_NotTreatedAsEvent(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	before := metrics.M.EventsReceivedTotal.Load()

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkFileRequestOneFS))
	req.RemoteAddr = "10.26.1.150:42256"
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := metrics.M.EventsReceivedTotal.Load(); got != before {
		t.Errorf("EventsReceivedTotal moved from %d to %d; a heartbeat was counted as an event", before, got)
	}

	// It is a publisher heartbeat, so it must stamp the peer — otherwise a
	// cluster that is heartbeating normally reads as dead on the dashboard.
	snap := metrics.M.PeerSnapshot()
	if _, ok := snap["10.26.1.150"]; !ok {
		t.Fatalf("peer 10.26.1.150 not stamped by a CheckFileRequest; snapshot = %v", snap)
	}
	if snap["10.26.1.150"].LastRequestUnix == 0 {
		t.Error("LastRequestUnix = 0 for a heartbeating cluster")
	}
}

// TestServeHTTP_CheckFileEvent_AckedAndParsed covers the event branch.
//
// This test asserted the opposite until the CEE registration work landed: an
// event was answered and then DISCARDED, because nothing decoded it yet, and
// the drop counter was the only alertable signal. OneFS events are parsed and
// written now, so the same payload must move EventsReceivedTotal and leave
// EventsDroppedTotal alone. The ACK still has to be a CheckFileResponse --
// that half never changed, because an empty body is fatal for OneFS whatever
// the action was.
//
// The end-to-end assertion that the event reaches the writer lives in
// onefs_event_test.go; this one guards the counters and the reply.
func TestServeHTTP_CheckFileEvent_AckedAndParsed(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	beforeDropped := metrics.M.EventsDroppedTotal.Load()
	beforeReceived := metrics.M.EventsReceivedTotal.Load()

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkFileEventOneFS))
	req.RemoteAddr = "10.26.1.150:42256"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var probe struct {
		XMLName xml.Name `xml:"CheckFileResponse"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatalf("event request was not answered with a CheckFileResponse: %v\nbody: %s", err, rec.Body.String())
	}

	if got := metrics.M.EventsReceivedTotal.Load(); got != beforeReceived+1 {
		t.Errorf("EventsReceivedTotal = %d, want %d — the event is parsed now, not discarded", got, beforeReceived+1)
	}
	if got := metrics.M.EventsDroppedTotal.Load(); got != beforeDropped {
		t.Errorf("EventsDroppedTotal moved from %d to %d; a parsed event is not a drop", beforeDropped, got)
	}

	// An event is not a handshake. Counting it as one would inflate the
	// per-publisher handshake series with file activity.
	if got := metrics.M.PeerSnapshot()["10.26.1.150"].Registrations; got != 0 {
		t.Errorf("Registrations = %d for an event request, want 0", got)
	}
}

// TestServeHTTP_UnparseableEvent_LoggingIsSampledAndRedacted asserts that a
// payload this build cannot decode stops rendering its structure after a
// handful of samples, while the drop count keeps rising.
//
// This used to fire on every OneFS event, because none of them could be
// decoded. They are parsed now, so the sampler only sees genuinely
// unparseable payloads -- an SMB event, say, whose element this build has
// never met. The reasoning is unchanged: OneFS runs this branch at cluster
// file-activity rate, the samples answer "what does this format look like",
// the counter answers "how much am I losing", and only the counter has to
// scale.
//
// The rendered structure is redacted (see redactPayload), so sampling and
// redaction are two independent limits on the same line: one bounds how often
// it appears, the other bounds what it may contain.
func TestServeHTTP_UnparseableEvent_LoggingIsSampledAndRedacted(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	// action="11" routes to the event parser; SMBEventArgs is an element this
	// build has no decoder for, so parsing fails and the sampler is reached.
	const unparseable = `<CheckFileRequest><Args action="11" ` +
		`name="XABcAHAAbwB3AGUAcgBzAGMAYQBsAGUAMQAtADEAXABvAG4AZQBmAHMAJABcAGkAZgBzAFwAdABlAHMAdABcAGUAdgB0AGUAcwB0AC0AMQA3ADgANgA3ADMANQAwADAAMgAuAHQAeAB0AA=="/>` +
		`<SMBEventArgs eventType="8" userSid="S-1-5-21-9-9-9-1001" clientIP="10.26.1.222"/></CheckFileRequest>`

	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	const sends = unhandledPayloadSamples + 5
	beforeDropped := metrics.M.EventsDroppedTotal.Load()
	for range sends {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(unparseable))
		req.RemoteAddr = "10.26.1.150:42256"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Every one is counted — sampling must never cost the operator the count.
	if got := metrics.M.EventsDroppedTotal.Load(); got != beforeDropped+sends {
		t.Errorf("EventsDroppedTotal = %d, want %d — sampling must not suppress the count", got, beforeDropped+sends)
	}

	// body_shape is the sampled field; it appears once per sample and then stops.
	if got := strings.Count(buf.String(), "SMBEventArgs"); got != unhandledPayloadSamples {
		t.Errorf("structure logged %d times for %d events, want %d samples", got, sends, unhandledPayloadSamples)
	}

	// Redaction holds on every one of those samples: the element name is what
	// identifies the format, the values are the audit record.
	for _, leaked := range []string{"S-1-5-21-9-9-9-1001", "10.26.1.222", `eventType="8"`} {
		if strings.Contains(buf.String(), leaked) {
			t.Errorf("sampled log line leaked %q", leaked)
		}
	}
}

// TestServeHTTP_UTF16EventPayload covers a UTF-16LE event end to end through
// ServeHTTP — the live CEE encoding, which had no handler-level test.
//
// It does NOT guard the single-decode refactor, and was checked by mutation:
// replacing Parse(decoded) with Parse(body) leaves this test green, because
// Parse decodes again on its own. That refactor is behaviour-neutral by
// construction — it only removes repeated transcodes — so nothing here can
// fail if it is undone. Only a benchmark or an allocation count would catch
// a regression, and neither exists.
func TestServeHTTP_UTF16EventPayload(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	const event = `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType><SrcPath>/ifs/x.txt</SrcPath></CEEEvent>`
	utf16le := make([]byte, 0, len(event)*2)
	for _, b := range []byte(event) {
		utf16le = append(utf16le, b, 0x00)
	}

	before := metrics.M.EventsReceivedTotal.Load()

	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(utf16le))
	req.RemoteAddr = "10.26.1.150:42256"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := metrics.M.EventsReceivedTotal.Load(); got != before+1 {
		t.Errorf("EventsReceivedTotal = %d, want %d — the UTF-16 event did not parse", got, before+1)
	}
}
