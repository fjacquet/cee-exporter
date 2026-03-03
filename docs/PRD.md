# Product Requirements Document: cee-exporter

**Version:** 2.0.0
**Date:** 2026-03-03
**Status:** v1.0 Implemented — v2.0 In Progress

---

## Problem Statement

Dell PowerStore file-system audit events use the CEPA/CEE protocol, which was designed
for Windows CEE agents. Organizations running Linux-based SIEMs (Graylog, Elasticsearch,
Splunk) or unable to deploy Windows infrastructure have no native way to consume these events.

cee-exporter bridges this gap: it receives CEPA events over HTTP, maps them to Windows
Event Log semantics, and forwards them to any GELF-capable SIEM, Logstash via Beats,
syslog receivers, native Windows Event Log, or standalone `.evtx` files on Linux.

---

## Goals

- Any SIEM can ingest Dell PowerStore file-system audit events without a Windows host
- Zero runtime dependencies — a single statically-linked Go binary runs on Linux or Windows
- CEPA protocol-compliant listener handles the RegisterRequest handshake and heartbeat
  timing requirements (3-second response window)
- Operator can configure and run the daemon using only the README (no source code required)
- Native platform service integration (systemd on Linux, SCM on Windows)
- Observable via Prometheus `/metrics` endpoint

---

## Protocol Constraint (critical)

> The Dell PowerStore CEPA client sends events over **plain HTTP only**. The CEPA endpoint
> URL must always use `http://`. TLS on the cee-exporter listener port 12228 does not
> encrypt the PowerStore-to-exporter path — it is only useful when a reverse proxy sits
> in front. See [ADR-011](adr/ADR-011-tls-certificate-automation.md).

---

## Non-Goals (v1.0 — now delivered)

- ~~Binary .evtx file generation on Linux~~ (implemented in v2 — see ADR-009)
- ~~Prometheus /metrics endpoint~~ (implemented in v2)
- ~~Windows Service installer / systemd unit file~~ (implemented in v2)

## Non-Goals (v2.0)

- High-availability load-balancer configuration (operational concern)
- CAVA antivirus event processing (out of scope)
- RPC/MSRPC transport (Windows-only, significant complexity)
- BinaryEvtxWriter cross-event template sharing (OUT-F01 — future minor release)
- EVTX rolling/chunked streaming mid-chunk (flush-on-close is the v2 approach)
- DNS-01 ACME challenge via go-acme/lego (deferred; self-signed covers air-gapped)

---

## User Personas

**Linux sysadmin (primary)**

- Runs Graylog on Linux; needs PowerStore NAS file-system audit events without Windows
- Configures cee-exporter with `type = "gelf"` and directs it at their Graylog GELF input
- Uses systemd unit for lifecycle management; scrapes `/metrics` with Prometheus

**SOC analyst / SIEM engineer**

- Needs PowerStore events in Logstash/Elastic SIEM via Beats protocol
- Configures `type = "beats"` for Lumberjack v2 transport with TLS
- Alternatively uses `type = "syslog"` for RFC 5424 forwarding to rsyslog or syslog-ng

**Windows sysadmin (secondary)**

- Uses native Windows Event Viewer or Winlogbeat
- Installs cee-exporter.exe as a Windows service: `cee-exporter.exe install`
- Win32 EventLog writer activates automatically on Windows
- Linux-generated `.evtx` files can be opened in Event Viewer (via BinaryEvtxWriter)

---

## Functional Requirements

Full requirement list: see [REQUIREMENTS.md](.planning/REQUIREMENTS.md)

### v1.0 (delivered)

- CEPA protocol: CEPA-01 through CEPA-05
- Semantic mapping: MAP-01 through MAP-06
- GELF output: GELF-01 through GELF-04
- Win32 output: WIN-01, WIN-02
- Multi-backend: MULTI-01
- TLS: TLS-01, TLS-02 (manual cert)
- Observability: OBS-01 through OBS-03 (health endpoint, structured logs)
- Quality: QUAL-01 through QUAL-06
- Build: BUILD-01, BUILD-02
- Documentation: DOC-01 through DOC-04

### v2.0 (in progress)

| ID | Requirement | Phase | ADR |
|----|-------------|-------|-----|
| OBS-04 | Prometheus `/metrics` endpoint on port 9228 | 04 | ADR-006 |
| DEPLOY-01 | systemd unit file (Linux) | 04 | — |
| DEPLOY-02 | `/health` and `/metrics` survive service restart | 04 | — |
| DEPLOY-03 | `cee-exporter.exe install` registers with Windows SCM | 05 | ADR-010 |
| DEPLOY-04 | `cee-exporter.exe uninstall` removes SCM registration | 05 | ADR-010 |
| DEPLOY-05 | Windows Service auto-restarts after crash | 05 | ADR-010 |
| OUT-01 | BeatsWriter: Lumberjack v2 to Logstash / Graylog | 06 | — |
| OUT-02 | BeatsWriter supports TLS | 06 | — |
| OUT-03 | SyslogWriter: RFC 5424 over UDP | 06 | ADR-008 |
| OUT-04 | SyslogWriter: RFC 5424 over TCP (octet-counting) | 06 | ADR-008 |
| OUT-05 | BinaryEvtxWriter: native `.evtx` on Linux | 07 | ADR-009 |
| OUT-06 | Generated `.evtx` opens in Windows Event Viewer | 07 | ADR-009 |
| TLS-03 | `tls_mode="acme"` auto-provisions via Let's Encrypt | 08 | ADR-011 |
| TLS-04 | `tls_mode="self-signed"` for air-gapped deployments | 08 | ADR-011 |

---

## Non-Functional Requirements

- **Latency:** HTTP handler must ACK within 3 seconds (CEPA heartbeat constraint)
- **Throughput:** Queue capacity 100,000 events default; handles VCAPS batches of thousands per PUT
- **Portability:** Single binary; CGO_ENABLED=0; compiles for linux/amd64 and windows/amd64
- **Reliability:** TCP GELF/Beats reconnects automatically; failed backend does not block others (MultiWriter)
- **Observability:** Health endpoint, Prometheus metrics, and structured logs on every received batch
- **Security:** All new writer transport supports TLS; CEPA listener TLS documented with protocol caveat

---

## Architecture Summary

```text
CEPA HTTP PUT → pkg/server → pkg/parser → pkg/mapper → pkg/queue → pkg/evtx (writers)
                                                                  ↓
                                                         pkg/metrics → /metrics (Prometheus)
```

- **server:** HTTP handler; ACKs immediately; enqueues events
- **parser:** CEE XML → []CEPAEvent
- **mapper:** CEPAEvent → WindowsEvent (CEPA type → Windows EventID + access mask)
- **queue:** Async worker pool; drops events on overflow with WARN log
- **evtx writers:**
  - `GELFWriter` — GELF 1.1 UDP/TCP (all platforms)
  - `Win32EventLogWriter` — Win32 ReportEvent (Windows only)
  - `SyslogWriter` — RFC 5424 UDP/TCP (all platforms) [v2]
  - `BeatsWriter` — Lumberjack v2 TCP/TLS (all platforms) [v2]
  - `BinaryEvtxWriter` — native .evtx files (non-Windows) [v2]
  - `MultiWriter` — fan-out to multiple backends
- **platform service:** systemd unit (Linux) / Windows SCM via kardianos/service [v2]

For architectural decisions, see `docs/adr/`.

---

## v2.0 Dependency Changes

| Package | Version | Purpose | Notes |
|---------|---------|---------|-------|
| `github.com/kardianos/service` | v1.2.4 | Windows SCM integration | Supersedes x/sys direct (ADR-010) |
| `github.com/crewjam/rfc5424` | v0.1.0 | SyslogWriter RFC 5424 messages | CGO-free (ADR-008) |
| `github.com/elastic/go-lumber` | v0.1.1 | BeatsWriter Lumberjack v2 | CGO-free |
| `github.com/prometheus/client_golang` | v1.23.2 | Prometheus /metrics | CGO-free (ADR-006) |
| `golang.org/x/crypto` (promoted) | v0.48.0 | ACME autocert (TLS-ALPN-01) | Was indirect dep (ADR-011) |

**No new dependencies for:** BinaryEvtxWriter (stdlib only), systemd unit (text artifact).

---

## Success Metrics

- `go test ./...` passes with zero failures on Linux and Windows
- `make build` and `make build-windows` produce runnable binaries
- An operator can follow the README quickstart and see events in Graylog within 15 minutes
- `cee-exporter.exe install` registers a service that survives reboot and restarts on failure
- `.evtx` files generated on Linux open correctly in Windows Event Viewer
- `curl :9228/metrics` returns Prometheus-formatted counters
