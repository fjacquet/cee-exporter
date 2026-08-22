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
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name: "cee_last_event_unix_seconds",
				Help: "Unix timestamp of the last event processed. 0 = none yet. " +
					"This is what separates a quiet estate from a dead pipeline: " +
					"a zero event rate reads identically in both cases, and they " +
					"need different responses. Pair it with the rate rather than " +
					"alerting on the rate alone.",
			},
			func() float64 { return float64(metrics.M.LastEventUnix()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_events_truncated_total",
				Help: "Total events with at least one field capped before the EVTX writer.",
			},
			func() float64 { return float64(metrics.M.EventsTruncatedTotal.Load()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_cepa_peers_dropped_total",
				Help: "Total requests from CEPA publishers not recorded because " +
					"the MaxPeers cap was reached; increments on every such " +
					"request, not once per distinct publisher. Non-zero means " +
					"the remote label is truncated and a real publisher may " +
					"be missing.",
			},
			func() float64 { return float64(metrics.M.PeersDropped()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_event_labels_dropped_total",
				Help: "Total event-breakdown increments discarded because the " +
					"MaxEventLabels cap was reached. Non-zero means " +
					"cee_events_by_type_total or cee_events_by_server_total is " +
					"truncated and a real event type or NAS server may be " +
					"missing from it; the scalar cee_events_received_total is " +
					"unaffected and stays authoritative for the total.",
			},
			func() float64 { return float64(metrics.M.EventLabelsDropped()) },
		),
		prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Name: "cee_requests_throttled_total",
				Help: "Total CEPA requests that had to wait for a concurrency " +
					"slot before their body was read. Non-zero means " +
					"max_concurrent_requests is binding: publishers are being " +
					"held past their 3-second ACK budget and will mark this " +
					"consumer unavailable. Raise the limit only after checking " +
					"that live heap has room — each slot is worth roughly " +
					"4x max_body_mb.",
			},
			func() float64 { return float64(metrics.M.RequestsThrottledTotal.Load()) },
		),
	)

	reg.MustRegister(cepaCollector{})

	return reg
}

// The per-publisher series need a dynamic label set, which
// prometheus.CounterFunc and prometheus.GaugeFunc cannot carry — hence a
// hand-written collector rather than another entry in newRegistry's
// MustRegister list.
var (
	cepaLastRequestDesc = prometheus.NewDesc(
		"cee_cepa_last_request_unix_seconds",
		"Unix timestamp of the last CEPA request received from this publisher, "+
			"whether handshake, event batch, or failed payload. Alert when "+
			"time()-this exceeds several times CEE's HeartBeatIntervalSecs "+
			"(default 10). A publisher is whatever opened the connection, which "+
			"is not the same as where the event happened: a CEE server relaying "+
			"for a NAS appears here under its own address, and the NAS behind it "+
			"does not appear at all. Arrays that publish directly, such as OneFS "+
			"nodes, do appear in their own right. A NAS that stops generating "+
			"events while its CEE server keeps heartbeating is therefore "+
			"invisible here; cee_events_by_server_total is where that shows.",
		[]string{"remote"}, nil,
	)
	cepaRegistrationsDesc = prometheus.NewDesc(
		"cee_cepa_registrations_total",
		"Total CEPA RegisterRequest handshakes received from this publisher. "+
			"Only the PowerStore dialect's RegisterRequest counts. A publisher "+
			"that heartbeats healthily but has never registered reads zero "+
			"here, which is the intended signal and not a gap — arrays "+
			"publishing directly only ever heartbeat. See "+
			"cee_cepa_heartbeats_total.",
		[]string{"remote"}, nil,
	)
	eventsByTypeDesc = prometheus.NewDesc(
		"cee_events_by_type_total",
		"Total CEPA events received, by event type and protocol. The scalar "+
			"cee_events_received_total says how many; this says what. Note it "+
			"counts deliveries, not distinct operations — a redelivered event "+
			"increments its type again, so a single type pinned at a constant "+
			"rate is worth checking against the wire.",
		[]string{"event_type", "protocol"}, nil,
	)
	eventsByServerDesc = prometheus.NewDesc(
		"cee_events_by_server_total",
		"Total CEPA events received, by the NAS server the operation happened "+
			"on. This is the array's own name for itself, not the publisher "+
			"that delivered it — an event relayed by a CEE server is attributed "+
			"to the NAS, which is what cee_cepa_* cannot tell you. The client "+
			"address is deliberately not a label: it is unbounded.",
		[]string{"server"}, nil,
	)
	cepaHeartbeatsDesc = prometheus.NewDesc(
		"cee_cepa_heartbeats_total",
		"Total CEPA liveness exchanges received from this publisher, in either "+
			"dialect: CEE's HeartBeatRequest probe, or the PowerScale "+
			"CheckFileRequest with action 9. Both are sent per heartbeat "+
			"interval, so this is a heartbeat rate. Counted separately from "+
			"registrations, which they were previously folded into.",
		[]string{"remote"}, nil,
	)
)

// cepaCollector emits the per-publisher CEPA series from a single snapshot
// per scrape, which keeps the two series mutually consistent.
type cepaCollector struct{}

func (cepaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- cepaLastRequestDesc
	ch <- cepaRegistrationsDesc
	ch <- cepaHeartbeatsDesc
	ch <- eventsByTypeDesc
	ch <- eventsByServerDesc
}

func (cepaCollector) Collect(ch chan<- prometheus.Metric) {
	for host, p := range metrics.M.PeerSnapshot() {
		ch <- prometheus.MustNewConstMetric(
			cepaLastRequestDesc, prometheus.GaugeValue,
			float64(p.LastRequestUnix), host,
		)
		ch <- prometheus.MustNewConstMetric(
			cepaRegistrationsDesc, prometheus.CounterValue,
			float64(p.Registrations), host,
		)
		ch <- prometheus.MustNewConstMetric(
			cepaHeartbeatsDesc, prometheus.CounterValue,
			float64(p.Heartbeats), host,
		)
	}
	for _, e := range metrics.M.EventTypeSnapshot() {
		ch <- prometheus.MustNewConstMetric(
			eventsByTypeDesc, prometheus.CounterValue,
			float64(e.Count), e.EventType, e.Protocol,
		)
	}
	for _, e := range metrics.M.EventServerSnapshot() {
		ch <- prometheus.MustNewConstMetric(
			eventsByServerDesc, prometheus.CounterValue,
			float64(e.Count), e.Server,
		)
	}
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
