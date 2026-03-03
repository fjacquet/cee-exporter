# Phase 4: Observability & Linux Service - Research

**Researched:** 2026-03-03
**Domain:** Prometheus /metrics endpoint (prometheus/client_golang), systemd unit file hardening
**Confidence:** HIGH

---

<phase_requirements>

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| OBS-01 | Operator can scrape `cee_events_received_total` counter from `/metrics` endpoint | `pkg/metrics.M.EventsReceivedTotal` atomic wrapped via `NewCounterFunc`; registered in custom registry |
| OBS-02 | Operator can scrape `cee_events_dropped_total` counter from `/metrics` endpoint | `pkg/metrics.M.EventsDroppedTotal` atomic wrapped via `NewCounterFunc`; registered in custom registry |
| OBS-03 | Operator can scrape `cee_queue_depth` gauge from `/metrics` endpoint | `pkg/metrics.M.QueueDepth()` wrapped via `NewGaugeFunc`; registered in custom registry |
| OBS-04 | Operator can scrape `cee_writer_errors_total` counter from `/metrics` endpoint | `pkg/metrics.M.WriterErrorsTotal` atomic wrapped via `NewCounterFunc`; registered in custom registry |
| OBS-05 | `/metrics` endpoint is served on a configurable dedicated port (default 9228, separate from CEPA port 12228) | Separate `http.ServeMux` + `http.Server` on 9228; guarded by `cfg.Metrics.Enabled`; runs in own goroutine |
| DEPLOY-01 | Linux operator is provided a hardened systemd unit file for daemon management | `deploy/systemd/cee-exporter.service` with `Type=simple`, `ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`, `Restart=on-failure` |
| DEPLOY-02 | Linux operator can `systemctl enable --now cee-exporter` to auto-start at boot with auto-restart on failure | `WantedBy=multi-user.target` enables auto-start; `Restart=on-failure` + `RestartSec=5s` provides auto-restart; `make install-systemd` copies unit file |
</phase_requirements>

---

## Summary

Phase 4 delivers two independent but operationally coupled features: a Prometheus `/metrics` endpoint and a hardened systemd unit file. Both are low-risk, high-value items — the Prometheus handler wraps atomics already present in `pkg/metrics` (no new counters, no state duplication), and the systemd unit file is a pure text artifact requiring zero Go code changes.

The Prometheus endpoint runs on a **dedicated port 9228**, isolated from the CEPA mux on port 12228. This is the industry-standard pattern for Go exporters: it avoids TLS scrape configuration complexity, prevents scrape log pollution in CEPA logs, and eliminates HTTP method conflicts. A custom `prometheus.NewRegistry()` (not the DefaultRegisterer) is used, which avoids Go runtime metrics pollution in the scrape output and makes the handler testable in isolation.

The systemd unit file uses `Type=simple` (correct for all Go HTTP daemons — no fork), `network-online.target` ordering (the daemon connects to a syslog/GELF/Beats target on startup), and a hardened sandbox (`ProtectSystem=strict`, `ReadWritePaths=/var/log/cee-exporter`, `NoNewPrivileges`, `PrivateTmp`). The `Makefile` gains a `make install-systemd` target that copies the unit file and reloads the daemon.

Phase 4 also performs the `main()` → `run()` extraction refactor in `main.go`. This refactor is not visible to operators but is a prerequisite for Phase 5 (Windows Service), which needs `run()` to exist as a callable function that can be wrapped by `kardianos/service`.

**Primary recommendation:** Implement in this order: (1) `main()` → `run()` refactor, (2) add `MetricsConfig` to `Config`, (3) implement `pkg/prometheus/handler.go`, (4) wire metrics server in `main.go`, (5) create systemd unit file, (6) add `make install-systemd` target.

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/prometheus/client_golang/prometheus` | v1.23.2 | Counter/gauge registration, custom registry | Only officially backed Go Prometheus library; 26,364 packages import promhttp |
| `github.com/prometheus/client_golang/prometheus/promhttp` | v1.23.2 (same module) | HTTP handler exposing `/metrics` in Prometheus text format | Produces correct OpenMetrics/text exposition format; `HandlerFor` supports custom registry |
| stdlib `net/http` | Go 1.24 (existing) | Second HTTP server on port 9228 | Already used for CEPA listener; no new dependency |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| stdlib `sync/atomic` (via `pkg/metrics`) | Go 1.24 (existing) | Atomic counters already maintained | These ARE the source of truth; wrap them, don't duplicate |
| `golang.org/x/sys` | v0.41.0 (existing in go.mod) | No direct use in Phase 4; present for build compatibility | Phase 5 uses it for Windows Service |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Custom `prometheus.NewRegistry()` | `prometheus.DefaultRegisterer` | DefaultRegisterer includes Go runtime metrics (GC, goroutines) which pollute scrape output and are not useful for this daemon; also harder to test |
| `NewCounterFunc` wrapping existing atomics | New `prometheus.Counter` objects incremented alongside atomics | Dual-state causes divergence; the atomic in `pkg/metrics.Store` is already the single source of truth — wrap it, never duplicate |
| Separate port 9228 | Register on CEPA mux 12228 | CEPA mux may be TLS-only, forcing Prometheus scraper TLS config; scrape requests pollute CEPA access logs; `RegisterRequest` check runs on every request |

**Installation:**

```bash
go get github.com/prometheus/client_golang@v1.23.2
go mod tidy
```

---

## Architecture Patterns

### Recommended Project Structure

```
pkg/
  prometheus/
    handler.go        # MetricsHandler: custom registry + promhttp.HandlerFor

cmd/cee-exporter/
  main.go             # modified: MetricsConfig, run() extraction, metrics server goroutine

deploy/
  systemd/
    cee-exporter.service    # new: hardened unit file

Makefile              # add: install-systemd target
config.toml.example   # add: [metrics] section
```

### Pattern 1: Custom Registry with CounterFunc/GaugeFunc Wrapping Atomics

**What:** Create a private `prometheus.Registry`, register `NewCounterFunc` and `NewGaugeFunc` instances that read from the existing `pkg/metrics.Store` atomics at collection time. Serve via `promhttp.HandlerFor`.

**When to use:** Always — this is the only correct pattern given `pkg/metrics.Store` is the canonical source of truth.

**Example:**

```go
// Source: pkg.go.dev/github.com/prometheus/client_golang@v1.23.2/prometheus
// pkg/prometheus/handler.go

package prometheus

import (
    "net/http"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"

    "github.com/fjacquet/cee-exporter/pkg/metrics"
)

// NewMetricsHandler returns an http.Handler that serves Prometheus metrics
// for the cee-exporter. It uses a private registry to exclude Go runtime
// collectors from the scrape output.
func NewMetricsHandler() http.Handler {
    reg := prometheus.NewRegistry()

    reg.MustRegister(prometheus.NewCounterFunc(
        prometheus.CounterOpts{
            Name: "cee_events_received_total",
            Help: "Total number of CEPA events received by the HTTP handler.",
        },
        func() float64 { return float64(metrics.M.EventsReceivedTotal.Load()) },
    ))

    reg.MustRegister(prometheus.NewCounterFunc(
        prometheus.CounterOpts{
            Name: "cee_events_dropped_total",
            Help: "Total number of events dropped due to queue overflow.",
        },
        func() float64 { return float64(metrics.M.EventsDroppedTotal.Load()) },
    ))

    reg.MustRegister(prometheus.NewCounterFunc(
        prometheus.CounterOpts{
            Name: "cee_writer_errors_total",
            Help: "Total number of errors returned by the event writer.",
        },
        func() float64 { return float64(metrics.M.WriterErrorsTotal.Load()) },
    ))

    reg.MustRegister(prometheus.NewGaugeFunc(
        prometheus.GaugeOpts{
            Name: "cee_queue_depth",
            Help: "Current number of events waiting in the async queue.",
        },
        func() float64 { return float64(metrics.M.QueueDepth()) },
    ))

    return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// Serve starts an HTTP server on addr that exposes /metrics.
// It blocks until the server exits. Run in a goroutine.
func Serve(addr string) error {
    mux := http.NewServeMux()
    mux.Handle("/metrics", NewMetricsHandler())
    srv := &http.Server{
        Addr:    addr,
        Handler: mux,
    }
    return srv.ListenAndServe()
}
```

### Pattern 2: MetricsConfig struct in main.go and conditional start

**What:** Add `MetricsConfig` to the top-level `Config` struct, default `Enabled=true`, `Addr="0.0.0.0:9228"`. Start metrics server in a goroutine only if `Enabled`.

**When to use:** Always for the config wiring.

**Example:**

```go
// Source: architecture from .planning/research/ARCHITECTURE.md
// cmd/cee-exporter/main.go additions

type MetricsConfig struct {
    Enabled bool   `toml:"enabled"`
    Addr    string `toml:"addr"` // default "0.0.0.0:9228"
}

// In defaultConfig():
Metrics: MetricsConfig{
    Enabled: true,
    Addr:    "0.0.0.0:9228",
},

// In run() after CEPA server starts:
if cfg.Metrics.Enabled {
    go func() {
        if err := ceeprometheus.Serve(cfg.Metrics.Addr); err != nil && err != http.ErrServerClosed {
            slog.Error("metrics_server_error", "error", err)
        }
    }()
    slog.Info("metrics_server_started", "addr", cfg.Metrics.Addr)
}
```

### Pattern 3: main() → run() extraction for Windows Service prerequisite

**What:** Move the entire body of `main()` into a `run()` function. `main()` becomes a one-liner that calls `runWithServiceManager(run)`. The non-Windows shim implements `runWithServiceManager(fn func()) { fn() }`.

**When to use:** Must be done in Phase 4 so Phase 5 can add `service_windows.go` without touching core logic.

**Example:**

```go
// Source: architecture from .planning/research/ARCHITECTURE.md
// cmd/cee-exporter/main.go

func main() {
    runWithServiceManager(run)
}

// run contains all existing main() logic verbatim.
func run() {
    cfgPath := flag.String("config", "config.toml", "path to TOML configuration file")
    // ... rest of existing main() body ...
}
```

```go
// cmd/cee-exporter/service_notwindows.go
//go:build !windows

package main

// runWithServiceManager runs the program directly on non-Windows platforms.
func runWithServiceManager(runFn func()) {
    runFn()
}
```

### Pattern 4: Systemd Unit File — hardened Type=simple for Go daemon

**What:** Static text file at `deploy/systemd/cee-exporter.service`. Uses `Type=simple` (Go HTTP daemons do not fork), `network-online.target` (daemon connects out to GELF/syslog targets on startup), filesystem sandbox.

**When to use:** Ship with release. Operator copies to `/etc/systemd/system/` and runs `systemctl enable --now cee-exporter`.

**Example:**

```ini
# Source: systemd.service man page + .planning/research/STACK.md + .planning/research/ARCHITECTURE.md
# deploy/systemd/cee-exporter.service

[Unit]
Description=Dell PowerStore CEPA to Windows Event Log bridge
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

# Filesystem hardening
ProtectSystem=strict
ReadWritePaths=/var/log/cee-exporter
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

### Pattern 5: Makefile install-systemd target

**What:** Add a `make install-systemd` target that copies the unit file to `/etc/systemd/system/` and reloads the daemon.

**Example:**

```makefile
SYSTEMD_UNIT_SRC := deploy/systemd/cee-exporter.service
SYSTEMD_UNIT_DST := /etc/systemd/system/cee-exporter.service

install-systemd: $(SYSTEMD_UNIT_SRC)
 install -m 644 $(SYSTEMD_UNIT_SRC) $(SYSTEMD_UNIT_DST)
 systemctl daemon-reload
 @echo "Unit installed. Run: systemctl enable --now cee-exporter"
```

### Anti-Patterns to Avoid

- **Registering /metrics on the CEPA mux (port 12228):** The CEPA mux is TLS-optional; scrapes would need TLS client config. Every scrape request triggers the CEPA handler's `RegisterRequest` check path. Scrape requests pollute CEPA access logs. Use a separate mux on port 9228.
- **Using `prometheus.DefaultRegisterer`:** The default registry includes Go runtime metrics (GC pause, goroutine count, heap stats) which are irrelevant to this daemon and inflate scrape payload. Use `prometheus.NewRegistry()` which starts empty.
- **Creating separate `prometheus.Counter` objects alongside existing atomics:** Two independent counters diverge under concurrent writes. The `pkg/metrics.Store` atomics are the only counter state. Wrap them with `NewCounterFunc`; do not maintain parallel state.
- **`Type=forking` or `Type=notify` in systemd unit:** Go daemons do not fork — `Type=simple` is correct. `Type=notify` requires `sd_notify` integration in Go code (deferred to OBS-F02 in future requirements).
- **Using cardinality-generating labels:** Do not add `ObjectName`, `SubjectUsername`, or similar high-cardinality fields as Prometheus label values. Use only low-cardinality labels (`event_id`, `writer`, `status`) if labels are needed at all. Phase 4 metrics have no labels.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Prometheus text exposition format | Custom `/metrics` text serializer | `promhttp.HandlerFor(reg, opts)` | OpenMetrics encoding, content negotiation, compression, error handling all handled |
| Counter/gauge metric types | Custom struct with mutex and text output | `prometheus.NewCounterFunc` / `NewGaugeFunc` + `Registry.MustRegister` | Type safety, naming conventions, HELP/TYPE metadata headers, Go concurrency guarantees |
| Systemd unit validation | Manual format checking | Ship and `systemd-analyze verify deploy/systemd/cee-exporter.service` | Systemd's own validator catches syntax errors before production |

**Key insight:** `prometheus/client_golang` handles all exposition format concerns (content negotiation, OpenMetrics, gzip compression, error propagation). There is no scenario where hand-rolling the text format is better than the library.

---

## Common Pitfalls

### Pitfall 1: Prometheus Mux Collision

**What goes wrong:** Registering `/metrics` on the CEPA `http.ServeMux` (port 12228) causes 404 when CEPA TLS is enabled (scraper without client cert gets TLS error), or 405 when the CEPA handler rejects GET methods, or log pollution from scrape requests.
**Why it happens:** Developers reach for the existing server variable as the obvious integration point.
**How to avoid:** Create a dedicated `http.ServeMux` and `http.Server` in `pkg/prometheus`, bind to port 9228, start in its own goroutine. The two servers are completely independent.
**Warning signs:** Prometheus scrape returning 404 or TLS handshake errors; CEPA logs showing `/metrics` GET requests.

### Pitfall 2: Parallel Counter Drift (DefaultRegisterer + own atomics)

**What goes wrong:** If you register a `prometheus.Counter` on DefaultRegisterer AND also maintain `pkg/metrics.Store.EventsReceivedTotal` as a separate atomic, the two values diverge immediately under concurrent writes because they are incremented by different code paths.
**Why it happens:** Temptation to use `promauto.NewCounter` (auto-registers on DefaultRegisterer) for convenience, then forget the existing `pkg/metrics` atomics are also being incremented.
**How to avoid:** `NewCounterFunc` wraps the existing atomic. The atomic is always the value. The Prometheus callback just reads it. One source of truth.
**Warning signs:** `cee_events_received_total` in Prometheus scrape is lower than actual event count; discrepancy grows over time.

### Pitfall 3: Cardinality Explosion via Labels

**What goes wrong:** Adding `event_id` as a label with `prometheus.Labels{"event_id": strconv.Itoa(e.EventID)}` on a CounterVec. EventIDs are a small bounded set (3-4 values), so this is acceptable. But `ObjectName` or `SubjectUsername` as labels creates millions of distinct metric series.
**Why it happens:** Over-instrumentation driven by "more is better" thinking.
**How to avoid:** Phase 4 metrics have NO labels — they are plain counters and gauges. If per-EventID counters are wanted in future, use a `CounterVec` with ONLY `event_id` as a label dimension.
**Warning signs:** Prometheus scrape payload growing unboundedly; Prometheus server OOM.

### Pitfall 4: Blocking Collect in CounterFunc

**What goes wrong:** The `func() float64` passed to `NewCounterFunc` does expensive work (I/O, locking, network call). Prometheus calls this from the HTTP handler goroutine during `/metrics` scrape. Blocking the scrape blocks the Prometheus scraper timeout.
**Why it happens:** Developers put non-trivial logic in the callback.
**How to avoid:** The callback must only call `.Load()` on `pkg/metrics.Store` atomics — a nanosecond-scale operation. No I/O, no mutexes, no allocation.
**Warning signs:** Prometheus scrape timeout errors; `/metrics` requests taking > 1 second.

### Pitfall 5: Systemd Type=forking or Type=notify

**What goes wrong:** Using `Type=forking` causes systemd to wait indefinitely for a parent process to exit (Go daemons don't fork). Using `Type=notify` requires the Go binary to send `READY=1` via sd_notify socket, which is not wired in Phase 4 code — the service will be killed by systemd after `TimeoutStartSec` expires.
**Why it happens:** Copy-paste from old unit file templates; confusion with traditional C daemons.
**How to avoid:** Use `Type=simple`. systemd considers the service running immediately when the process starts. This is correct for Go's goroutine-based server model.
**Warning signs:** Service shows `activating` state indefinitely; `journalctl -u cee-exporter` shows no output.

### Pitfall 6: Missing network-online.target vs network.target

**What goes wrong:** Using `After=network.target` means the service starts as soon as the network stack is initialized, before any interfaces have IP addresses. If cee-exporter connects to a GELF/syslog target at startup, the connection will fail.
**Why it happens:** `network.target` is commonly copy-pasted but is subtly wrong for services that make outbound connections.
**How to avoid:** Use `After=network-online.target` + `Wants=network-online.target`. This waits until at least one network interface has a routable IP address. (Source: systemd.io/NETWORK_ONLINE/)
**Warning signs:** Service fails immediately at boot with "connection refused" but works after a manual `systemctl restart cee-exporter`.

### Pitfall 7: ProtectSystem=strict Without ReadWritePaths

**What goes wrong:** `ProtectSystem=strict` mounts the entire filesystem read-only. If cee-exporter writes logs to `/var/log/cee-exporter` or EVTX files to a path on the filesystem, those writes fail with "read-only file system" error.
**Why it happens:** Enabling strict hardening without auditing what the process actually writes.
**How to avoid:** Add `ReadWritePaths=/var/log/cee-exporter` (or wherever log/evtx output goes). The EnvironmentFile `-/etc/cee-exporter/env` is read-only — that is correct. Only write paths need `ReadWritePaths`.
**Warning signs:** `journalctl -u cee-exporter` shows "read-only file system" errors immediately after service start.

---

## Code Examples

Verified patterns from official sources:

### Full MetricsHandler implementation

```go
// Source: pkg.go.dev/github.com/prometheus/client_golang@v1.23.2 (official docs)
// pkg/prometheus/handler.go

package prometheus

import (
    "net/http"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"

    "github.com/fjacquet/cee-exporter/pkg/metrics"
)

func NewMetricsHandler() http.Handler {
    reg := prometheus.NewRegistry()

    reg.MustRegister(prometheus.NewCounterFunc(
        prometheus.CounterOpts{
            Name: "cee_events_received_total",
            Help: "Total CEPA events received by the HTTP handler.",
        },
        func() float64 { return float64(metrics.M.EventsReceivedTotal.Load()) },
    ))

    reg.MustRegister(prometheus.NewCounterFunc(
        prometheus.CounterOpts{
            Name: "cee_events_dropped_total",
            Help: "Total events dropped due to queue overflow.",
        },
        func() float64 { return float64(metrics.M.EventsDroppedTotal.Load()) },
    ))

    reg.MustRegister(prometheus.NewCounterFunc(
        prometheus.CounterOpts{
            Name: "cee_writer_errors_total",
            Help: "Total errors returned by the event writer.",
        },
        func() float64 { return float64(metrics.M.WriterErrorsTotal.Load()) },
    ))

    reg.MustRegister(prometheus.NewGaugeFunc(
        prometheus.GaugeOpts{
            Name: "cee_queue_depth",
            Help: "Current number of events waiting in the async queue.",
        },
        func() float64 { return float64(metrics.M.QueueDepth()) },
    ))

    return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

func Serve(addr string) error {
    mux := http.NewServeMux()
    mux.Handle("/metrics", NewMetricsHandler())
    return (&http.Server{Addr: addr, Handler: mux}).ListenAndServe()
}
```

### Config.toml additions

```toml
# Prometheus /metrics endpoint (operator telemetry)
[metrics]
enabled = true
addr    = "0.0.0.0:9228"
```

### Hardened systemd unit file

```ini
# Source: systemd.service man page; systemd.io/NETWORK_ONLINE/; linux-audit.com/systemd/settings/units/protectsystem/
# deploy/systemd/cee-exporter.service

[Unit]
Description=Dell PowerStore CEPA to Windows Event Log bridge
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

# Filesystem hardening
ProtectSystem=strict
ReadWritePaths=/var/log/cee-exporter
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

### Unit file validation command

```bash
# Run before committing the unit file
systemd-analyze verify deploy/systemd/cee-exporter.service
```

### Test: verify /metrics endpoint

```go
// White-box test in pkg/prometheus/handler_test.go
// Tests that all 4 required metrics are served with correct names
func TestMetricsHandler_AllRequiredMetrics(t *testing.T) {
    // Reset metrics store
    metrics.M.EventsReceivedTotal.Store(42)
    metrics.M.EventsDroppedTotal.Store(7)
    metrics.M.WriterErrorsTotal.Store(1)
    metrics.M.SetQueueDepth(15)

    h := NewMetricsHandler()
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    w := httptest.NewRecorder()
    h.ServeHTTP(w, req)

    body := w.Body.String()
    for _, want := range []string{
        "cee_events_received_total 42",
        "cee_events_dropped_total 7",
        "cee_writer_errors_total 1",
        "cee_queue_depth 15",
    } {
        if !strings.Contains(body, want) {
            t.Errorf("missing metric line %q in response body", want)
        }
    }
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Register on DefaultRegisterer | Custom `prometheus.NewRegistry()` | Best practice established ~2020; now documented in official examples | No Go runtime metrics in scrape; fully testable |
| `promauto.NewCounter` (auto-registers) | `NewCounterFunc` wrapping existing atomic | Documented pattern in client_golang examples 2023+ | Single source of truth; no counter divergence risk |
| `Type=notify` for Go daemons | `Type=simple` (unless sd_notify explicitly wired) | Always correct; `Type=notify` requires explicit `sd_notify()` integration | No accidental startup timeout |
| `After=network.target` | `After=network-online.target` + `Wants=network-online.target` | Official systemd docs clarified 2020 (systemd.io/NETWORK_ONLINE) | Prevents startup failure on boot when network not yet up |
| `ProtectSystem=full` | `ProtectSystem=strict` + `ReadWritePaths=` | `strict` available since systemd 232 (all relevant distros) | Stricter isolation; /etc also read-only in strict mode |

**Deprecated/outdated:**

- `prometheus/client_golang` before v1.23.2: Avoid — older versions had `go.uber.org/atomic` and `grafana/regexp` transitive deps that break `CGO_ENABLED=0`. Version v1.23.2 (released 2025-09-05) drops these.
- `Type=forking` for Go daemons: Never correct for Go. Go's HTTP server goroutine model is incompatible with fork semantics.

---

## Open Questions

1. **cee_events_written_total: include or exclude?**
   - What we know: `pkg/metrics.Store` has `EventsWrittenTotal` but it is not listed in OBS-01 through OBS-04 requirements.
   - What's unclear: Requirements only specify 4 metrics (received, dropped, queue_depth, writer_errors). Written is available but not required.
   - Recommendation: Include `cee_events_written_total` anyway — it is free (one more `NewCounterFunc` line) and operationally useful for computing success rate as `written / received`. Document it in the `[metrics]` config section as bonus coverage beyond requirements.

2. **Metrics server graceful shutdown**
   - What we know: The CEPA server has `httpServer.Shutdown(shutdownCtx)` in the existing `run()` function. The metrics server is a separate `http.Server` instance.
   - What's unclear: The current `Serve()` design does not return the server instance, making graceful shutdown of the metrics server impossible from `run()`.
   - Recommendation: Refactor `Serve` to accept a `context.Context` and call `srv.Shutdown(ctx)` when the context is cancelled. This keeps the metrics server lifecycle aligned with the CEPA server. Alternatively, leave it fire-and-forget (acceptable since metrics port has no stateful connections).

3. **User/group creation for systemd unit**
   - What we know: The unit file specifies `User=cee-exporter` / `Group=cee-exporter`. The `make install-systemd` target must create this user or document that the operator must create it.
   - What's unclear: Whether to include `useradd` in the Makefile or only document it.
   - Recommendation: Document the command in a comment in the unit file and in `make install-systemd` output. Do not auto-create users from Make — that requires sudo and is outside make's responsibility.

---

## Implementation Checklist for Planner

The planner should produce tasks covering these concrete changes:

**Go changes (main.go):**

- [ ] Extract `main()` body into `run()` function
- [ ] Add `runWithServiceManager(run)` call in `main()`
- [ ] Add `MetricsConfig` struct to `Config`
- [ ] Add `Metrics MetricsConfig` field to `Config` struct
- [ ] Add defaults `Enabled: true, Addr: "0.0.0.0:9228"` to `defaultConfig()`
- [ ] Add metrics server goroutine in `run()` after CEPA server starts

**New Go file:**

- [ ] Create `pkg/prometheus/handler.go` with `NewMetricsHandler()` and `Serve(addr string) error`
- [ ] Create `cmd/cee-exporter/service_notwindows.go` with `//go:build !windows` shim

**New Go test:**

- [ ] Create `pkg/prometheus/handler_test.go` verifying all 4 required metric names appear in scrape output

**Non-Go files:**

- [ ] Create `deploy/systemd/cee-exporter.service`
- [ ] Update `config.toml.example` with `[metrics]` section
- [ ] Add `install-systemd` target to `Makefile`

**go.mod:**

- [ ] `go get github.com/prometheus/client_golang@v1.23.2`
- [ ] `go mod tidy`

---

## Sources

### Primary (HIGH confidence)

- `pkg.go.dev/github.com/prometheus/client_golang@v1.23.2/prometheus` — NewCounterFunc, NewGaugeFunc, NewRegistry, MustRegister signatures and examples; confirmed 2026-03-03
- `pkg.go.dev/github.com/prometheus/client_golang@v1.23.2/prometheus/promhttp` — HandlerFor signature, HandlerOpts fields; confirmed 2026-03-03
- Direct codebase inspection: `pkg/metrics/metrics.go` — atomic field names, types, QueueDepth() method signature; `cmd/cee-exporter/main.go` — existing Config struct, buildWriter(), signal handling pattern
- `.planning/research/ARCHITECTURE.md` — MetricsConfig struct, pkg/prometheus/handler.go design, run() extraction rationale
- `.planning/research/STACK.md` — prometheus/client_golang v1.23.2 CGO_ENABLED=0 compatibility confirmation; systemd unit file pattern

### Secondary (MEDIUM confidence)

- `systemd.io/NETWORK_ONLINE/` — network-online.target semantics; Wants= vs After= distinction; verified 2026-03-03
- `linux-audit.com/systemd/settings/units/protectsystem/` — ProtectSystem=strict semantics, ReadWritePaths interaction; verified 2026-03-03
- WebSearch "systemd unit hardening 2025" — confirmed NoNewPrivileges, PrivateTmp, ProtectSystem=strict as current baseline for hardened services

### Tertiary (LOW confidence)

- Metrics server graceful shutdown design — not verified against a specific code pattern; recommendation is based on Go http.Server Shutdown API pattern (well-established)

---

## Metadata

**Confidence breakdown:**

- Standard stack (prometheus/client_golang v1.23.2): HIGH — verified at pkg.go.dev, version confirmed 2025-09-05
- Architecture (custom registry + CounterFunc wrapping): HIGH — confirmed against official prometheus client_golang examples
- Pitfalls (mux collision, counter drift, cardinality): HIGH — derived from codebase inspection + official library design constraints
- Systemd unit file: HIGH — Type=simple, network-online.target, ProtectSystem=strict all verified against official systemd documentation
- Metrics server graceful shutdown: MEDIUM — pattern is well-known but exact implementation not validated against existing shutdown code

**Research date:** 2026-03-03
**Valid until:** 2026-06-03 (prometheus/client_golang API is stable; systemd unit patterns are stable)
