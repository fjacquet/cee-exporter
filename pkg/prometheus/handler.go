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

// newRegistry builds a private registry holding the core cee_* collectors.
//
// A private registry — not prometheus.DefaultRegisterer — keeps Go runtime
// metrics out of the scrape output.
func newRegistry() *prometheus.Registry {
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
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name: "cee_last_fsync_unix_seconds",
				Help: "Unix timestamp of the last successful fsync to the EVTX file. " +
					"0 = no fsync has occurred yet. Alert when time()-this > flush_interval_s*2.",
			},
			func() float64 { return float64(metrics.M.LastFsyncUnix()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_events_truncated_total",
				Help: "Total events with at least one field capped before the EVTX writer.",
			},
			func() float64 { return float64(metrics.M.EventsTruncatedTotal.Load()) },
		),
	)

	return reg
}

// NewMetricsHandler returns an http.Handler that serves Prometheus text-format
// metrics for the four core cee-exporter counters plus one bonus counter.
//
// A private prometheus.Registry is used — not prometheus.DefaultRegisterer —
// so the scrape output contains only cee_* metrics and no Go runtime data.
func NewMetricsHandler() http.Handler {
	return promhttp.HandlerFor(newRegistry(), promhttp.HandlerOpts{})
}

// NewMetricsHandlerWithBuildInfo returns the standard metrics handler plus a
// cee_build_info gauge, fixed at 1 and labelled with the build-stamped version
// and the Go toolchain version. This is the conventional Prometheus idiom for
// exposing build metadata: join on it to correlate metrics with a release.
func NewMetricsHandlerWithBuildInfo(version, goVersion string) http.Handler {
	reg := newRegistry()
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name:        "cee_build_info",
			Help:        "Build metadata. Always 1; read the labels.",
			ConstLabels: prometheus.Labels{"version": version, "go_version": goVersion},
		},
		func() float64 { return 1 },
	))
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// Serve starts a dedicated HTTP server on addr and registers /metrics,
// including the cee_build_info gauge for the given build.
func Serve(addr, version, goVersion string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", NewMetricsHandlerWithBuildInfo(version, goVersion))

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return srv.ListenAndServe()
}
