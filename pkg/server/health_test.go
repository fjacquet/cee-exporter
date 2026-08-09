package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

func TestHealth_MethodNotAllowed(t *testing.T) {
	h := NewHealthHandler(HealthConfig{StartTime: time.Now()})
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHealth_OKWithBaseline(t *testing.T) {
	metrics.M.EventsReceivedTotal.Store(0)
	metrics.M.EventsDroppedTotal.Store(0)
	metrics.M.EventsWrittenTotal.Store(0)
	metrics.M.SetQueueDepth(5)

	h := NewHealthHandler(HealthConfig{
		StartTime:  time.Now().Add(-90 * time.Second),
		WriterType: "gelf",
		WriterAddr: "graylog.local:12201",
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf("status: got %v", got["status"])
	}
	if got["queue_depth"].(float64) != 5 {
		t.Errorf("queue_depth: got %v", got["queue_depth"])
	}
	wr := got["writer"].(map[string]any)
	if wr["type"] != "gelf" || wr["target"] != "graylog.local:12201" {
		t.Errorf("writer block: got %+v", wr)
	}
}

func TestHealth_DegradedWhenDropped(t *testing.T) {
	metrics.M.EventsDroppedTotal.Store(7)
	t.Cleanup(func() { metrics.M.EventsDroppedTotal.Store(0) })

	h := NewHealthHandler(HealthConfig{StartTime: time.Now()})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["status"] != "degraded" {
		t.Errorf("expected degraded status when dropped>0; got %v", got["status"])
	}
}

func TestHealth_TLSCertExpiryPopulated(t *testing.T) {
	certPath := writeTempCert(t, 400*24*time.Hour)
	h := NewHealthHandler(HealthConfig{
		StartTime:   time.Now(),
		TLSEnabled:  true,
		TLSCertFile: certPath,
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	tls := got["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Errorf("enabled should be true; got %v", tls["enabled"])
	}
	if tls["days_remaining"] == nil {
		t.Error("days_remaining should be populated when cert is readable")
	}
}

// TestHealth_TLSDaysRemainingSurvivesLastDay pins the one input that an
// `omitempty` tag on DaysRemaining would silently swallow: a certificate
// inside its final 24 hours, where int(hours/24) truncates to 0. That is the
// moment a monitor most needs the field, and it is the only moment the tag
// would delete it — TestHealth_TLSCertExpiryPopulated uses a 400-day cert and
// passes either way.
func TestHealth_TLSDaysRemainingSurvivesLastDay(t *testing.T) {
	certPath := writeTempCert(t, 12*time.Hour)
	h := NewHealthHandler(HealthConfig{
		StartTime:   time.Now(),
		TLSEnabled:  true,
		TLSCertFile: certPath,
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tlsBlock := got["tls"].(map[string]any)

	days, present := tlsBlock["days_remaining"]
	if !present {
		t.Fatal("days_remaining absent for a cert expiring in 12h; " +
			"absence must mean \"no certificate\", not \"expires today\"")
	}
	if days.(float64) != 0 {
		t.Errorf("days_remaining: got %v, want 0 for a 12h cert", days)
	}
	if tlsBlock["cert_expiry"] == nil {
		t.Error("cert_expiry should accompany days_remaining")
	}
}

// TestHealth_TLSDaysRemainingAbsentWhenNoCert holds the other side of the
// contract. Simply dropping omitempty from an `int` field would satisfy the
// test above while emitting `days_remaining: 0` on every plaintext
// deployment — indistinguishable from "expires today". Only nil-vs-0 encodes
// both facts, so both tests must pass together.
func TestHealth_TLSDaysRemainingAbsentWhenNoCert(t *testing.T) {
	h := NewHealthHandler(HealthConfig{StartTime: time.Now()})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tlsBlock := got["tls"].(map[string]any)

	if tlsBlock["enabled"] != false {
		t.Errorf("enabled: got %v, want false", tlsBlock["enabled"])
	}
	if _, present := tlsBlock["cert_expiry"]; present {
		t.Error("cert_expiry must be absent when TLS is off")
	}
	if days, present := tlsBlock["days_remaining"]; present {
		t.Errorf("days_remaining present (%v) with no certificate; "+
			"0 here is indistinguishable from \"expires today\"", days)
	}
}

// writeTempCert generates a self-signed ECDSA cert valid for `d` and returns
// the PEM file path.
func writeTempCert(t *testing.T, d time.Duration) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cee-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(d),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cert.pem")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("pem: %v", err)
	}
	return path
}
