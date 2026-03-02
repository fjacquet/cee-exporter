// Health endpoint — GET /health
//
// Returns a JSON snapshot of operational metrics.  Designed for use with
// container health checks, load balancer probes, and monitoring systems.
package server

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/metrics"
)

// HealthConfig is provided once at startup.
type HealthConfig struct {
	StartTime   time.Time
	WriterType  string // "gelf", "evtx", "win32", "multi"
	WriterAddr  string // e.g. "graylog.corp.local:12201"
	TLSEnabled  bool
	TLSCertFile string
}

// HealthHandler serves GET /health.
type HealthHandler struct {
	cfg HealthConfig
}

// NewHealthHandler creates a HealthHandler.
func NewHealthHandler(cfg HealthConfig) *HealthHandler {
	return &HealthHandler{cfg: cfg}
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := metrics.M.Snapshot()
	resp := buildHealthResponse(h.cfg, snap)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("health_encode_error", "error", err)
	}
}

// healthResponse is the JSON schema for GET /health.
type healthResponse struct {
	Status              string     `json:"status"`
	UptimeSeconds       int64      `json:"uptime_seconds"`
	QueueDepth          int64      `json:"queue_depth"`
	EventsReceivedTotal int64      `json:"events_received_total"`
	EventsWrittenTotal  int64      `json:"events_written_total"`
	EventsDroppedTotal  int64      `json:"events_dropped_total"`
	LastEventAt         string     `json:"last_event_at,omitempty"`
	Writer              writerInfo `json:"writer"`
	TLS                 tlsInfo    `json:"tls"`
}

type writerInfo struct {
	Type    string `json:"type"`
	Target  string `json:"target"`
	Healthy bool   `json:"healthy"`
}

type tlsInfo struct {
	Enabled       bool   `json:"enabled"`
	CertExpiry    string `json:"cert_expiry,omitempty"`
	DaysRemaining int    `json:"days_remaining,omitempty"`
}

func buildHealthResponse(cfg HealthConfig, snap metrics.Snapshot) healthResponse {
	resp := healthResponse{
		Status:              "ok",
		UptimeSeconds:       int64(time.Since(cfg.StartTime).Seconds()),
		QueueDepth:          snap.QueueDepth,
		EventsReceivedTotal: snap.EventsReceivedTotal,
		EventsWrittenTotal:  snap.EventsWrittenTotal,
		EventsDroppedTotal:  snap.EventsDroppedTotal,
		Writer: writerInfo{
			Type:    cfg.WriterType,
			Target:  cfg.WriterAddr,
			Healthy: true,
		},
		TLS: buildTLSInfo(cfg),
	}

	if !snap.LastEventAt.IsZero() {
		resp.LastEventAt = snap.LastEventAt.UTC().Format(time.RFC3339)
	}

	if snap.EventsDroppedTotal > 0 {
		resp.Status = "degraded"
	}

	return resp
}

func buildTLSInfo(cfg HealthConfig) tlsInfo {
	info := tlsInfo{Enabled: cfg.TLSEnabled}
	if !cfg.TLSEnabled || cfg.TLSCertFile == "" {
		return info
	}

	cert := loadFirstCert(cfg.TLSCertFile)
	if cert == nil {
		return info
	}

	expiry := cert.NotAfter
	days := int(time.Until(expiry).Hours() / 24)
	info.CertExpiry = expiry.Format("2006-01-02")
	info.DaysRemaining = days

	if days < 30 {
		slog.Warn("tls_cert_expiry_soon",
			"cert_file", cfg.TLSCertFile,
			"expiry", info.CertExpiry,
			"days_remaining", days,
		)
	}
	return info
}

// loadFirstCert reads a PEM file and returns the first certificate found.
func loadFirstCert(path string) *x509.Certificate {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			return cert
		}
	}
	return nil
}
