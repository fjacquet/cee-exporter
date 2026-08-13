// Package server implements the CEPA HTTP listener.
//
// Endpoints:
//
//	PUT /            — CEPA event receiver (RegisterRequest + event batches)
//	GET /health      — JSON health status
//
// Critical protocol constraints (from Dell CEPA documentation):
//  1. RegisterRequest: respond HTTP 200 with an EMPTY body.  Any XML in the
//     response causes a fatal parse error on the PowerStore side.
//     CheckFileRequest — the OneFS handshake — is the exact opposite: an
//     empty body is fatal there, and it needs a CheckFileResponse back. The
//     two dialects must not be collapsed.
//  2. Response latency: the CEPA heartbeat timeout is ~3 seconds.  The handler
//     ACKs immediately and delegates work to the async queue.
//  3. VCAPS batches: a single PUT may contain thousands of events.
package server

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/mapper"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
	"github.com/fjacquet/cee-exporter/pkg/parser"
	"github.com/fjacquet/cee-exporter/pkg/queue"
)

// checkFileResponse is the reply to a OneFS <CheckFileRequest> heartbeat.
//
// Shape and field values are copied from what Dell CEE 9.2.0.0 puts on the
// wire, captured with tcpdump between an OneFS 9.13.0.0 cluster and a CEE
// server (2026-08-12). OneFS parses this successfully — the only reason it
// rejected CEE was the status value.
//
// The status attribute is the vcstatus the cluster reports. Two values were
// measured from CEE's own replies, both errors:
//
//	status="0x1"   →  isi_audit_cee: vcstatus 0x1: VC_ERROR_SETUP
//	status="0x16"  →  isi_audit_cee: vcstatus 0x16: VC_ERROR_CEPP_NOT_FOUND
//
// 0x0 for success started as an inference from those two — CEE never reached a
// successful handshake with that cluster, so no success had been observed at
// all. It is no longer an inference: this exporter answering 0x0 was accepted
// by a live 4-node OneFS 9.13.0.0 cluster, all four nodes heartbeating cleanly,
// and the cluster's Protocol Audit Cee Time advanced to match Protocol Audit
// Log Time. See the CHANGELOG entry for the measurement.
//
// NOT verified: that this same body is the right answer to an *event* request
// (action 11). It is a HeartBeatResponse, captured from a heartbeat exchange,
// and it is returned verbatim for events because the cluster has to be
// answered with something. No capture of CEE's reply to an event request
// exists. If OneFS rejects it, the symptom is the STATUS_DATA_ERROR this
// change exists to prevent, and this constant is the first thing to change.
//
// The capabilities are advertised rather than negotiated: this consumer
// accepts whatever the cluster sends, so it claims both file protocols and a
// single partner (itself). HDFS is 0 because nothing downstream maps it.
var checkFileResponse = []byte(`<CheckFileResponse status="0x0" ceeVersion="9.2.0.0">
<HeartBeatResponse ceeVersion="9.2.0.0" dtdVersion="2.5.3">
<CEPPHeartBeatResponse>
<CEPPCapabilities>
<Protocols CIFS="1" NFS="1" HDFS="0"/>
<Partner multiplicity="1"/>
</CEPPCapabilities>
</CEPPHeartBeatResponse>
</HeartBeatResponse>
</CheckFileResponse>
`)

// maxLoggedBodyBytes caps how much of a payload a log line may carry. The
// OneFS events measured are ~1 KiB, so this keeps them whole while bounding
// what a malformed or hostile publisher can put in the log: readBody accepts
// up to 64 MiB, and string(body) on that produces a 64 MiB log line and a
// 64 MiB copy to build it.
const maxLoggedBodyBytes = 4096

// loggableBody renders a payload for a log line, truncated to a bound.
func loggableBody(body []byte) string {
	if len(body) <= maxLoggedBodyBytes {
		return string(body)
	}
	return string(body[:maxLoggedBodyBytes]) + "…[truncated]"
}

// Handler is the CEPA HTTP handler.
type Handler struct {
	q        *queue.Queue
	hostname string // embedded in every generated WindowsEvent
}

// NewHandler creates a Handler.
// hostname is the value used for the WindowsEvent.Computer field
// (typically the NAS hostname extracted from the CEPA request context).
func NewHandler(q *queue.Queue, hostname string) *Handler {
	return &Handler{q: q, hostname: hostname}
}

// ServeHTTP implements http.Handler.  Only PUT is accepted; everything else
// returns 405.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Stamp the publisher before anything can fail. A publisher whose body
	// is unreadable or unparseable is broken but alive; recording it only on
	// the success paths would report it as dead. peer is a CEE server, not a
	// NAS Data Mover — see docs/superpowers/specs/2026-08-10-cepa-heartbeat-metrics-design.md.
	peer := peerHost(r.RemoteAddr)
	metrics.M.RecordPeerRequestAt(peer, start)

	defer func() { _ = r.Body.Close() }()
	body, err := readBody(w, r)
	if err != nil {
		slog.Error("cepa_body_read_error", "remote", r.RemoteAddr, "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Transcode once. Every question below — both handshake probes and Parse —
	// needs UTF-8, and CEE sends UTF-16LE, for which each decode rebuilds the
	// whole document. Decoding per question meant a VCAPS batch was transcoded
	// and discarded three times inside the 3-second ACK budget. A payload that
	// cannot be decoded is passed through untouched so Parse reports it with
	// the same error it always did.
	decoded, decodeErr := parser.DecodeBody(body)
	if decodeErr != nil {
		decoded = body
	}

	// -- Handshake -----------------------------------------------------------
	if parser.IsRegisterRequest(decoded) {
		metrics.M.RecordPeerRegistration(peer)
		slog.Info("cepa_register_request",
			"remote", r.RemoteAddr,
			"body_bytes", len(body),
			"response_bytes", 0, // MUST be 0
		)
		// Respond 200 OK with strictly empty body.
		w.WriteHeader(http.StatusOK)
		return
	}

	// OneFS. Unlike PowerStore, an empty body is fatal here: the cluster
	// parses the response as a CheckFileResponse and reports
	// STATUS_DATA_ERROR when it cannot, then stops publishing entirely. Both
	// heartbeats and events arrive in this element and both need the same
	// reply; only the action attribute separates them.
	if parser.IsCheckFileRequest(decoded) {
		action := parser.CheckFileAction(decoded)

		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		n, err := w.Write(checkFileResponse)
		if err != nil {
			slog.Error("cepa_check_file_response_write_error",
				"remote", r.RemoteAddr, "error", err)
			return
		}

		if action == parser.OneFSHeartbeatAction {
			metrics.M.RecordPeerRegistration(peer)
			slog.Info("cepa_check_file_request",
				"remote", r.RemoteAddr,
				"body_bytes", len(body),
				"response_bytes", n,
			)
		} else {
			// An event we cannot decode yet. Acknowledging it advances the
			// cluster's forwarding cursor, so the record is gone for good —
			// count it as dropped and log it at WARN with the payload, so the
			// loss is visible and the format is recoverable from the log
			// rather than disappearing behind an INFO-level "heartbeat" line.
			//
			// The counter is what makes the loss alertable: a log line nobody
			// greps for is not a signal, and cee_events_dropped_total is the
			// series an operator already watches. Remove this branch once
			// OneFS event parsing lands; see docs/powerscale-verification.md.
			metrics.M.EventsDroppedTotal.Add(1)
			slog.Warn("cepa_onefs_event_unhandled",
				"remote", r.RemoteAddr,
				"action", action,
				"body_bytes", len(body),
				"body", loggableBody(body),
			)
		}
		return
	}

	// -- Event payload -------------------------------------------------------
	receiveTime := time.Now().UTC()
	events, parseErr := parser.Parse(decoded, receiveTime)
	if parseErr != nil {
		slog.Error("cepa_parse_error",
			"remote", r.RemoteAddr,
			"body_bytes", len(body),
			"error", parseErr,
		)
		// Still ACK so CEPA doesn't mark us unreachable.
		w.WriteHeader(http.StatusOK)
		return
	}

	metrics.M.EventsReceivedTotal.Add(int64(len(events)))

	slog.Info("cepa_events_received",
		"remote", r.RemoteAddr,
		"events_in_batch", len(events),
		"queue_depth", metrics.M.QueueDepth(),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	// ACK immediately — before any potentially slow queue work.
	w.WriteHeader(http.StatusOK)

	// Flush the response to the CEPA client right away.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Enqueue for async processing.
	hostname := h.hostname
	if hostname == "" {
		hostname = r.Host
	}
	for _, e := range events {
		slog.Debug("cepa_event_detail",
			"event_type", e.EventType,
			"file_path", e.FilePath,
			"client_ip", e.ClientAddr,
			"username", e.Username,
		)
		we := mapper.Map(e, hostname)
		h.q.Enqueue(we)
	}
}

// readBody reads up to 64 MiB from the request body. MaxBytesReader enforces
// the cap; any excess returns an error that the caller maps to HTTP 400.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	const maxBody = 64 << 20 // 64 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	return io.ReadAll(r.Body)
}

// peerHost reduces a RemoteAddr to its host, dropping the ephemeral port.
// The port changes per connection, so leaving it on would make the remote
// label unbounded. An address that does not parse is used as-is; the
// MaxPeers cap in pkg/metrics still bounds it.
func peerHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
