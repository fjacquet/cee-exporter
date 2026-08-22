package ceeprometheus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// TestMetricsHandler_AllRequiredMetrics verifies that all four required metric
// names appear in the Prometheus scrape output with the expected values, and
// that Go runtime metrics are absent (private registry check).
func TestMetricsHandler_AllRequiredMetrics(t *testing.T) {
	// Reset global metrics to a known baseline before seeding.
	metrics.M.EventsReceivedTotal.Store(0)
	metrics.M.EventsDroppedTotal.Store(0)
	metrics.M.WriterErrorsTotal.Store(0)
	metrics.M.EventsWrittenTotal.Store(0)
	metrics.M.EventsTruncatedTotal.Store(0)
	metrics.M.SetQueueDepth(0)

	// Seed known values.
	metrics.M.EventsReceivedTotal.Store(42)
	metrics.M.EventsDroppedTotal.Store(7)
	metrics.M.WriterErrorsTotal.Store(1)
	metrics.M.SetQueueDepth(15)
	metrics.M.EventsWrittenTotal.Store(30)
	metrics.M.EventsTruncatedTotal.Store(3)
	metrics.M.RecordFsyncAt(time.Unix(1700000000, 0))

	h := NewMetricsHandler()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	body := w.Body.String()

	// Assert all required metric lines appear with correct values.
	required := []string{
		"cee_events_received_total 42",
		"cee_events_dropped_total 7",
		"cee_writer_errors_total 1",
		"cee_queue_depth 15",
		"cee_events_written_total 30",
		"cee_events_truncated_total 3",
	}

	// Assert the fsync gauge metric name appears.
	if !strings.Contains(body, "cee_last_fsync_unix_seconds") {
		t.Errorf("expected cee_last_fsync_unix_seconds in scrape output\nBody:\n%s", body)
	}

	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in scrape output, but not found\nBody:\n%s", want, body)
		}
	}

	// Sanity-check: private registry must not include Go runtime metrics.
	if strings.Contains(body, "go_gc_") {
		t.Error("unexpected Go runtime metric in output; handler must use a private registry")
	}
}

// TestBuildInfoMetric verifies cee_build_info is exposed with version labels,
// so the running version is visible to Prometheus and not only in the logs.
func TestBuildInfoMetric(t *testing.T) {
	h := NewMetricsHandlerWithBuildInfo("v4.1.3-test", "go1.26.5")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "cee_build_info") {
		t.Fatalf("cee_build_info missing from scrape output:\n%s", body)
	}
	if !strings.Contains(body, `version="v4.1.3-test"`) {
		t.Errorf("version label missing from cee_build_info:\n%s", body)
	}
	if !strings.Contains(body, `go_version="go1.26.5"`) {
		t.Errorf("go_version label missing from cee_build_info:\n%s", body)
	}
}

// TestMetricsHandler_CEPAPeerMetrics verifies the per-publisher series are
// scrapeable with their remote label, for more than one publisher — a single
// aggregate would stay green while one of three publishers went dark, which
// is the case these metrics exist to catch.
func TestMetricsHandler_CEPAPeerMetrics(t *testing.T) {
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)

	metrics.M.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))
	metrics.M.RecordPeerRegistration("10.0.2.250")
	metrics.M.RecordPeerRegistration("10.0.2.250")
	metrics.M.RecordPeerRequestAt("10.0.2.251", time.Unix(1_700_000_030, 0))

	h := NewMetricsHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	required := []string{
		`cee_cepa_last_request_unix_seconds{remote="10.0.2.250"} 1.7e+09`,
		`cee_cepa_registrations_total{remote="10.0.2.250"} 2`,
		`cee_cepa_last_request_unix_seconds{remote="10.0.2.251"} 1.70000003e+09`,
		`cee_cepa_registrations_total{remote="10.0.2.251"} 0`,
		"cee_cepa_peers_dropped_total 0",
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in scrape output, not found\nBody:\n%s", want, body)
		}
	}
}

// TestMetricsHandler_CEPAPeerMetricsEmpty confirms no peers means no series —
// in particular no remote="" artefact, which would alert as a dead publisher
// that never existed.
func TestMetricsHandler_CEPAPeerMetricsEmpty(t *testing.T) {
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)

	h := NewMetricsHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()

	if strings.Contains(body, `cee_cepa_last_request_unix_seconds{`) {
		t.Errorf("expected no cee_cepa_last_request_unix_seconds series with no peers\nBody:\n%s", body)
	}
	if strings.Contains(body, `remote=""`) {
		t.Errorf("empty remote label in scrape output\nBody:\n%s", body)
	}
	// The label-free drop counter is always present.
	if !strings.Contains(body, "cee_cepa_peers_dropped_total") {
		t.Errorf("cee_cepa_peers_dropped_total missing from scrape output\nBody:\n%s", body)
	}
}

// TestMetricsHandler_CEPAHeartbeatSeries: heartbeats were previously folded
// into cee_cepa_registrations_total, so splitting them out is only half the
// fix — the series has to reach the scrape, or a publisher that heartbeats
// healthily but never registers now shows nothing at all where it used to show
// a (wrong) registration rate.
func TestMetricsHandler_CEPAHeartbeatSeries(t *testing.T) {
	metrics.M.ResetPeers()
	t.Cleanup(metrics.M.ResetPeers)

	metrics.M.RecordPeerRequestAt("10.26.1.199", time.Unix(1_700_000_000, 0))
	metrics.M.RecordPeerHeartbeat("10.26.1.199")
	metrics.M.RecordPeerHeartbeat("10.26.1.199")

	metrics.M.RecordPeerRequestAt("10.26.1.225", time.Unix(1_700_000_000, 0))
	metrics.M.RecordPeerRegistration("10.26.1.225")

	h := NewMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	required := []string{
		`cee_cepa_heartbeats_total{remote="10.26.1.199"} 2`,
		`cee_cepa_registrations_total{remote="10.26.1.199"} 0`,
		`cee_cepa_heartbeats_total{remote="10.26.1.225"} 0`,
		`cee_cepa_registrations_total{remote="10.26.1.225"} 1`,
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in scrape output, not found\nBody:\n%s", want, body)
		}
	}
}

// TestMetricsHandler_EventBreakdown: the event counters were scalar, so the
// whole estate collapsed into one number. Nothing could say what kind of
// activity was happening or which NAS server it happened on — and on this
// deployment that mattered, because CEE replays a single CREATE_FILE and a
// scalar counter renders that identically to real, varied traffic.
//
// Two metrics rather than one with three labels: multiplying event_type by
// protocol by server would be 20 x 4 x N series, and the two questions ("what
// is happening" and "where") are asked separately anyway.
func TestMetricsHandler_EventBreakdown(t *testing.T) {
	metrics.M.ResetEventBreakdown()
	t.Cleanup(metrics.M.ResetEventBreakdown)

	metrics.M.RecordEvent("CEPP_CREATE_FILE", "NFS", "10.26.1.224")
	metrics.M.RecordEvent("CEPP_CREATE_FILE", "NFS", "10.26.1.224")
	metrics.M.RecordEvent("CEPP_FILE_WRITE", "CIFS", "NAS02")

	h := NewMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`cee_events_by_type_total{event_type="CEPP_CREATE_FILE",protocol="NFS"} 2`,
		`cee_events_by_type_total{event_type="CEPP_FILE_WRITE",protocol="CIFS"} 1`,
		`cee_events_by_server_total{server="10.26.1.224"} 2`,
		`cee_events_by_server_total{server="NAS02"} 1`,
		"cee_event_labels_dropped_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in scrape output, not found\nBody:\n%s", want, body)
		}
	}
}

// TestMetricsHandler_EventBreakdownCapped: an unbounded label is how an
// exporter takes down its own Prometheus. The server label is bounded by the
// estate in practice, but "in practice" is not a guarantee — so the cap is
// enforced and its being hit is visible, exactly as MaxPeers already is for the
// publisher label.
func TestMetricsHandler_EventBreakdownCapped(t *testing.T) {
	metrics.M.ResetEventBreakdown()
	t.Cleanup(metrics.M.ResetEventBreakdown)

	for i := 0; i < metrics.MaxEventLabels+10; i++ {
		metrics.M.RecordEvent("CEPP_CREATE_FILE", "NFS", fmt.Sprintf("nas%03d", i))
	}

	h := NewMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if n := strings.Count(body, "cee_events_by_server_total{"); n > metrics.MaxEventLabels {
		t.Errorf("emitted %d server series, want at most %d", n, metrics.MaxEventLabels)
	}
	if strings.Contains(body, "cee_event_labels_dropped_total 0") {
		t.Error("cee_event_labels_dropped_total is 0 after exceeding the cap; hitting it must be visible")
	}
}

// TestMetricsHandler_LastEventTimestamp: a zero event rate is ambiguous on its
// own — it reads the same whether the estate is quiet or the pipeline is dead,
// and those need different responses. The store has always tracked the time of
// the last processed event; it was only ever visible in the health snapshot, so
// no dashboard or alert could use it.
//
// Zero when nothing has been processed, matching cee_last_fsync_unix_seconds:
// an exporter that has never seen an event must not look like one whose last
// event was at the epoch.
func TestMetricsHandler_LastEventTimestamp(t *testing.T) {
	metrics.M.ResetEventBreakdown()
	t.Cleanup(metrics.M.ResetEventBreakdown)

	h := NewMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "cee_last_event_unix_seconds") {
		t.Fatal("cee_last_event_unix_seconds absent; a zero event rate cannot be told from a dead pipeline")
	}
}
