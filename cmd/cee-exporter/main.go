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
	Addr    string `toml:"addr"`     // e.g. "0.0.0.0:12228"
	TLS     bool   `toml:"tls"`
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
}

type OutputConfig struct {
	Type         string `toml:"type"`          // "gelf" | "evtx" | "win32" | "multi"
	Targets      []string `toml:"targets"`     // for type="multi"
	EVTXPath     string `toml:"evtx_path"`
	GELFHost     string `toml:"gelf_host"`
	GELFPort     int    `toml:"gelf_port"`
	GELFProtocol string `toml:"gelf_protocol"` // "tcp" | "udp"
	GELFTLS      bool   `toml:"gelf_tls"`
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

func run() {
	cfgPath := flag.String("config", "config.toml", "path to TOML configuration file")
	flag.Parse()

	cfg := defaultConfig()
	if _, err := toml.DecodeFile(*cfgPath, &cfg); err != nil && !os.IsNotExist(err) {
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
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)

	// Build HTTP mux.
	mux := http.NewServeMux()
	mux.Handle("/", server.NewHandler(q, hostname))
	mux.Handle("/health", server.NewHealthHandler(server.HealthConfig{
		StartTime:   time.Now(),
		WriterType:  cfg.Output.Type,
		WriterAddr:  writerAddr,
		TLSEnabled:  cfg.Listen.TLS,
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

	if cfg.Listen.TLS {
		tlsCfg, err := buildTLS(cfg.Listen.CertFile, cfg.Listen.KeyFile)
		if err != nil {
			slog.Error("tls_init_failed", "error", err)
			os.Exit(1)
		}
		httpServer.TLSConfig = tlsCfg
		logCertInfo(cfg.Listen.CertFile)
	}

	// Start listener.
	ln, err := net.Listen("tcp", cfg.Listen.Addr)
	if err != nil {
		slog.Error("listen_failed", "addr", cfg.Listen.Addr, "error", err)
		os.Exit(1)
	}

	go func() {
		var serveErr error
		if cfg.Listen.TLS {
			serveErr = httpServer.ServeTLS(ln, cfg.Listen.CertFile, cfg.Listen.KeyFile)
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
			if err := ceeprometheus.Serve(cfg.Metrics.Addr); err != nil && err != http.ErrServerClosed {
				slog.Error("metrics_server_error", "error", err)
			}
		}()
		slog.Info("metrics_server_started", "addr", cfg.Metrics.Addr)
	}

	slog.Info("cee_exporter_ready", "addr", cfg.Listen.Addr, "tls", cfg.Listen.TLS)

	// Graceful shutdown on SIGTERM / SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	slog.Info("shutdown_initiated",
		"queue_depth", q.Len(),
	)
	cancel()

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

	default:
		return nil, "", fmt.Errorf("unknown output type %q", cfg.Type)
	}
}

// ----------------------------------------------------------------------------
// TLS helpers
// ----------------------------------------------------------------------------

func buildTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func logCertInfo(certFile string) {
	// Startup log: cert fingerprint and expiry.
	slog.Info("tls_cert_loaded", "cert_file", certFile)
}
