// Package server implements the CEPA HTTP listener.
//
// Endpoints:
//
//	PUT|POST /       — CEPA receiver: RegisterRequest, CheckFileRequest
//	                   (OneFS), CheckEventRequest (Dell CEE) and event batches
//	GET /health      — JSON health status
//
// Critical protocol constraints:
//
//  1. RegisterRequest: respond HTTP 200 with a <RegisterResponse> document.
//     See pkg/server/register.go for the shape and where it comes from.
//
//     This file previously said the opposite — "respond with an EMPTY body,
//     any XML causes a fatal parse error on the PowerStore side" — and that
//     claim is wrong. It is worth recording rather than deleting, because it
//     cost two bring-ups. CEE parses the reply (CRegisterResponse, built from
//     the response string) and refuses an empty one outright: "Top node is not
//     RegisterResponse", and a consumer CEE never registers can never be sent
//     events.
//
//     This IS what makes CEE answer an array status="0x16" (CEPP_NOT_FOUND) —
//     verified end to end on 2026-08-22, when fixing it took a PowerStore from
//     0x16 with 151 discarded events to 0x0 with events flowing. A dead-endpoint
//     control appears to exonerate this leg (identical 0x16 either way); that is
//     a false negative, since both cases mean "no partner".
//
//     Registering also requires an identity CEE already knows: see
//     RegistrationConfig. And it is not sufficient on its own — CEE then probes
//     with <HeartBeatRequest /> and needs hbStatus=0 (see heartbeat.go), or the
//     partner stays OFFLINE and the array gets 0x12.
//
//     Do NOT read CEE's 10-second <RegisterRequest /> cadence as evidence
//     either way. Measured against three CEE instances whose consumers
//     returned a valid RegisterResponse, an empty body, and non-XML garbage:
//     all three kept re-registering every 10 s, identically, forever. The
//     cadence is just what CEE does over HTTP. The requirement above comes
//     from CEE's code, not from that behaviour.
//
//     The empty-body rule was also read as applying to "the PowerStore side",
//     but the peer that sends RegisterRequest is CEE itself, not the array.
//
//     CheckFileRequest — the OneFS handshake — needs a CheckFileResponse back
//     and is fatal on an empty body. The dialects must not be collapsed.
//
//  2. Response latency: the CEPA heartbeat timeout is ~3 seconds.  The handler
//     ACKs immediately and delegates work to the async queue.
//
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
// The status attribute is the vcstatus the cluster reports, measured twice on
// two different values:
//
//	status="0x1"   →  isi_audit_cee: vcstatus 0x1: VC_ERROR_SETUP
//	status="0x16"  →  isi_audit_cee: vcstatus 0x16: VC_ERROR_CEPP_NOT_FOUND
//
// 0x0 for success is therefore an inference, not a measurement: no successful
// OneFS handshake has ever been observed, because CEE never reached one with
// that cluster. Both values seen map to errors and zero is the conventional
// success. If OneFS still refuses, this constant is the first thing to change.
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

// Handler is the CEPA HTTP handler.
type Handler struct {
	q        *queue.Queue
	hostname string // embedded in every generated WindowsEvent
	reg      RegistrationConfig
}

// NewHandler creates a Handler.
// hostname is the value used for the WindowsEvent.Computer field
// (typically the NAS hostname extracted from the CEPA request context).
// reg describes this consumer to Dell CEE; a zero value takes the defaults.
func NewHandler(q *queue.Queue, hostname string, reg RegistrationConfig) *Handler {
	return &Handler{q: q, hostname: hostname, reg: reg.withDefaults()}
}

// ServeHTTP implements http.Handler.  Only PUT is accepted; everything else
// returns 405.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// PUT and POST both, because the publishers disagree and neither is
	// negotiable. Dell CEE and OneFS send PUT; PowerStore's Data Mover sends
	// `POST /vee` — measured on the wire, User-Agent "EMC Data Mover". While
	// this accepted PUT alone, a NAS server pointed straight at this consumer
	// got 405 on every heartbeat and could never establish a CEPP session.
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
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

	// -- Handshake -----------------------------------------------------------
	if parser.IsRegisterRequest(body) {
		metrics.M.RecordPeerRegistration(peer)

		// Answer in the encoding we were addressed in, exactly as the OneFS
		// branch below does. CEE sends UTF-16LE without a BOM and cannot read
		// a UTF-8 reply.
		reply := h.reg.registrationResponseXML()
		if parser.IsUTF16(body) {
			reply = parser.EncodeUTF16LE(reply)
		}

		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		n, err := w.Write(reply)
		if err != nil {
			slog.Error("cepa_register_response_write_error",
				"remote", r.RemoteAddr, "error", err)
			return
		}

		// friendly_name is logged because it is the one field CEE may match
		// against its own EndPoint partner id, and a mismatch there fails
		// registration with no diagnostic on Windows.
		slog.Info("cepa_register_request",
			"remote", r.RemoteAddr,
			"body_bytes", len(body),
			"response_bytes", n,
			"friendly_name", h.reg.FriendlyName,
		)
		return
	}

	// OneFS. Unlike PowerStore, an empty body is fatal here: the cluster
	// parses the response as a CheckFileResponse and reports
	// STATUS_DATA_ERROR when it cannot, then stops publishing entirely. Both
	// heartbeats and events arrive in this element and both need the same
	// reply; only the action attribute separates them.
	if parser.IsCheckFileRequest(body) {
		action := parser.CheckFileAction(body)
		heartbeat := action == parser.OneFSHeartbeatAction

		if heartbeat {
			metrics.M.RecordPeerRegistration(peer)
		}

		// Answer in the encoding we were addressed in. PowerStore sends
		// UTF-16LE and CEE answers it in UTF-16LE; OneFS sends UTF-8 and is
		// answered in UTF-8. Both measured on the wire. A publisher that
		// cannot parse the reply treats it as fatal, so this is not cosmetic.
		reply := checkFileResponse
		if parser.IsUTF16(body) {
			reply = parser.EncodeUTF16LE(checkFileResponse)
		}

		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		n, err := w.Write(reply)
		if err != nil {
			slog.Error("cepa_check_file_response_write_error",
				"remote", r.RemoteAddr, "error", err)
			return
		}

		if heartbeat {
			slog.Info("cepa_check_file_request",
				"remote", r.RemoteAddr,
				"body_bytes", len(body),
				"response_bytes", n,
			)
			return
		}

		// A file event. The CheckFileResponse above has already advanced the
		// cluster's forwarding cursor, so this record exists nowhere else the
		// moment we return: anything not written here is lost, which is why
		// the failure path below logs the whole payload rather than a summary.
		events, err := parser.ParseOneFSEvent(body, time.Now().UTC())
		if err != nil {
			slog.Warn("cepa_onefs_event_unhandled",
				"remote", r.RemoteAddr,
				"action", action,
				"body_bytes", len(body),
				"error", err,
				"body", string(body),
			)
			return
		}

		slog.Info("cepa_onefs_event_received",
			"remote", r.RemoteAddr,
			"action", action,
			"events_in_batch", len(events),
			"queue_depth", metrics.M.QueueDepth(),
			"latency_ms", time.Since(start).Milliseconds(),
		)
		h.enqueue(events, r)
		return
	}

	// CEE's post-registration liveness probe. Answered explicitly rather than
	// left to fall through to the event parser, which would log it as a parse
	// error and reply with an empty body — telling CEE nothing about whether
	// this consumer is online.
	if parser.IsHeartBeatRequest(body) {
		metrics.M.RecordPeerRegistration(peer)

		reply := heartbeatReply()
		if parser.IsUTF16(body) {
			reply = parser.EncodeUTF16LE(reply)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		n, err := w.Write(reply)
		if err != nil {
			slog.Error("cepa_heartbeat_response_write_error",
				"remote", r.RemoteAddr, "error", err)
			return
		}

		// Worth an Info line rather than Debug: seeing this at all is the
		// signal that registration finally succeeded. Until 2026-08-21 CEE
		// never progressed past <RegisterRequest />.
		slog.Info("cepa_heartbeat_request",
			"remote", r.RemoteAddr,
			"body_bytes", len(body),
			"response_bytes", n,
		)
		return
	}

	// Dell CEE's own event delivery. A third dialect: neither the
	// RegisterRequest it opens with nor the CheckFileRequest OneFS uses.
	// Reached only once registration has succeeded — CEE sends no events to a
	// partner it has not registered.
	if parser.IsCheckEventRequest(body) {
		receiveTime := time.Now().UTC()
		events, err := parser.ParseCheckEventRequest(body, receiveTime)
		if err != nil {
			slog.Warn("cepa_cee_event_unhandled",
				"remote", r.RemoteAddr,
				"body_bytes", len(body),
				"error", err,
				"body", string(body),
			)
			// ACK anyway: CEE marks a partner that does not answer as
			// unavailable and stops publishing to it entirely, which would
			// cost far more than the one payload we could not read — and the
			// payload is in the log above either way.
			w.WriteHeader(http.StatusOK)
			return
		}

		slog.Info("cepa_cee_events_received",
			"remote", r.RemoteAddr,
			"events_in_batch", len(events),
			"queue_depth", metrics.M.QueueDepth(),
			"latency_ms", time.Since(start).Milliseconds(),
		)

		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		h.enqueue(events, r)
		return
	}

	// -- Event payload -------------------------------------------------------
	receiveTime := time.Now().UTC()
	events, parseErr := parser.Parse(body, receiveTime)
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

	h.enqueue(events, r)
}

// enqueue counts, maps and queues a parsed batch, whichever dialect produced
// it. It writes nothing to the response: both callers have already replied,
// because the CEPA heartbeat timeout is ~3 s and the ACK must not wait on
// queue work.
func (h *Handler) enqueue(events []parser.CEPAEvent, r *http.Request) {
	metrics.M.EventsReceivedTotal.Add(int64(len(events)))

	hostname := h.hostname
	if hostname == "" {
		hostname = r.Host
	}
	for _, e := range events {
		// An eventType OneFS sends that this build has no established meaning
		// for. The record is still written — the cluster's cursor has already
		// moved, so dropping it would lose it outright — but it carries the
		// mapper's documented default EventID rather than a researched one,
		// and that distinction has to be visible to whoever reads the trail.
		if parser.IsUnmappedOneFSEventType(e.EventType) {
			slog.Warn("cepa_onefs_event_type_unmapped",
				"remote", r.RemoteAddr,
				"event_type", e.EventType,
				"file_path", e.FilePath,
			)
		}
		// Same contract for Dell CEE's numeric event codes, which are ALL
		// unmapped in this build: pkg/parser/checkevent.go declines to guess
		// which bit each EVENT_* name occupies. Every such record is written
		// with the mapper's default EventID and the raw code preserved in the
		// label, and this line is what makes the gap visible in the log —
		// including the codes needed to fill the table in.
		if parser.IsUnmappedCEEEventType(e.EventType) {
			slog.Warn("cepa_cee_event_type_unmapped",
				"remote", r.RemoteAddr,
				"event_type", e.EventType,
				"file_path", e.FilePath,
			)
		}
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
