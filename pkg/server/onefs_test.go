package server

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// checkFileRequestOneFS is the heartbeat body captured on the wire from an
// OneFS 9.13.0.0 cluster (tcpdump, 2026-08-12), byte for byte.
const checkFileRequestOneFS = `<CheckFileRequest><Args action="9" sourceIP="10.26.1.150" sourceID="2" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="><Cluster id="00505692f33595217c6ab005f128c9b4c9f9" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/></Args></CheckFileRequest>`

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
