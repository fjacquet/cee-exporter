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
//  2. Response latency: the CEPA heartbeat timeout is ~3 seconds.  The handler
//     ACKs immediately and delegates work to the async queue.
//  3. VCAPS batches: a single PUT may contain thousands of events.
package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/mapper"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
	"github.com/fjacquet/cee-exporter/pkg/parser"
	"github.com/fjacquet/cee-exporter/pkg/queue"
)

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

	defer r.Body.Close()
	body, err := readBody(w, r)
	if err != nil {
		slog.Error("cepa_body_read_error", "remote", r.RemoteAddr, "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// -- Handshake -----------------------------------------------------------
	if parser.IsRegisterRequest(body) {
		slog.Info("cepa_register_request",
			"remote", r.RemoteAddr,
			"body_bytes", len(body),
			"response_bytes", 0, // MUST be 0
		)
		// Respond 200 OK with strictly empty body.
		w.WriteHeader(http.StatusOK)
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

// readBody reads up to 64 MiB from the request body.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	const maxBody = 64 << 20 // 64 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			// MaxBytesReader error string
			if n == 0 {
				return buf, err
			}
			break
		}
	}
	return buf, nil
}
