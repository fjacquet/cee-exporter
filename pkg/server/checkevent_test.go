package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// checkEventRequestCEE is Dell CEE's own event delivery, built from
// CCheckEventRequest::GetXmlRequest() in libCEPPAPIWrapper.so. Not a wire
// capture — see pkg/parser/checkevent_test.go for why, and replace it with one
// when an array first publishes.
const checkEventRequestCEE = `<CheckEventRequest><EventList count="1">` +
	`<Event event="0x00000008" path="\\NAS01\fs01\test\evtest.txt" flag="0x0" ` +
	`server="NAS01" share="fs01" clientIP="10.26.1.222" serverIP="10.26.1.224" ` +
	`timeStamp="1786735002" userSid="S-1-5-21-1-2-3-1001" ownerSid="S-1-5-21-1-2-3-513" ` +
	`fileSize="0x400" desiredAccess="0x100106" createDispo="0x3" ntStatus="0x0" ` +
	`relativePath="\test\evtest.txt"/>` +
	`</EventList></CheckEventRequest>`

// TestServeHTTP_CEEEvent_ReachesTheWriter is the end-to-end guard for the
// dialect CEE uses once it has a registered partner. Before the registration
// fix this path was unreachable — CEE never sends events to a consumer it
// could not register — so nothing here had ever run against real traffic.
func TestServeHTTP_CEEEvent_ReachesTheWriter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkEventRequestCEE))
	req.RemoteAddr = "10.26.1.199:51000"
	rec, got := sendAndAwaitWrite(t, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — CEE marks a partner that does not answer unavailable and stops publishing", rec.Code)
	}
	if want := `\\NAS01\fs01\test\evtest.txt`; got.ObjectName != want {
		t.Errorf("ObjectName = %q, want %q", got.ObjectName, want)
	}
}

// TestServeHTTP_CEEEvent_MalformedStillACKs: an unreadable payload must not
// cost the session. CEE marks a partner that fails to answer as unavailable
// and stops publishing to it entirely — losing every subsequent event, not
// just the one that could not be parsed. The payload is logged instead.
func TestServeHTTP_CEEEvent_MalformedStillACKs(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/",
		strings.NewReader(`<CheckEventRequest><EventList count="1"></EventList></CheckEventRequest>`))
	req.RemoteAddr = "10.26.1.199:51001"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even for an unreadable payload", rec.Code)
	}
}

// TestServeHTTP_CEEEvent_DoesNotAnswerWithCheckFileResponse guards the
// dialect boundary from the reply side. CheckEventRequest and OneFS's
// CheckFileRequest arrive on the same URL; answering one with the other's
// document is the failure mode that is easiest to introduce and hardest to
// see, because both look like a healthy 200 from the outside.
func TestServeHTTP_CEEEvent_DoesNotAnswerWithCheckFileResponse(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkEventRequestCEE))
	req.RemoteAddr = "10.26.1.199:51002"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "<CheckFileResponse") {
		t.Errorf("a CEE event was answered with a OneFS CheckFileResponse: %q", rec.Body.String())
	}
}

// TestServeHTTP_HeartBeatRequest_ReportsOnline covers the leg immediately
// after registration. It had never run against real traffic, because CEE only
// sends <HeartBeatRequest /> to a partner it managed to register — which,
// before the registration fix, was never.
//
// Answering it wrongly does not look like a failure: registration keeps
// succeeding and CEE simply stops publishing.
func TestServeHTTP_HeartBeatRequest_ReportsOnline(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`<HeartBeatRequest />`))
	req.RemoteAddr = "10.26.1.199:51003"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// 0 is CEPP_SERVICE_ONLINE, read from CEE's indexed name table. Any other
	// value reports this consumer as offline or unregistered.
	if !strings.Contains(body, "hbStatus=0") {
		t.Errorf("reply %q carries no hbStatus=0; CEE would not consider this consumer online", body)
	}
	if !strings.Contains(body, "ntStatus=0") {
		t.Errorf("reply %q carries no ntStatus=0", body)
	}
	// The parse-error path returns an empty body, so this distinguishes
	// "handled" from "fell through to the event parser".
	if body == "" {
		t.Error("empty reply: the heartbeat fell through to the event parser")
	}
}
