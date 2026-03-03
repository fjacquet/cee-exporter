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

	ceeprometheus "github.com/fjacquet/cee-exporter/pkg/prometheus"
	applog "github.com/fjacquet/cee-exporter/pkg/log"
	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/queue"
	"github.com/fjacquet/cee-exporter/pkg/server"
)

// ----------------------------------------------------------------------------
// Configuration
// ----------------------------------------------------------------------------

// Config is the top-level config file structure.
type Config struct {
	Listen   ListenConfig   `toml:"listen"`
	Output   OutputConfig   `toml:"output"`
	Queue    QueueConfig    `toml:"queue"`
	Logging  LoggingConfig  `toml:"logging"`
	Metrics  MetricsConfig  `toml:"metrics"`
	Hostname string         `toml:"hostname"` // embedded in events; default: os.Hostname()
}

type ListenConfig struct {
	Addr     string `toml:"addr"` // e.g. "0.0.0.0:12228"
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
	Type         string   `toml:"type"`          // "gelf" | "evtx" | "win32" | "multi" | "syslog" | "beats"
	Targets      []string `toml:"targets"`       // for type="multi"
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
}

type QueueConfig struct {
	Capacity int `toml:"capacity"` // default 100000
	Workers  int `toml:"workers"`  // default 4
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
			Type:         "gelf",
			GELFHost:     "localhost",
			GELFPort:     12201,
			GELFProtocol: "udp",
		},
		Queue: QueueConfig{
			Capacity: 100000,
			Workers:  4,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Addr:    "0.0.0.0:9228",
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
	flag.Parse()

	cfg := defaultConfig()
	if _, err := toml.DecodeFile(*cfgPath, &cfg); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	migrateListenConfig(&cfg.Listen)

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
		"version", "1.0.0",
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

	// Build queue.
	q := queue.New(cfg.Queue.Capacity, cfg.Queue.Workers, w)
	queueCtx, queueCancel := context.WithCancel(ctx)
	defer queueCancel()
	q.Start(queueCtx)

	// Build HTTP mux.
	mux := http.NewServeMux()
	mux.Handle("/", server.NewHandler(q, hostname))
	mux.Handle("/health", server.NewHealthHandler(server.HealthConfig{
		StartTime:   time.Now(),
		WriterType:  cfg.Output.Type,
		WriterAddr:  writerAddr,
		TLSEnabled:  cfg.Listen.TLSMode != "off",
		TLSCertFile: cfg.Listen.CertFile,
	}))

	// Build HTTP server.
	httpServer := &http.Server{
		Addr:         cfg.Listen.Addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// TODO(phase08-plan03): wire tls_mode switch here

	// Start listener.
	ln, err := net.Listen("tcp", cfg.Listen.Addr)
	if err != nil {
		slog.Error("listen_failed", "addr", cfg.Listen.Addr, "error", err)
		os.Exit(1)
	}

	go func() {
		if serveErr := httpServer.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("http_server_error", "error", serveErr)
			os.Exit(1)
		}
	}()

	if cfg.Metrics.Enabled {
		go func() {
			if err := ceeprometheus.Serve(cfg.Metrics.Addr); err != nil && err != http.ErrServerClosed {
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
		w, err := evtx.NewNativeEvtxWriter(cfg.EVTXPath)
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

