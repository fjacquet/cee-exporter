package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// checkFileEventOneFS is a real action="11" file event, captured on the wire
// from a 4-node OneFS 9.13.0.0 cluster on 2026-08-14 — the create half of what
// a single `touch` over NFSv4 produced.
const checkFileEventOneFS = `<CheckFileRequest><Args action="11" sourceIP="10.26.1.150" sourceID="2" name="XABcAHAAbwB3AGUAcgBzAGMAYQBsAGUAMQAtADEAXABvAG4AZQBmAHMAJABcAGkAZgBzAFwAdABlAHMAdABcAGUAdgB0AGUAcwB0AC0AMQA3ADgANgA3ADMANQAwADAAMgAuAHQAeAB0AA==" protocol="1"><Cluster id="00505692f33595217c6ab005f128c9b4c9f9" name="cABvAHcAZQByAHMAYwBhAGwAZQAxAA=="/><Zone id="1" name="UwB5AHMAdABlAG0A"/></Args><NFSEventArgs eventType="8" desiredAccess="0x100106" createDispo="0x3" userSid="S-1-22-1-0" clientIP="10.26.1.222" serverName="cABvAHcAZQByAHMAYwBhAGwAZQAxAC0AMQA=" ntStatus="0x0" userId="0" timeStamp="1786734886" timeStampMicroSeconds="1593" inode="4295174245" fsId="1"/></CheckFileRequest>`

// TestServeHTTP_OneFSEvent_ReachesTheWriter is the end-to-end guard for the
// path that used to stop at a WARN log. Two things must hold together, and
// neither is sufficient alone:
//
//   - the event is written, because the CheckFileResponse this handler already
//     sent advances the cluster's forwarding cursor — an event not written here
//     exists nowhere else
//   - the reply is still a well-formed CheckFileResponse, because OneFS treats
//     an empty or unparseable body as STATUS_DATA_ERROR and stops publishing
func TestServeHTTP_OneFSEvent_ReachesTheWriter(t *testing.T) {
	resetPeers(t)

	done := make(chan struct{}, 1)
	w := &stubWriter{done: done}
	h := newTestHandler(t, w, 10, 1)

	before := metrics.M.EventsReceivedTotal.Load()

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkFileEventOneFS))
	req.RemoteAddr = "10.26.1.150:53139"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "<CheckFileResponse") {
		t.Fatalf("an event got no CheckFileResponse back; OneFS would report STATUS_DATA_ERROR. body = %q", rec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event never reached the writer")
	}

	if got := metrics.M.EventsReceivedTotal.Load(); got != before+1 {
		t.Errorf("EventsReceivedTotal = %d, want %d", got, before+1)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.events) != 1 {
		t.Fatalf("writer got %d events, want 1", len(w.events))
	}
	got := w.events[0]

	// eventType 8 is the open/create, which the mapper files as a WriteData
	// access. A close (256) would be 4658 instead — the distinction only
	// survives if the numeric attribute was read rather than the action.
	if got.EventID != 4663 {
		t.Errorf("EventID = %d, want 4663 for a create", got.EventID)
	}
	if want := `\\powerscale1-1\onefs$\ifs\test\evtest-1786735002.txt`; got.ObjectName != want {
		t.Errorf("ObjectName = %q, want %q", got.ObjectName, want)
	}
	if want := "CEPP_CREATE_FILE"; got.CEPAEventType != want {
		t.Errorf("CEPAEventType = %q, want %q", got.CEPAEventType, want)
	}
	if want := "10.26.1.222"; got.ClientAddr != want {
		t.Errorf("ClientAddr = %q, want %q — the NFS client, not the forwarding node", got.ClientAddr, want)
	}
}

// TestServeHTTP_OneFSEvent_NotCountedAsHeartbeat guards the other direction of
// the routing. An event must not stamp the peer's *registration*: a cluster
// that has stopped heartbeating but is still draining a backlog would
// otherwise read as healthy.
func TestServeHTTP_OneFSEvent_NotCountedAsHeartbeat(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(checkFileEventOneFS))
	req.RemoteAddr = "10.26.1.150:53139"
	h.ServeHTTP(httptest.NewRecorder(), req)

	snap := metrics.M.PeerSnapshot()
	p, ok := snap["10.26.1.150"]
	if !ok {
		t.Fatal("peer not stamped at all; a publisher sending events is alive")
	}
	if p.Registrations != 0 {
		t.Errorf("Registrations = %d, want 0 — an event is not a heartbeat", p.Registrations)
	}
}
