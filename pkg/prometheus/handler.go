// Package ceeprometheus exposes a Prometheus /metrics endpoint backed by
// in-process atomics from pkg/metrics.  A private registry is used so that
// Go runtime metrics (GC, goroutines) are not included in the scrape output.
package ceeprometheus

import (
	"net/http"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetricsHandler returns an http.Handler that serves Prometheus text-format
// metrics for the four core cee-exporter counters plus one bonus counter.
//
// A private prometheus.Registry is used — not prometheus.DefaultRegisterer —
// so the scrape output contains only cee_* metrics and no Go runtime data.
func NewMetricsHandler() http.Handler {
	reg := prometheus.NewRegistry()

	reg.MustRegister(
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_events_received_total",
				Help: "Total CEPA events received by the HTTP handler.",
			},
			func() float64 { return float64(metrics.M.EventsReceivedTotal.Load()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_events_dropped_total",
				Help: "Total events dropped due to queue overflow.",
			},
			func() float64 { return float64(metrics.M.EventsDroppedTotal.Load()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_writer_errors_total",
				Help: "Total errors returned by the event writer.",
			},
			func() float64 { return float64(metrics.M.WriterErrorsTotal.Load()) },
		),
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name: "cee_queue_depth",
				Help: "Current number of events waiting in the async queue.",
			},
			func() float64 { return float64(metrics.M.QueueDepth()) },
		),
		// Bonus metric: useful for computing success rate in dashboards.
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_events_written_total",
				Help: "Total events successfully written by the output writer.",
			},
			func() float64 { return float64(metrics.M.EventsWrittenTotal.Load()) },
		),
	)

	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// Serve starts a dedicated HTTP server on addr and registers the /metrics
// endpoint.  It uses a private ServeMux so that http.DefaultServeMux is not
// exposed.  The caller should decide what to do with the returned error;
// http.ErrServerClosed is expected on graceful shutdown.
func Serve(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", NewMetricsHandler())

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return srv.ListenAndServe()
}
