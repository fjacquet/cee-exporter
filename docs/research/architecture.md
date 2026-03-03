# Architecture Research

**Domain:** Go daemon — CEPA audit event bridge, v2 feature integration
**Researched:** 2026-03-03
**Confidence:** HIGH (existing code read directly; new feature patterns verified via official docs and pkg.go.dev)

---

## Current v1 Architecture (Baseline)

### System Overview

```text
Dell PowerStore
  CEPA HTTP PUT
       │
       ▼
┌──────────────────────────────────────────────────────────────┐
│  pkg/server — HTTP listener (:12228)                         │
│  ┌─────────────────┐   ┌──────────────────────────────────┐  │
│  │ Handler (PUT /) │   │ HealthHandler (GET /health) JSON │  │
│  └────────┬────────┘   └──────────────────────────────────┘  │
└───────────┼──────────────────────────────────────────────────┘
            │ Enqueue()
            ▼
┌──────────────────────────────────────────────────────────────┐
│  pkg/queue — buffered channel + N worker goroutines          │
│  ch chan WindowsEvent (capacity 100 000)                     │
│  workers = 4 (configurable)                                  │
└───────────┬──────────────────────────────────────────────────┘
            │
    ┌───────┴────────────────────────────────────────┐
    │  pkg/parser   →   pkg/mapper                    │
    │  CEE XML           CEPAEvent → WindowsEvent      │
    └───────┬────────────────────────────────────────┘
            │ WriteEvent(ctx, WindowsEvent)
            ▼
┌─────────────────────────────────────────────────────────────┐
│  pkg/evtx — Writer interface                                │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │  GELFWriter  │  │ Win32Writer  │  │ BinaryEvtxWriter  │  │
│  │  (all OS)    │  │ (Windows)    │  │  (stub, !windows) │  │
│  └──────────────┘  └──────────────┘  └───────────────────┘  │
│  ┌──────────────────────────────────────────┐               │
│  │  MultiWriter (fan-out to ≥1 of the above)│               │
│  └──────────────────────────────────────────┘               │
└─────────────────────────────────────────────────────────────┘
            │
            ▼
┌─────────────────────────────────────────────────────────────┐
│  pkg/metrics — atomic int64 counters                        │
│  EventsReceivedTotal  EventsWrittenTotal  EventsDroppedTotal │
│  WriterErrorsTotal    QueueDepth          LastEventAt        │
└─────────────────────────────────────────────────────────────┘
```

### Component Responsibilities (v1)

| Component | Responsibility | File(s) |
|-----------|----------------|---------|
| `cmd/cee-exporter/main.go` | Wiring: config, writers, queue, mux, signals | `cmd/cee-exporter/main.go` |
| `pkg/server.Handler` | CEPA HTTP PUT handler; ACKs immediately | `pkg/server/server.go` |
| `pkg/server.HealthHandler` | GET /health JSON snapshot | `pkg/server/health.go` |
| `pkg/parser` | CEE XML → `[]CEPAEvent` | `pkg/parser/parser.go` |
| `pkg/mapper` | `CEPAEvent` → `WindowsEvent` | `pkg/mapper/mapper.go` |
| `pkg/queue.Queue` | Async worker pool; non-blocking Enqueue | `pkg/queue/queue.go` |
| `pkg/evtx.Writer` | Output abstraction interface | `pkg/evtx/writer.go` |
| `pkg/evtx.GELFWriter` | GELF 1.1 JSON over UDP/TCP | `pkg/evtx/writer_gelf.go` |
| `pkg/evtx.Win32EventLogWriter` | Win32 ReportEvent API | `pkg/evtx/writer_windows.go` |
| `pkg/evtx.BinaryEvtxWriter` | Stub; returns error | `pkg/evtx/writer_evtx_stub.go` |
| `pkg/evtx.MultiWriter` | Fan-out to multiple backends | `pkg/evtx/writer_multi.go` |
| `pkg/metrics.Store` | Atomic counters + Snapshot() | `pkg/metrics/metrics.go` |
| `pkg/log` | slog initialisation | `pkg/log/log.go` |

---

## v2 Feature Integration Plan

### Feature 1: Prometheus /metrics Endpoint

**Integration point:** `cmd/cee-exporter/main.go` (modified) + new `pkg/metrics` bridge.

**Problem:** The CEPA listener already occupies port 12228 and is TLS-optioned. The `/metrics` endpoint must not share that mux because:

- Prometheus scrapes are typically unauthenticated and on a private network port.
- The CEPA listener may be TLS-only in production; adding `/metrics` there forces the Prometheus scraper to handle TLS client configuration.
- Industry standard for Go exporters is a **separate port** in the `9xxx` range.

**Solution: separate HTTP server on a dedicated port (default 9228).**

```
Prometheus scraper ──► :9228/metrics
                           │
                    ┌──────┴──────────────────────────────────────┐
                    │  pkg/prometheus — PrometheusHandler          │
                    │  Reads pkg/metrics.M.Snapshot() atomically  │
                    │  Wraps existing counters as Prometheus       │
                    │  CounterVec / Gauge descriptors              │
                    └─────────────────────────────────────────────┘
```

**New file:** `pkg/prometheus/handler.go` (all platforms, no build tag)

The handler creates a `prometheus.NewRegistry()` (not the default registry, to avoid Go runtime metrics pollution) and registers:

| Prometheus Metric | Source in pkg/metrics | Type |
|---|---|---|
| `cee_events_received_total` | `M.EventsReceivedTotal.Load()` | Counter |
| `cee_events_written_total` | `M.EventsWrittenTotal.Load()` | Counter |
| `cee_events_dropped_total` | `M.EventsDroppedTotal.Load()` | Counter |
| `cee_writer_errors_total` | `M.WriterErrorsTotal.Load()` | Counter |
| `cee_queue_depth` | `M.QueueDepth()` | Gauge |

Because `pkg/metrics.Store` uses `atomic.Int64` values, no mutex is needed; each `Collect()` call reads with `Load()`.

**Modified file:** `cmd/cee-exporter/main.go`

Add `MetricsConfig` to `Config` struct:

```go
type MetricsConfig struct {
    Enabled bool   `toml:"enabled"`
    Addr    string `toml:"addr"` // default "0.0.0.0:9228"
}
```

In `main()`, after starting the CEPA `httpServer`:

```go
if cfg.Metrics.Enabled {
    go prometheus.Serve(cfg.Metrics.Addr)
}
```

`prometheus.Serve()` starts its own `net/http` server on the metrics port, binding a `promhttp.HandlerFor(reg, ...)` to `/metrics`. It runs in its own goroutine and is intentionally not TLS (metrics traffic is internal).

**No changes to `pkg/server`, `pkg/queue`, `pkg/evtx`, or `pkg/metrics`.**

**Config additions** (`config.toml.example`):

```toml
[metrics]
enabled = true
addr    = "0.0.0.0:9228"
```

**New dependency:** `github.com/prometheus/client_golang` (verified current on pkg.go.dev, latest `v1.x`).

---

### Feature 2: Systemd Unit File

**Integration point:** None — pure deployment artifact.

**New file:** `deploy/systemd/cee-exporter.service`

This is a static text file committed to the repo. No Go code changes.

Standard pattern for a Go binary as systemd service:

```ini
[Unit]
Description=Dell PowerStore CEPA → Windows Event Log bridge
Documentation=https://github.com/fjacquet/cee-exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cee-exporter
Group=cee-exporter
WorkingDirectory=/etc/cee-exporter
ExecStart=/usr/local/bin/cee-exporter -config /etc/cee-exporter/config.toml
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
Environment=CEE_LOG_FORMAT=json
EnvironmentFile=-/etc/cee-exporter/env
LimitNOFILE=65536
ProtectSystem=full
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

Key choices:

- `Type=simple` because Go's `net/http` goroutine model does not support `Type=fork`.
- `TimeoutStopSec=30s` matches the 30-second graceful shutdown already coded in `main()`.
- `EnvironmentFile=-/etc/cee-exporter/env` (the `-` prefix makes it optional).
- `ProtectSystem=full` + `PrivateTmp=true` + `NoNewPrivileges=true` for minimal privilege surface.

**No Go code changes. No new dependencies.**

---

### Feature 3: Windows Service

**Integration point:** `cmd/cee-exporter/main.go` (modified) + new platform file.

**Library:** `github.com/kardianos/service` v1.x (verified: last published July 2025, supports Windows XP+, imported by 1,359 projects).

**Platform architecture:** The Windows service wrapping must not affect Linux builds.

**Pattern:** Split `main()` into a `run()` function containing the existing startup logic. Add a `serviceRunner` struct that satisfies `service.Interface`. The service wrapper lives in a platform-specific file pair.

**New files:**

```
cmd/cee-exporter/service_notwindows.go   //go:build !windows
cmd/cee-exporter/service_windows.go      //go:build windows
```

`service_notwindows.go` — trivial shim:

```go
//go:build !windows

package main

// runWithServiceManager runs the program directly (no OS service manager).
func runWithServiceManager(runFn func()) {
    runFn()
}
```

`service_windows.go` — wraps via kardianos/service:

```go
//go:build windows

package main

import "github.com/kardianos/service"

type serviceRunner struct{ runFn func() }

func (s *serviceRunner) Start(_ service.Service) error {
    go s.runFn()
    return nil
}

func (s *serviceRunner) Stop(_ service.Service) error { return nil }

func runWithServiceManager(runFn func()) {
    cfg := &service.Config{
        Name:        "CeeExporter",
        DisplayName: "CEE Exporter (PowerStore CEPA bridge)",
        Description: "Bridges Dell PowerStore CEPA audit events to Windows Event Log / GELF / Beats / Syslog.",
    }
    prg := &serviceRunner{runFn: runFn}
    svc, err := service.New(prg, cfg)
    if err != nil {
        panic(err)
    }
    if err := svc.Run(); err != nil {
        panic(err)
    }
}
```

**Modified file:** `cmd/cee-exporter/main.go`

```go
func main() {
    runWithServiceManager(run)
}

func run() {
    // all existing main() logic moved here verbatim
}
```

The signal handling (`syscall.SIGTERM` / `SIGINT`) already in `run()` satisfies the `Stop()` contract on Windows because SCM sends a stop signal that is translated to SIGTERM by kardianos/service. No additional stop logic is needed.

**Service install/uninstall:** kardianos/service provides `svc.Install()` / `svc.Uninstall()`. Expose via `-service install|uninstall|start|stop` CLI flags (add to `main()` flag parsing).

**New dependency:** `github.com/kardianos/service`

**Important:** CGO_ENABLED=0 is preserved. kardianos/service uses pure Go on Windows (calls `golang.org/x/sys/windows` which is already a dependency in `go.mod`).

---

### Feature 4: BinaryEvtxWriter (Pure Go .evtx on Linux)

**Integration point:** `pkg/evtx/writer_evtx_stub.go` (replaced) + new implementation file.

**Context:** No existing Go library writes valid binary .evtx files. All public Go libraries (`0xrawsec/golang-evtx`, `Velocidex/evtx`) are read-only parsers. The BinaryEvtxWriter must be implemented from scratch using the EVTX file format specification (documented in `libyal/libevtx`).

**Complexity:** HIGH. Estimated 500–1500 LOC for a minimal compliant writer covering:

- EVTX file header (magic `ElfFile\0`, chunk count, CRC32)
- Chunk header (magic `ElfChnk\0`, string table, CRC32)
- BinXML token stream (template, fragment, element, attribute, value tokens)
- FILETIME encoding (100-nanosecond intervals since 1601-01-01)
- SID encoding for `SubjectUserSID`
- Log rotation: new chunk when current chunk fills (65536 bytes)

**Replacement strategy:**

Replace `pkg/evtx/writer_evtx_stub.go` (build tag `//go:build !windows`) with the real implementation once complete. The stub can remain during development and be replaced file by file.

**New files:**

```
pkg/evtx/writer_binary_evtx.go           //go:build !windows
pkg/evtx/binxml/                          new sub-package
  chunk.go    — chunk header + CRC32
  header.go   — file header
  token.go    — BinXML token emitter
  sid.go      — SID binary encoding
  filetime.go — Windows FILETIME encoding
```

**Modified file:** `pkg/evtx/writer_native_notwindows.go`

Currently delegates to `NewBinaryEvtxWriter(evtxPath)` which returns the stub. After v2, `NewBinaryEvtxWriter` returns the real implementation. No change to `writer_native_notwindows.go` itself.

**`OutputConfig` already has `EVTXPath` field.** No config schema changes needed.

**No new external dependencies.** Pure stdlib: `encoding/binary`, `hash/crc32`, `os`, `path/filepath`, `sync`.

**Testing approach:** Generate a test .evtx file, parse it with `0xrawsec/golang-evtx` (as a test-only dependency) to verify round-trip correctness.

---

### Feature 5: BeatsWriter (Lumberjack v2)

**Integration point:** New `pkg/evtx/writer_beats.go` (all platforms) + `buildWriter()` in `main.go`.

**Library:** `github.com/elastic/go-lumber/client/v2` (official Elastic library, verified on pkg.go.dev).

**Client choice:** `SyncClient` for simplicity (send batch → wait for ACK → proceed). `AsyncClient` is appropriate if throughput benchmarking shows the ACK round-trip is a bottleneck, but SyncClient is correct for the first implementation.

**Data format:** go-lumber sends `[]interface{}` where each element is a map. Map the `WindowsEvent` struct fields to a flat map:

```go
func windowsEventToBeatsMap(e evtx.WindowsEvent) map[string]interface{} {
    return map[string]interface{}{
        "@timestamp":       e.TimeCreated.UTC().Format(time.RFC3339Nano),
        "event_id":         e.EventID,
        "provider":         e.ProviderName,
        "computer":         e.Computer,
        "object_name":      e.ObjectName,
        "account_name":     e.SubjectUsername,
        "account_domain":   e.SubjectDomain,
        "account_sid":      e.SubjectUserSID,
        "client_address":   e.ClientAddr,
        "access_mask":      e.AccessMask,
        "cepa_event_type":  e.CEPAEventType,
        "bytes_read":       e.BytesRead,
        "bytes_written":    e.BytesWritten,
    }
}
```

**New file:** `pkg/evtx/writer_beats.go` (no build tag — all platforms)

```go
type BeatsWriter struct {
    client *v2.SyncClient
    mu     sync.Mutex
    addr   string
}
```

The writer holds a `SyncClient`, reconnects on error (same pattern as `GELFWriter`). `WriteEvent` sends a single-event batch: `client.Send([]interface{}{eventMap})`.

**Config additions** — new fields in `OutputConfig`:

```go
BeatsHost     string `toml:"beats_host"`
BeatsPort     int    `toml:"beats_port"` // default 5044
BeatsTLS      bool   `toml:"beats_tls"`
```

Output type token: `"beats"`.

**`buildWriter()` modification:** Add `case "beats":` branch.

**Config example additions:**

```toml
# Beats/Lumberjack v2 output (Logstash or Graylog Beats Input)
# beats_host = "logstash.corp.local"
# beats_port = 5044
# beats_tls  = false
```

**New dependency:** `github.com/elastic/go-lumber`

---

### Feature 6: SyslogWriter (RFC 5424)

**Integration point:** New `pkg/evtx/writer_syslog.go` (all platforms) + `buildWriter()` in `main.go`.

**Standard library consideration:** Go's `log/syslog` package implements BSD syslog (RFC 3164), not RFC 5424. It also has build constraints that exclude Windows. For RFC 5424 compliance and cross-platform support, a thin wrapper is needed.

**Approach:** Implement `SyslogWriter` using the stdlib `log/syslog` package on Linux/macOS and a simple TCP/UDP connection-based RFC 5424 formatter on Windows. Use build tags to split.

**Alternative:** Use `github.com/crewjam/rfc5424` (pure Go, cross-platform, verified on pkg.go.dev) to build the RFC 5424 message, then send over a raw `net.Conn`. This avoids all platform split complexity.

**Recommended approach:** Raw `net.Conn` + `crewjam/rfc5424` message builder.

```
SyslogWriter
  ├── net.Conn (UDP or TCP to syslog server)
  ├── RFC 5424 message built by crewjam/rfc5424
  └── reconnect-on-error (same as GELFWriter)
```

**RFC 5424 field mapping:**

| RFC 5424 field | Source |
|---|---|
| MSGID | `strconv.Itoa(e.EventID)` |
| HOSTNAME | `e.Computer` |
| APP-NAME | `e.ProviderName` |
| STRUCTURED-DATA `[cepa@0]` | `CEPAEventType`, `ObjectName`, `SubjectUsername`, `ClientAddr` |
| MSG | `"EventID=<n> <CEPAEventType> on <ObjectName>"` |
| FACILITY | `LOG_AUDIT` (13) or `LOG_LOCAL0` (16) if audit unavailable |
| SEVERITY | `LOG_INFO` (6) |

**New file:** `pkg/evtx/writer_syslog.go` (no build tag — all platforms)

**Config additions:**

```go
SyslogHost     string `toml:"syslog_host"`
SyslogPort     int    `toml:"syslog_port"`   // default 514
SyslogProtocol string `toml:"syslog_protocol"` // "udp" | "tcp"
SyslogTLS      bool   `toml:"syslog_tls"`
```

Output type token: `"syslog"`.

**New dependency:** `github.com/crewjam/rfc5424`

---

## Complete v2 Architecture

```
Dell PowerStore
  CEPA HTTP PUT
       │
       ▼
┌──────────────────────────────────────────────────────────────────┐
│  pkg/server — HTTP listener (:12228, optional TLS)               │
│  ┌──────────────────┐   ┌────────────────────────────────────┐   │
│  │ Handler (PUT /)  │   │ HealthHandler (GET /health) JSON   │   │
│  └─────────┬────────┘   └────────────────────────────────────┘   │
└────────────┼─────────────────────────────────────────────────────┘
             │ Enqueue()
             ▼
┌──────────────────────────────────────────────────────────────────┐
│  pkg/queue — buffered channel + N worker goroutines              │
└────────────┬─────────────────────────────────────────────────────┘
             │ WriteEvent(ctx, WindowsEvent)
             ▼
┌──────────────────────────────────────────────────────────────────┐
│  pkg/evtx — Writer interface                                     │
│  ┌────────────┐  ┌──────────┐  ┌────────────┐  ┌─────────────┐  │
│  │ GELFWriter │  │ Win32Writer│  │BeatsWriter │  │SyslogWriter │  │
│  │ (all)      │  │ (windows)│  │ (all)      │  │ (all)       │  │
│  └────────────┘  └──────────┘  └────────────┘  └─────────────┘  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │  BinaryEvtxWriter  (!windows — real impl replaces stub)   │  │
│  └────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │  MultiWriter (fan-out)                                     │  │
│  └────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────┘
             │
             ▼
┌──────────────────────────────────────────────────────────────────┐
│  pkg/metrics — atomic counters (unchanged)                       │
└──────────────────────────────────────────────────────────────────┘

Prometheus scraper ──► :9228/metrics
                              │
                    ┌─────────┴──────────────────────────────────┐
                    │  pkg/prometheus — separate HTTP mux         │
                    │  Reads pkg/metrics.M.Snapshot()            │
                    └────────────────────────────────────────────┘

[Windows only]
SCM / Service Manager
  Start/Stop signals
       │
       ▼
cmd/cee-exporter/service_windows.go
  kardianos/service wraps run()
```

---

## New Files Created in v2

| File | Build Tag | Purpose |
|------|-----------|---------|
| `pkg/prometheus/handler.go` | none (all) | Prometheus HTTP handler; reads pkg/metrics |
| `pkg/evtx/writer_beats.go` | none (all) | BeatsWriter implementation |
| `pkg/evtx/writer_syslog.go` | none (all) | SyslogWriter implementation |
| `pkg/evtx/writer_binary_evtx.go` | `!windows` | Real BinaryEvtxWriter (replaces stub) |
| `pkg/evtx/binxml/chunk.go` | none (all) | BinXML chunk builder |
| `pkg/evtx/binxml/header.go` | none (all) | EVTX file header builder |
| `pkg/evtx/binxml/token.go` | none (all) | BinXML token stream emitter |
| `pkg/evtx/binxml/sid.go` | none (all) | Windows SID binary encoder |
| `pkg/evtx/binxml/filetime.go` | none (all) | Windows FILETIME encoder |
| `cmd/cee-exporter/service_windows.go` | `windows` | kardianos/service wrapper |
| `cmd/cee-exporter/service_notwindows.go` | `!windows` | No-op service shim |
| `deploy/systemd/cee-exporter.service` | n/a | Systemd unit file (not Go) |

---

## Existing Files Modified in v2

| File | Change | Scope |
|------|--------|-------|
| `cmd/cee-exporter/main.go` | Add `MetricsConfig` struct; add `runWithServiceManager()` call; rename existing `main()` body to `run()`; add `-service` flag; extend `buildWriter()` with `beats`/`syslog` cases | Moderate |
| `pkg/evtx/writer_evtx_stub.go` | Remove or demote to development fallback once `writer_binary_evtx.go` is complete | Moderate |
| `config.toml.example` | Add `[metrics]`, `beats_*`, `syslog_*` fields | Trivial |
| `go.mod` / `go.sum` | Add `client_golang`, `go-lumber`, `rfc5424`, `kardianos/service` | Trivial |

---

## Build Order (Dependency-Driven)

```
Phase 1 — Independent: no dependency on other new components
  1a. Prometheus endpoint
      Depends on: pkg/metrics (exists), main.go (minor changes)
      New dep:    github.com/prometheus/client_golang

  1b. Systemd unit file
      Depends on: nothing (text file)

Phase 2 — Windows Service (depends on main() refactor from Phase 1 changes)
  2.  Windows Service
      Depends on: run() function extracted in Phase 1 changes to main.go
      New dep:    github.com/kardianos/service

Phase 3 — New Writers (independent of each other; both depend on Writer interface)
  3a. BeatsWriter
      Depends on: pkg/evtx.Writer (exists), OutputConfig (minor changes)
      New dep:    github.com/elastic/go-lumber

  3b. SyslogWriter
      Depends on: pkg/evtx.Writer (exists), OutputConfig (minor changes)
      New dep:    github.com/crewjam/rfc5424

Phase 4 — BinaryEvtxWriter (highest complexity; independent of phases 2, 3)
  4.  BinaryEvtxWriter
      Depends on: pkg/evtx.Writer (exists), pkg/evtx/binxml sub-package
      New dep:    none (stdlib only)
      Test dep:   github.com/0xrawsec/golang-evtx (test-only, verify round-trip)
```

**Recommended build sequence:** 1b → 1a → 3a → 3b → 2 → 4

Rationale: Systemd first (zero risk). Prometheus second (validates metrics are correct before adding more writers). Beats and Syslog next (parallel development possible; same Writer interface). Windows Service after Prometheus because it requires the `run()` extraction refactor. BinaryEvtxWriter last because it is the highest complexity and can be developed independently without blocking any other feature.

---

## Data Flow Changes

### Prometheus Data Flow

```
[Prometheus scraper GET :9228/metrics]
  ↓
pkg/prometheus.handler.Collect()
  → metrics.M.Snapshot()   // single atomic read, no lock
  → prometheus.Desc values populated
  ↓
promhttp.HandlerFor(reg) serialises to text/plain
  ↓
HTTP 200 response
```

No event pipeline touched. Zero latency impact on CEPA path.

### BeatsWriter Data Flow

```
pkg/queue worker
  → WriteEvent(ctx, WindowsEvent)
  → BeatsWriter.WriteEvent()
    → windowsEventToBeatsMap(e)   // struct → map[string]interface{}
    → SyncClient.Send([]interface{}{m})
    → wait for Lumberjack ACK
    → return nil or error
```

The SyncClient.Send() blocks until server ACK. This is acceptable because the queue worker pool decouples this from the CEPA HTTP handler. If ACK latency becomes an issue, switch to AsyncClient in a later iteration.

### SyslogWriter Data Flow

```
pkg/queue worker
  → WriteEvent(ctx, WindowsEvent)
  → SyslogWriter.WriteEvent()
    → rfc5424.Message{...}.String()   // format RFC 5424 message
    → conn.Write(msg)
    → reconnect on error (UDP: best-effort, no error; TCP: reconnect)
    → return nil or error
```

### BinaryEvtxWriter Data Flow

```
pkg/queue worker
  → WriteEvent(ctx, WindowsEvent)
  → BinaryEvtxWriter.WriteEvent()
    → binxml.NewFragment(e)   // BinXML token stream
    → chunk.Append(fragment)  // accumulate in current chunk
    → if chunk full: flush chunk to file, open new chunk
    → return nil or error
```

File rotation: one `.evtx` file per run (opened at `NewBinaryEvtxWriter` time), chunks accumulated in memory until full then flushed. On `Close()`, write final partial chunk and update file header chunk count and CRC32.

---

## Platform-Specific File Naming Rules

Following the rule established in v1 (`IMPORTANT: DO NOT use _linux suffix`):

| Platform scope | File suffix | Build tag |
|---|---|---|
| Windows only | `_windows.go` | `//go:build windows` |
| Non-Windows | `_notwindows.go` | `//go:build !windows` |
| All platforms | (no suffix) | none required |

New files for v2 follow this convention:

- `service_windows.go` / `service_notwindows.go` — correct
- `writer_binary_evtx.go` with `//go:build !windows` — correct (replaces `writer_evtx_stub.go` which also uses `!windows`)
- All other new writers and pkg/prometheus — no suffix needed

---

## Config Schema Additions (v2)

New sections/fields in `config.toml` and `Config` struct:

```toml
[metrics]
enabled = true
addr    = "0.0.0.0:9228"

[output]
# existing fields unchanged, add:
beats_host     = "logstash.corp.local"
beats_port     = 5044
beats_tls      = false

syslog_host     = "syslog.corp.local"
syslog_port     = 514
syslog_protocol = "udp"   # or "tcp"
syslog_tls      = false
```

`type` field new valid values: `"beats"`, `"syslog"` (adds to existing `"gelf"`, `"evtx"`, `"multi"`).

`HealthConfig.WriterType` string in `pkg/server/health.go` should be extended to accept new type names — this is purely a display concern, no logic change.

---

## Scalability Considerations

| Scale | Architecture | Notes |
|-------|--------------|-------|
| Current (1 PowerStore NAS) | Single instance, 4 workers, 100k queue | Sufficient |
| 5-10 NAS nodes | Same binary, increase `workers` | Queue depth metric via Prometheus tells when |
| 10+ NAS nodes | Run one instance per NAS or use MultiWriter | Prometheus scrape identifies bottleneck writer |

The async queue architecture means throughput is bounded by the slowest writer. With `MultiWriter`, all backends receive every event; the slowest one determines effective throughput. Prometheus `/metrics` exposes `cee_queue_depth` which is the primary scaling signal.

---

## Anti-Patterns

### Anti-Pattern 1: Adding /metrics to the CEPA mux

**What people do:** Register `promhttp.Handler()` on the existing CEPA HTTP mux at `"/metrics"`.

**Why it's wrong:** The CEPA mux may be TLS-only (forcing Prometheus scraper to handle certs). More importantly, the CEPA handler logs every request — Prometheus scrapes every 15s would pollute CEPA logs. The `RegisterRequest` path check (`parser.IsRegisterRequest`) runs on every PUT; accidental GET to `/` returns 405 with a CEPA-format error.

**Do this instead:** Separate goroutine, separate `http.ServeMux`, separate port (9228). Two servers, zero conflicts.

### Anti-Pattern 2: Blocking WriteEvent in BeatsWriter with unbounded retry

**What people do:** Retry failed `Send()` calls in a loop inside `WriteEvent()`.

**Why it's wrong:** A blocked worker goroutine stops draining the queue. With 4 workers, a Logstash outage causes the queue to fill in seconds and events get dropped.

**Do this instead:** One reconnect attempt (same as GELFWriter). If reconnect fails, return the error. The `pkg/queue` worker logs the error and increments `WriterErrorsTotal`. The Prometheus counter surfaces this to alerting.

### Anti-Pattern 3: Sharing kardianos/service Stop() with CEPA signal handling

**What people do:** Call `os.Exit()` inside `Stop()`.

**Why it's wrong:** kardianos/service documentation explicitly says Stop must not call `os.Exit()`. Doing so bypasses queue drain and writer Close.

**Do this instead:** In `Stop()`, send a signal to the existing `sig` channel (or cancel the context). The existing shutdown path in `run()` handles draining and close.

### Anti-Pattern 4: BinaryEvtxWriter without chunk CRC32

**What people do:** Write BinXML content without computing the chunk CRC32 at finalisation.

**Why it's wrong:** Windows Event Viewer and all parsers (including `0xrawsec/golang-evtx`) validate chunk CRC32. A missing or wrong CRC causes the file to be rejected as corrupt.

**Do this instead:** Accumulate the chunk bytes in a buffer, compute CRC32 over bytes 8–127 (with the CRC field zeroed) using CRC32 polynomial 0xEDB88320, write the CRC back at offset 120.

---

## Integration Points Summary

| Feature | New Files | Modified Files | New Dependencies |
|---------|-----------|----------------|-----------------|
| Prometheus /metrics | `pkg/prometheus/handler.go` | `main.go`, `config.toml.example`, `go.mod` | `github.com/prometheus/client_golang` |
| Systemd unit | `deploy/systemd/cee-exporter.service` | none | none |
| Windows Service | `cmd/cee-exporter/service_windows.go`, `service_notwindows.go` | `main.go`, `go.mod` | `github.com/kardianos/service` |
| BinaryEvtxWriter | `pkg/evtx/writer_binary_evtx.go`, `pkg/evtx/binxml/*.go` | `writer_evtx_stub.go` (replaced), `go.mod` | none (stdlib only) |
| BeatsWriter | `pkg/evtx/writer_beats.go` | `main.go` (`buildWriter`), `config.toml.example`, `go.mod` | `github.com/elastic/go-lumber` |
| SyslogWriter | `pkg/evtx/writer_syslog.go` | `main.go` (`buildWriter`), `config.toml.example`, `go.mod` | `github.com/crewjam/rfc5424` |

---

## Sources

- [kardianos/service pkg.go.dev](https://pkg.go.dev/github.com/kardianos/service) — Interface, Start/Stop signatures, Run() lifecycle (HIGH confidence, official)
- [elastic/go-lumber client/v2 pkg.go.dev](https://pkg.go.dev/github.com/elastic/go-lumber/client/v2) — SyncClient.Send(), AsyncClient API (HIGH confidence, official)
- [prometheus/client_golang pkg.go.dev](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus) — NewRegistry(), CounterVec, GaugeVec (HIGH confidence, official)
- [promhttp pkg.go.dev](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp) — HandlerFor(), Handler() (HIGH confidence, official)
- [crewjam/rfc5424 pkg.go.dev](https://pkg.go.dev/github.com/crewjam/rfc5424) — RFC 5424 message writer (MEDIUM confidence, verified pkg.go.dev)
- [0xrawsec/golang-evtx GitHub](https://github.com/0xrawsec/golang-evtx) — Confirmed read-only parser; no write capability (HIGH confidence, source code reviewed)
- [libyal/libevtx EVTX format spec](https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc) — BinXML binary format documentation (HIGH confidence, authoritative spec)
- Existing codebase read directly: `main.go`, `writer.go`, `writer_gelf.go`, `writer_windows.go`, `writer_multi.go`, `writer_evtx_stub.go`, `metrics.go`, `server.go`, `health.go`, `queue.go`

---

*Architecture research for: cee-exporter v2 feature integration*
*Researched: 2026-03-03*
