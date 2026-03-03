package ceeprometheus_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
	ceeprometheus "github.com/fjacquet/cee-exporter/pkg/prometheus"
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
	metrics.M.SetQueueDepth(0)

	// Seed known values.
	metrics.M.EventsReceivedTotal.Store(42)
	metrics.M.EventsDroppedTotal.Store(7)
	metrics.M.WriterErrorsTotal.Store(1)
	metrics.M.SetQueueDepth(15)
	metrics.M.EventsWrittenTotal.Store(30)

	h := ceeprometheus.NewMetricsHandler()

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
