package server

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fjacquet/cee-exporter/pkg/parser"
)

// powerStoreHeartbeatXML is the CEPA heartbeat a PowerStore NAS server sends,
// captured on the wire from NAS01 (10.26.1.224) on 2026-08-14. It goes out as
// `POST /vee` with `User-Agent: EMC Data Mover` and a UTF-16LE body — three
// details that each differ from OneFS, which sends `PUT /` in UTF-8.
const powerStoreHeartbeatXML = `<CheckFileRequest>` +
	`<Args action="9" name="XABcAG4AYQBzADAAMQAuAGQAaQBhAGIALgBsAG8AYwBhAGwAXABDAEgARQBDAEsAJAA=" ` +
	`id="1786527819" celerraIP="10.26.1.224" sourceID="10" sourceDescr="Trident" type="0" protocol="0"/>` +
	`<CEPPHeartBeatArgs preEventsMissed="0" postSuccessEventsMissed="0" postFailureEventsMissed="0">` +
	`<CEPPServerList><CEPPServer ip="10.26.1.199"/></CEPPServerList>` +
	`<CIFSServerList><CIFSServer netbios="NAS01" domain="DIAB" fqdn="nas01.diab.local" realm="DIAB.LOCAL"/>` +
	`</CIFSServerList></CEPPHeartBeatArgs></CheckFileRequest>`

func utf16le(s string) []byte { return parser.EncodeUTF16LE([]byte(s)) }

// TestServeHTTP_AcceptsPowerStorePost is the regression guard for the 405 that
// made a direct PowerStore→consumer path impossible. The NAS server publishes
// with POST; this handler accepted only PUT, so every heartbeat was refused
// before the body was ever read.
func TestServeHTTP_AcceptsPowerStorePost(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPost, "/vee", bytes.NewReader(utf16le(powerStoreHeartbeatXML)))
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("User-Agent", "EMC Data Mover")
	req.RemoteAddr = "10.26.1.224:51000"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — PowerStore publishes with POST, not PUT", rec.Code)
	}
}

// TestServeHTTP_PowerStoreGetsUTF16Response pins the reply encoding. PowerStore
// speaks UTF-16LE and Dell CEE answers it in UTF-16LE; a UTF-8 reply is not
// something the Data Mover can parse, so the CEPP session would never
// establish and the array would report its publishing pools unavailable while
// the HTTP layer looked healthy.
func TestServeHTTP_PowerStoreGetsUTF16Response(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPost, "/vee", bytes.NewReader(utf16le(powerStoreHeartbeatXML)))
	req.RemoteAddr = "10.26.1.224:51001"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("empty reply; OneFS and PowerStore both treat that as fatal for a CheckFileRequest")
	}
	// A UTF-16LE document of ASCII content has a NUL as every second byte.
	// Asserting on the bytes rather than on the decoded string is the point:
	// the encoding is the thing under test.
	if !bytes.Contains(body, []byte{0x00}) {
		t.Fatalf("reply carries no NUL bytes, so it is not UTF-16LE: %q", body[:min(64, len(body))])
	}

	decoded := bytes.ReplaceAll(body, []byte{0x00}, nil)
	var probe struct {
		XMLName xml.Name `xml:"CheckFileResponse"`
		Status  string   `xml:"status,attr"`
	}
	if err := xml.Unmarshal(decoded, &probe); err != nil {
		t.Fatalf("reply is not a well-formed CheckFileResponse: %v\nbody: %s", err, decoded)
	}
	if probe.Status != "0x0" {
		t.Errorf("status = %q, want %q — any non-zero value is an error to the publisher", probe.Status, "0x0")
	}
}

// TestServeHTTP_OneFSKeepsUTF8Response is the other half: mirroring the
// request encoding must not regress the OneFS path, which sends plain UTF-8
// and is answered in UTF-8. Encoding both alike would break whichever one
// lost.
func TestServeHTTP_OneFSKeepsUTF8Response(t *testing.T) {
	resetPeers(t)
	h := newTestHandler(t, &stubWriter{}, 10, 1)

	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader([]byte(checkFileRequestOneFS)))
	req.RemoteAddr = "10.26.1.150:42256"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	body := rec.Body.Bytes()
	if bytes.Contains(body, []byte{0x00}) {
		t.Fatalf("OneFS reply contains NUL bytes, so it was encoded as UTF-16: %q", body[:min(64, len(body))])
	}
	if !bytes.Contains(body, []byte("<CheckFileResponse")) {
		t.Fatalf("OneFS reply is not a CheckFileResponse: %q", body)
	}
}
