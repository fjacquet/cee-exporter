# Technology Stack

**Project:** cee-exporter v2.0
**Researched:** 2026-03-03
**Milestone:** v2.0 — Operations & Output Expansion (6 new capabilities)
**Constraint:** CGO_ENABLED=0 (fully static binary, cross-compile from Linux)

---

## Existing Stack (v1.0 — DO NOT re-research)

| Technology | Version | Purpose |
|------------|---------|---------|
| Go | 1.24.0 | Runtime |
| `net/http` | stdlib | CEPA HTTP listener, TLS |
| `encoding/xml` | stdlib | CEPA XML parsing |
| `log/slog` | stdlib | Structured logging |
| `github.com/BurntSushi/toml` | v1.6.0 | Config file parsing |
| `golang.org/x/sys` | v0.31.0 | Win32 EventLog API |

---

## Recommended Stack for v2.0 New Features

### Feature 1: Prometheus /metrics Endpoint

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `github.com/prometheus/client_golang/prometheus` | v1.23.2 | Counter/gauge registration | Industry standard; only Go Prometheus library with official backing |
| `github.com/prometheus/client_golang/prometheus/promhttp` | v1.23.2 (same module) | HTTP handler for `/metrics` | Produces correct OpenMetrics/text exposition format |

**CGO compatibility:** Confirmed pure Go. No CGO dependencies anywhere in the dependency graph (prometheus/common v0.66.1+ dropped `go.uber.org/atomic` and `grafana/regexp`). Compatible with `CGO_ENABLED=0`.

**Integration with existing code:** `pkg/metrics/metrics.go` already maintains atomic counters (`EventsReceivedTotal`, `EventsDroppedTotal`, `queueDepth`). The v2 implementation wraps those atomics in `prometheus.NewCounterFunc` / `prometheus.NewGaugeFunc` callbacks, or registers them as `prometheus.Counter` / `prometheus.Gauge` directly. No rewrite of the `metrics.Store` is needed; the existing atomic values are the source of truth. The `/metrics` route is registered on the existing `net/http` server mux — no second port required unless isolation is desired.

**Packages to import:**

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)
```

**What NOT to do:** Do not register on the default `prometheus.DefaultRegisterer` if the binary is embedded; use a custom registry for testability. Do not add the full `prometheus/prometheus` server module — only `client_golang` is needed.

---

### Feature 2: Systemd Unit File (Linux Service Deployment)

**No library required.** A systemd unit file is a static text file shipped alongside the binary; it is not generated at runtime.

**Decision:** Ship a hardened unit file as `packaging/linux/cee-exporter.service`. Install instructions go in the README / `make install` target.

**Recommended unit file template:**

```ini
[Unit]
Description=Dell CEPA / CEE to EVTX/GELF exporter
Documentation=https://github.com/fjacquet/cee-exporter
After=network.target
Wants=network.target

[Service]
Type=simple
User=cee-exporter
Group=cee-exporter
WorkingDirectory=/etc/cee-exporter
ExecStart=/usr/local/bin/cee-exporter -config /etc/cee-exporter/config.toml
Restart=on-failure
RestartSec=5s
# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/log/cee-exporter

[Install]
WantedBy=multi-user.target
```

**Key decisions:**

- `Type=simple` is correct for Go daemons (no fork). Use `Type=notify` only if systemd sd_notify integration is added (not needed for v2).
- `Restart=on-failure` preferred over `Restart=always` — prevents restart loops on config errors.
- `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict` are baseline hardening with zero code cost.

**Optional sd_notify integration:** `github.com/coreos/go-systemd/daemon` can send the `READY=1` notification to enable `Type=notify`. This is a LOW priority enhancement for v2; omit unless systemd-managed startup ordering is required.

---

### Feature 3: Windows Native Service Registration

**No new external library required.** `golang.org/x/sys` is already a v1 dependency (v0.31.0). The `windows/svc` and `windows/svc/mgr` subpackages ship inside it.

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `golang.org/x/sys/windows/svc` | already in go.mod (update to v0.31.0+) | Implement `svc.Handler` interface — run as service | Official Go Windows service API; no CGO; builds with `CGO_ENABLED=0` |
| `golang.org/x/sys/windows/svc/mgr` | same module | `mgr.CreateService` / `Delete` — install/uninstall | Same module; provides `Mgr.CreateService(name, exepath, config)` |
| `golang.org/x/sys/windows/svc/eventlog` | same module | Register event source, write service events to Windows EventLog | Already used by v1 Win32 writer path |

**CGO compatibility:** Confirmed. `golang.org/x/sys/windows` uses Go's `syscall` and `unsafe` packages only. Builds without CGO. Windows cross-compile from Linux is supported (`GOOS=windows GOARCH=amd64 CGO_ENABLED=0`).

**Integration pattern:** Add `-install` and `-uninstall` flags to `cmd/cee-exporter/main.go`. When running under the SCM, the `main` function delegates to `svc.Run(serviceName, &handler{})`. Build tag `//go:build windows` on the service-specific file to avoid Linux compilation issues.

**What NOT to do:** Do not use NSSM (external executable dependency), WiX installer, or PowerShell wrapper scripts. The `x/sys/windows/svc/mgr` API does everything needed from pure Go.

---

### Feature 4: BinaryEvtxWriter — Pure-Go EVTX on Linux

**Verdict: No adequate library exists. This is a custom implementation milestone.**

Research findings:

- `github.com/0xrawsec/golang-evtx` — parser only, no write capability confirmed
- `www.velocidex.com/golang/evtx` — parser only
- `github.com/Velocidex/evtx` — parser only
- No pure-Go EVTX writer library exists in the Go ecosystem as of 2026-03

**Why it is hard:** The BinXML binary format requires: chunk headers with CRC32 checksums, fragment/template token encoding, attribute value type tables, string tables, and offset-relative binary structure — estimated 500–1,500 lines of carefully tested Go. The format specification is documented in `libyal/libevtx` (ASCIIDOC format) and Microsoft's MS-EVEN6 specification.

**Decision:** Implement from scratch in `pkg/evtx/writer_binary_evtx.go` with build tag `//go:build !windows`. The implementation:

1. Writes a valid EVTX file header (magic bytes `ElfFile\x00`, chunk count, etc.)
2. Writes one chunk per flush or per N events
3. Encodes each `WindowsEvent` struct as a BinXML fragment with a fixed schema (System + EventData elements matching Windows Security event layout)
4. Computes CRC32 checksums for each chunk

**Libraries needed for the implementation (all stdlib):**

| Package | Purpose |
|---------|---------|
| `encoding/binary` | Little-endian binary struct serialization |
| `hash/crc32` | Chunk checksum calculation (CRC32 with ANSI poly) |
| `bytes` | In-memory buffer for chunk assembly before flush |
| `os` / `io` | File write handle |
| `unicode/utf16` | BinXML stores strings as UTF-16LE |

**No external dependencies required.** The BinaryEvtxWriter must implement the existing `evtx.Writer` interface (`WriteEvent(ctx, WindowsEvent) error` + `Close() error`).

**Risk flag:** This is the highest-complexity feature in v2.0. Recommend writing a dedicated test fixture that opens the generated file with `golang-evtx` parser to validate correctness. Budget 3-5x more time than a typical writer.

---

### Feature 5: BeatsWriter — Lumberjack v2 Protocol

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `github.com/elastic/go-lumber` | v0.1.1 | Lumberjack v2 client — send events to Logstash/Graylog Beats Input | Official Elastic library; only production-grade Go Beats protocol client |

**CGO compatibility:** Pure Go. Uses only stdlib packages (`net`, `crypto/tls`, `encoding/json`, `compress/zlib`). Confirmed compatible with `CGO_ENABLED=0`.

**Maintenance status:** Last released January 2021. Stable but not actively developed. The Lumberjack v2 protocol is frozen (Elastic has not changed the wire format since Beats 6.x), so library staleness does not indicate risk. The repository has 56 stars / 38 forks and CI passes. MEDIUM confidence.

**Client package path:**

```go
import (
    lumberjack "github.com/elastic/go-lumber/client/v2"
)
```

**Integration pattern:** `BeatsWriter` in `pkg/evtx/writer_beats.go` implements `evtx.Writer`. `WriteEvent` converts `WindowsEvent` to a `map[string]interface{}` (the field format Logstash/Graylog expects), then calls `client.Send([]interface{}{event})`. Use `lumberjack.SyncClient` for simplicity (async is not needed since the queue in `pkg/queue` already decouples ingestion from writing).

**TLS support:** `go-lumber` accepts a `*tls.Config` in its dial options. The existing TLS configuration in `config.toml` can be reused.

**Alternative considered:** Implementing the Lumberjack v2 wire protocol from scratch using `encoding/binary` + `compress/zlib`. Rejected because `elastic/go-lumber` already exists, is correct, and handles acknowledgement/windowing correctly. Hand-rolling the protocol would reproduce Elastic's work without benefit.

---

### Feature 6: SyslogWriter — RFC 5424 over UDP/TCP

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `github.com/crewjam/rfc5424` | v0.1.0 | RFC 5424 message construction and serialization | Minimal, pure Go, correct RFC 5424 formatter; writes to any `io.Writer` |

**CGO compatibility:** Pure Go, stdlib only. Confirmed compatible with `CGO_ENABLED=0`.

**Design decision — transport handled by stdlib `net.Dial`:** `crewjam/rfc5424` produces correctly formatted RFC 5424 byte payloads and writes them to any `io.Writer`. The transport (UDP socket or TCP connection) is provided by `net.Conn` from the Go stdlib. This keeps the dependency minimal.

**Why NOT `log/syslog` stdlib:** The standard library's `log/syslog` package:

1. Does not compile on Windows (build constraint `//go:build !windows` is embedded). Since cee-exporter must cross-compile to Windows, importing `log/syslog` breaks the build.
2. Produces RFC 3164-style messages, not RFC 5424. RFC 5424 structured data (SD-Elements) cannot be expressed with it.

**Why NOT `nathanaelle/syslog5424`:** Explicitly refuses UDP (by design — library author opposes UDP for audit logs). cee-exporter must support UDP for compatibility with legacy syslog daemons (rsyslog, syslog-ng over UDP). Rejected.

**Why NOT `juju/rfc/rfc5424`:** Heavy dependency (pulls in full juju infrastructure). Overkill for a writer.

**Integration pattern:** `SyslogWriter` in `pkg/evtx/writer_syslog.go`. No build tags needed — `crewjam/rfc5424` and `net.Dial` compile on all platforms. The writer holds a `net.Conn` (UDP or TCP depending on config), constructs an `rfc5424.Message` per event, and calls `msg.WriteTo(conn)`.

**Config additions needed:**

```toml
[syslog]
address = "192.168.1.1:514"   # host:port
network = "udp"               # "udp" or "tcp"
app_name = "cee-exporter"
hostname = ""                  # empty = os.Hostname()
```

---

## Dependency Summary — What Changes in go.mod

```
# ADD (new for v2.0)
github.com/prometheus/client_golang v1.23.2
github.com/elastic/go-lumber v0.1.1
github.com/crewjam/rfc5424 v0.1.0

# ALREADY PRESENT (no change needed, just use more subpackages)
golang.org/x/sys   # windows/svc, windows/svc/mgr already included
```

**Features requiring ZERO new external dependencies:**

- Systemd unit file — static text artifact
- BinaryEvtxWriter — stdlib only (`encoding/binary`, `hash/crc32`, `unicode/utf16`)

---

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| Prometheus | `prometheus/client_golang` v1.23.2 | VictoriaMetrics metrics, custom text-format writer | `client_golang` is the only officially supported Go Prometheus library; alternatives lack tooling integration |
| Windows service | `golang.org/x/sys/windows/svc` | NSSM, WiX installer, kardianos/service | `x/sys` already in go.mod; pure Go; no external tools; kardianos/service is a wrapper around `x/sys` with no benefit for this use case |
| Beats protocol | `elastic/go-lumber` | Custom Lumberjack v2 implementation | Elastic's library handles ACKs, windowing, gzip, TLS correctly; no benefit to reimplementing |
| Syslog RFC 5424 | `crewjam/rfc5424` + stdlib `net` | `nathanaelle/syslog5424`, `juju/rfc/rfc5424`, `log/syslog` stdlib | `crewjam/rfc5424`: minimal, correct, cross-platform. Others: UDP exclusion, heavy deps, or Windows build failure |
| EVTX writer | Custom from scratch | `0xrawsec/golang-evtx`, `Velocidex/evtx` | No Go EVTX writer exists; all libraries are parse-only; must implement from format specification |

---

## Installation

```bash
# Add new dependencies
go get github.com/prometheus/client_golang@v1.23.2
go get github.com/elastic/go-lumber@v0.1.1
go get github.com/crewjam/rfc5424@v0.1.0
go mod tidy
```

---

## Confidence Assessment

| Feature | Library | Confidence | Notes |
|---------|---------|------------|-------|
| Prometheus /metrics | `prometheus/client_golang` v1.23.2 | HIGH | Latest version confirmed via GitHub releases page (2025-09-05); pure Go confirmed by promhttp documentation |
| Systemd unit file | No library | HIGH | Static file; no library risk |
| Windows service | `golang.org/x/sys/windows/svc` | HIGH | Already in go.mod; official Go extended stdlib; CGO_ENABLED=0 confirmed in issue reports |
| BinaryEvtxWriter | Custom (stdlib only) | MEDIUM | No writer library exists — confirmed by exhaustive search; complexity estimate based on format spec review |
| BeatsWriter | `elastic/go-lumber` v0.1.1 | MEDIUM | Only suitable library; stable but unmaintained since 2021; protocol is frozen |
| SyslogWriter | `crewjam/rfc5424` + stdlib | MEDIUM | Library is minimal and correct; last released 2020 but RFC 5424 format is stable |

---

## Sources

- [prometheus/client_golang GitHub releases](https://github.com/prometheus/client_golang/releases) — v1.23.2 confirmed 2025-09-05
- [promhttp package documentation](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp) — pure Go, no CGO confirmed
- [elastic/go-lumber GitHub](https://github.com/elastic/go-lumber) — Lumberjack v2 client/server, v0.1.1
- [go-lumber client/v2 package](https://pkg.go.dev/github.com/elastic/go-lumber/client/v2) — pure Go, TLS via stdlib crypto/tls
- [crewjam/rfc5424 GitHub](https://github.com/crewjam/rfc5424) — RFC 5424 read/write library
- [crewjam/rfc5424 pkg.go.dev](https://pkg.go.dev/github.com/crewjam/rfc5424) — v0.1.0, pure Go
- [golang.org/x/sys/windows/svc/mgr](https://pkg.go.dev/golang.org/x/sys/windows/svc/mgr) — v0.31.0, CreateService/Delete confirmed; pure Go
- [libyal/libevtx EVTX format specification](https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc) — BinXML format complexity confirmed
- [log/syslog Windows issue golang/go#66666](https://github.com/golang/go/issues/66666) — stdlib syslog not RFC 5424 compliant, does not build on Windows
- [nathanaelle/syslog5424 pkg.go.dev](https://pkg.go.dev/github.com/nathanaelle/syslog5424/v2) — explicitly no UDP support confirmed
- [hashicorp/go-syslog](https://github.com/hashicorp/go-syslog) — wrapper only, not RFC 5424, rejected
- [coreos/go-systemd GitHub](https://github.com/coreos/go-systemd) — optional sd_notify, LOW priority for v2
