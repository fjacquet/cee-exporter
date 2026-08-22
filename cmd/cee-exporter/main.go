// cee-exporter — Dell PowerStore CEPA → Windows Event Log bridge
//
// Listens for CEPA HTTP PUT requests, transforms them into WindowsEvent
// structures, and forwards them to one or more output backends.
//
// Usage:
//
//	cee-exporter -config /etc/cee-exporter/config.toml
//
// Environment variables override config file values:
//
//	CEE_LOG_LEVEL   — debug | info | warn | error  (default: info)
//	CEE_LOG_FORMAT  — json | text                  (default: json in prod)
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	goevtx "github.com/fjacquet/go-evtx"
	"golang.org/x/crypto/acme/autocert"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	applog "github.com/fjacquet/cee-exporter/pkg/log"
	"github.com/fjacquet/cee-exporter/pkg/metrics"
	ceeprometheus "github.com/fjacquet/cee-exporter/pkg/prometheus"
	"github.com/fjacquet/cee-exporter/pkg/queue"
	"github.com/fjacquet/cee-exporter/pkg/server"
)

// version is the build-stamped release identifier, set at link time:
//
//	go build -ldflags "-X main.version=v4.1.3"
//
// An unstamped build reports "dev". It must never be a hardcoded release
// number: operators correlate fleet deployments by this value in the logs and
// in the cee_build_info metric.
var version = "dev"

// ----------------------------------------------------------------------------
// Configuration
// ----------------------------------------------------------------------------

// Config is the top-level config file structure.
type Config struct {
	Listen   ListenConfig              `toml:"listen"`
	Output   OutputConfig              `toml:"output"`
	Queue    QueueConfig               `toml:"queue"`
	Logging  LoggingConfig             `toml:"logging"`
	Metrics  MetricsConfig             `toml:"metrics"`
	CEPA     server.RegistrationConfig `toml:"cepa"`
	Server   server.LimitsConfig       `toml:"server"`
	Hostname string                    `toml:"hostname"` // embedded in events; default: os.Hostname()
}

type ListenConfig struct {
	// Addr is the port CEE sends events to. 12228 is CEE's own listener port
	// borrowed as a default, not a CEPA-assigned one — the CEE-to-partner port
	// is chosen by the administrator and must match CEE's EndPoint. Co-resident
	// with CEE this collides; see config.toml.example.
	Addr string `toml:"addr"` // e.g. "0.0.0.0:12228"
	// Deprecated: use TLSMode="manual" instead. Kept for backward compatibility.
	TLS      bool   `toml:"tls"`
	CertFile string `toml:"cert_file"` // tls_mode="manual": path to PEM certificate file
	KeyFile  string `toml:"key_file"`  // tls_mode="manual": path to PEM private key file
	// TLSMode selects certificate provisioning: "off" | "manual" | "acme" | "self-signed"
	// Default "off". If empty and TLS=true+CertFile!="", migrated to "manual" automatically.
	TLSMode           string   `toml:"tls_mode"`
	ACMEDomains       []string `toml:"acme_domains"`        // tls_mode="acme": domain list for Let's Encrypt
	ACMEEmail         string   `toml:"acme_email"`          // tls_mode="acme": contact email (recommended)
	ACMECacheDir      string   `toml:"acme_cache_dir"`      // tls_mode="acme": cert cache dir, default /var/cache/cee-exporter/acme
	ACMEChallengeAddr string   `toml:"acme_challenge_addr"` // tls_mode="acme": challenge listener addr, default :443
}

// migrateListenConfig converts the legacy tls=true + cert_file/key_file pattern
// to tls_mode="manual" so that old config.toml files keep working after the
// Phase 8 upgrade.
func migrateListenConfig(cfg *ListenConfig) {
	if cfg.TLSMode != "" {
		return // explicit tls_mode set — no migration needed
	}
	if cfg.TLS && cfg.CertFile != "" {
		cfg.TLSMode = "manual"
		return
	}
	cfg.TLSMode = "off"
}

type OutputConfig struct {
	// Type selects the output backend. On Windows, "evtx" routes to the Win32
	// EventLog API via NewNativeEvtxWriter; on other platforms it writes binary
	// .evtx files. There is no separate "win32" type — the platform decides.
	Type         string   `toml:"type"`    // "gelf" | "evtx" | "multi" | "syslog" | "beats"
	Targets      []string `toml:"targets"` // for type="multi"
	EVTXPath     string   `toml:"evtx_path"`
	GELFHost     string   `toml:"gelf_host"`
	GELFPort     int      `toml:"gelf_port"`
	GELFProtocol string   `toml:"gelf_protocol"` // "tcp" | "udp"
	GELFTLS      bool     `toml:"gelf_tls"`
	// Syslog output (type = "syslog")
	SyslogHost     string `toml:"syslog_host"`
	SyslogPort     int    `toml:"syslog_port"`     // default 514 (set in NewSyslogWriter)
	SyslogProtocol string `toml:"syslog_protocol"` // "udp" | "tcp"
	SyslogAppName  string `toml:"syslog_app_name"` // default "cee-exporter" (set in NewSyslogWriter)
	// Beats output (type = "beats")
	BeatsHost string `toml:"beats_host"`
	BeatsPort int    `toml:"beats_port"` // default 5044 (set in NewBeatsWriter)
	BeatsTLS  bool   `toml:"beats_tls"`
	// FlushIntervalSec is the interval in seconds between periodic checkpoint writes
	// for the evtx output type. 0 disables the background flush goroutine.
	// Default: 15 (set in defaultConfig). Only applies when type = "evtx".
	FlushIntervalSec int `toml:"flush_interval_s"`
	// MaxFileSizeMB rotates the active .evtx file when it reaches this size.
	// 0 = unlimited. Only applies when type = "evtx".
	MaxFileSizeMB int `toml:"max_file_size_mb"`
	// MaxFileCount keeps only the N most recent archive .evtx files.
	// 0 = unlimited. Only applies when type = "evtx".
	MaxFileCount int `toml:"max_file_count"`
	// RotationIntervalH rotates the active .evtx file every N hours.
	// 0 = disabled. Only applies when type = "evtx".
	RotationIntervalH int `toml:"rotation_interval_h"`
}

type QueueConfig struct {
	Capacity int `toml:"capacity"` // default 100000
	Workers  int `toml:"workers"`  // default 4
	// DrainTimeoutS bounds how long shutdown waits for the queue to drain.
	// Default 30 (set in defaultConfig).
	DrainTimeoutS int `toml:"drain_timeout_s"`
}

type LoggingConfig struct {
	Level  string `toml:"level"`  // debug | info | warn | error
	Format string `toml:"format"` // json | text
}

type MetricsConfig struct {
	Enabled bool   `toml:"enabled"`
	Addr    string `toml:"addr"` // default "0.0.0.0:9228"
}

func defaultConfig() Config {
	return Config{
		Listen: ListenConfig{
			Addr: "0.0.0.0:12228",
		},
		Output: OutputConfig{
			Type:             "gelf",
			GELFHost:         "localhost",
			GELFPort:         12201,
			GELFProtocol:     "udp",
			FlushIntervalSec: 15,
			// EVTX rotation. Zero means "unlimited"/"disabled" for these three,
			// so omitting them here left a config that does not mention
			// rotation with rotation switched off — while config.toml
			// advertised 100/100/24 as the defaults. A long-running deployment
			// then grew one .evtx file without bound.
			MaxFileSizeMB:     100,
			MaxFileCount:      100,
			RotationIntervalH: 24,
		},
		Queue: QueueConfig{
			Capacity:      100000,
			Workers:       4,
			DrainTimeoutS: 30,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Addr:    "0.0.0.0:9228",
		},
		Server: server.LimitsConfig{
			MaxBodyMB:             8,
			MaxConcurrentRequests: 8,
		},
	}
}

// ----------------------------------------------------------------------------
// Entry point
// ----------------------------------------------------------------------------

func main() {
	runWithServiceManager(run)
}

func run(ctx context.Context) {
	cfgPath := flag.String("config", "config.toml", "path to TOML configuration file")
	emitTest := flag.Bool("emit-test-events", false,
		"write one sample event per mapped Windows event ID and exit; use to verify event source registration and message rendering")
	flag.Parse()

	cfg := defaultConfig()
	if _, err := toml.DecodeFile(*cfgPath, &cfg); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	migrateListenConfig(&cfg.Listen)

	if err := validateOutputConfig(cfg.Output); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// Environment variable overrides.
	if v := os.Getenv("CEE_LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := os.Getenv("CEE_LOG_FORMAT"); v != "" {
		cfg.Logging.Format = v
	}

	applog.Init(cfg.Logging.Level, cfg.Logging.Format)

	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	slog.Info("cee_exporter_starting",
		"version", version,
		"go_version", runtime.Version(),
		"os", runtime.GOOS,
		"hostname", hostname,
		"listen", cfg.Listen.Addr,
		"output_type", cfg.Output.Type,
		"queue_capacity", cfg.Queue.Capacity,
		"queue_workers", cfg.Queue.Workers,
	)

	// Build writer.
	w, writerAddr, err := buildWriter(cfg.Output)
	if err != nil {
		slog.Error("writer_init_failed", "error", err)
		os.Exit(1)
	}

	if *emitTest {
		// os.Exit rather than return: on Windows, run() executes as a goroutine
		// started by the service manager's Start() (see service_windows.go),
		// which then blocks waiting for a stop signal. Returning from run()
		// here would end that goroutine but leave the process hanging forever.
		// os.Exit terminates the whole process regardless of which goroutine
		// calls it — which also means deferred calls never run, so Close is
		// called explicitly below rather than deferred, on every exit path,
		// so whatever was written before a failure still reaches disk.
		emitErr := emitTestEvents(w, hostname)
		if emitErr != nil {
			slog.Error("emit_test_events_failed", "err", emitErr)
		}
		closeErr := w.Close()
		if closeErr != nil {
			slog.Error("emit_test_events_writer_close_failed", "err", closeErr)
		}
		os.Exit(emitExitCode(emitErr, closeErr))
	}

	// Wire SIGHUP → immediate EVTX rotation (no-op on Windows).
	installSIGHUP(w)

	// Build queue.
	q := queue.New(queue.Config{
		Capacity:     cfg.Queue.Capacity,
		Workers:      cfg.Queue.Workers,
		DrainTimeout: time.Duration(cfg.Queue.DrainTimeoutS) * time.Second,
	}, w)
	q.Start(ctx)

	// Build HTTP mux.
	mux := http.NewServeMux()
	mux.Handle("/", server.NewHandler(q, hostname, cfg.CEPA, cfg.Server))
	mux.Handle("/health", server.NewHealthHandler(server.HealthConfig{
		StartTime:   time.Now(),
		WriterType:  cfg.Output.Type,
		WriterAddr:  writerAddr,
		TLSEnabled:  cfg.Listen.TLSMode != "off",
		TLSCertFile: cfg.Listen.CertFile,
	}))

	// TLS initialization — mode selected by tls_mode config field.
	var tlsCfg *tls.Config
	switch cfg.Listen.TLSMode {
	case "off", "":
		// Plain HTTP — no TLS config needed.

	case "manual":
		tlsCfg, err = buildManualTLS(cfg.Listen.CertFile, cfg.Listen.KeyFile)
		if err != nil {
			slog.Error("tls_init_failed", "mode", "manual", "error", err)
			os.Exit(1)
		}
		logCertInfo(cfg.Listen.CertFile)

	case "self-signed":
		tlsCfg, err = buildSelfSignedTLS(cfg.Listen.ACMEDomains)
		if err != nil {
			slog.Error("tls_init_failed", "mode", "self-signed", "error", err)
			os.Exit(1)
		}
		slog.Info("tls_self_signed_generated", "hosts", cfg.Listen.ACMEDomains)

	case "acme":
		var acmeMgr *autocert.Manager
		acmeMgr, tlsCfg, err = buildAutocertTLS(cfg.Listen.ACMEDomains, cfg.Listen.ACMEEmail, cfg.Listen.ACMECacheDir)
		if err != nil {
			slog.Error("tls_init_failed", "mode", "acme", "error", err)
			os.Exit(1)
		}
		if err := startACMEChallengeListener(acmeMgr, cfg.Listen.ACMEChallengeAddr); err != nil {
			slog.Error("acme_challenge_listener_failed", "error", err)
			os.Exit(1)
		}

	default:
		slog.Error("tls_unknown_mode", "mode", cfg.Listen.TLSMode,
			"valid_modes", "off|manual|acme|self-signed")
		os.Exit(1)
	}

	// Build HTTP server.
	httpServer := &http.Server{
		Addr:         cfg.Listen.Addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
		TLSConfig:    tlsCfg, // nil = no TLS
	}

	// Start listener.
	ln, err := net.Listen("tcp", cfg.Listen.Addr)
	if err != nil {
		slog.Error("listen_failed", "addr", cfg.Listen.Addr, "error", err)
		os.Exit(1)
	}

	go func() {
		var serveErr error
		if tlsCfg != nil {
			// For manual mode: cert/key from files; for acme/self-signed: certs in TLSConfig
			if cfg.Listen.TLSMode == "manual" {
				serveErr = httpServer.ServeTLS(ln, cfg.Listen.CertFile, cfg.Listen.KeyFile)
			} else {
				serveErr = httpServer.ServeTLS(ln, "", "")
			}
		} else {
			serveErr = httpServer.Serve(ln)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("http_server_error", "error", serveErr)
			os.Exit(1)
		}
	}()

	if cfg.Metrics.Enabled {
		go func() {
			if err := ceeprometheus.Serve(cfg.Metrics.Addr, version, runtime.Version()); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics_server_error", "error", err)
			}
		}()
		slog.Info("metrics_server_started", "addr", cfg.Metrics.Addr)
	}

	slog.Info("cee_exporter_ready", "addr", cfg.Listen.Addr, "tls_mode", cfg.Listen.TLSMode)

	// Graceful shutdown on SIGTERM / SIGINT or context cancellation (e.g. Windows SCM Stop).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sig:
		slog.Info("shutdown_signal_received")
	case <-ctx.Done():
		slog.Info("shutdown_context_cancelled")
	}

	slog.Info("shutdown_initiated",
		"queue_depth", q.Len(),
	)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("http_shutdown_error", "error", err)
	}

	q.Stop()
	slog.Info("cee_exporter_stopped")
}

// ----------------------------------------------------------------------------
// Writer factory
// ----------------------------------------------------------------------------

// validateOutputConfig validates the output section of the configuration.
// Returns nil if valid, or an error describing the problem.
// Only validates EVTX-specific fields when cfg.Type == "evtx".
func validateOutputConfig(cfg OutputConfig) error {
	if cfg.Type == "evtx" {
		if cfg.EVTXPath == "" {
			return fmt.Errorf("[output] evtx_path must be set when type = \"evtx\"")
		}
		if cfg.FlushIntervalSec == 0 {
			return fmt.Errorf("[output] flush_interval_s = 0 disables periodic fsync; " +
				"set to a positive value (default: 15) when type = \"evtx\" to bound data loss on crash")
		}
		if cfg.FlushIntervalSec < 0 {
			return fmt.Errorf("[output] flush_interval_s must be > 0, got %d", cfg.FlushIntervalSec)
		}
		if cfg.MaxFileSizeMB < 0 {
			return fmt.Errorf("[output] max_file_size_mb must be >= 0, got %d", cfg.MaxFileSizeMB)
		}
		if cfg.MaxFileCount < 0 {
			return fmt.Errorf("[output] max_file_count must be >= 0, got %d", cfg.MaxFileCount)
		}
		if cfg.RotationIntervalH < 0 {
			return fmt.Errorf("[output] rotation_interval_h must be >= 0, got %d", cfg.RotationIntervalH)
		}
	}
	return nil
}

func buildWriter(cfg OutputConfig) (evtx.Writer, string, error) {
	switch cfg.Type {
	case "gelf":
		w, err := evtx.NewGELFWriter(evtx.GELFConfig{
			Host:     cfg.GELFHost,
			Port:     cfg.GELFPort,
			Protocol: cfg.GELFProtocol,
			TLS:      cfg.GELFTLS,
		})
		addr := net.JoinHostPort(cfg.GELFHost, fmt.Sprintf("%d", cfg.GELFPort))
		return w, addr, err

	case "evtx":
		w, err := evtx.NewNativeEvtxWriter(cfg.EVTXPath, goevtx.RotationConfig{
			FlushIntervalSec:  cfg.FlushIntervalSec,
			MaxFileSizeMB:     cfg.MaxFileSizeMB,
			MaxFileCount:      cfg.MaxFileCount,
			RotationIntervalH: cfg.RotationIntervalH,
			OnFsync:           metrics.M.RecordFsyncAt,
		})
		return w, cfg.EVTXPath, err

	case "multi":
		var writers []evtx.Writer
		var addrs []string
		for _, t := range cfg.Targets {
			sub := cfg
			sub.Type = t
			ww, addr, err := buildWriter(sub)
			if err != nil {
				return nil, "", fmt.Errorf("multi target %q: %w", t, err)
			}
			writers = append(writers, ww)
			addrs = append(addrs, addr)
		}
		return evtx.NewMultiWriter(writers...), fmt.Sprintf("%v", addrs), nil

	case "syslog":
		w, err := evtx.NewSyslogWriter(evtx.SyslogConfig{
			Host:     cfg.SyslogHost,
			Port:     cfg.SyslogPort,
			Protocol: cfg.SyslogProtocol,
			AppName:  cfg.SyslogAppName,
		})
		addr := net.JoinHostPort(cfg.SyslogHost, fmt.Sprintf("%d", cfg.SyslogPort))
		return w, addr, err

	case "beats":
		w, err := evtx.NewBeatsWriter(evtx.BeatsConfig{
			Host: cfg.BeatsHost,
			Port: cfg.BeatsPort,
			TLS:  cfg.BeatsTLS,
		})
		addr := net.JoinHostPort(cfg.BeatsHost, fmt.Sprintf("%d", cfg.BeatsPort))
		return w, addr, err

	default:
		return nil, "", fmt.Errorf("unknown output type %q", cfg.Type)
	}
}
