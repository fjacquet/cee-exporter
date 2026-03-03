# Product Requirements Document: cee-exporter

**Version:** 1.0.0
**Date:** 2026-03-03
**Status:** Implemented

---

## Problem Statement

Dell PowerStore file-system audit events use the CEPA/CEE protocol, which was designed
for Windows CEE agents. Organizations running Linux-based SIEMs (Graylog, Elasticsearch)
or unable to deploy Windows infrastructure have no native way to consume these events.

cee-exporter bridges this gap: it receives CEPA events over HTTP, maps them to Windows
Event Log semantics, and forwards them to any GELF-capable SIEM or to the native Windows
Event Log.

---

## Goals

- Any SIEM can ingest Dell PowerStore file-system audit events without a Windows host
- Zero runtime dependencies — a single statically-linked Go binary runs on Linux or Windows
- CEPA protocol-compliant listener handles the RegisterRequest handshake and heartbeat
  timing requirements (3-second response window)
- Operator can configure and run the daemon using only the README (no source code required)

---

## Non-Goals (v1.0)

- Binary .evtx file generation on Linux (deferred to v2 — see ADR-004)
- Prometheus /metrics endpoint (v2)
- Windows Service installer / systemd unit file (v2)
- High-availability load-balancer configuration (operational concern)
- CAVA antivirus event processing (out of scope)
- RPC/MSRPC transport (Windows-only, adds significant complexity)

---

## User Personas

**Linux sysadmin (primary)**
- Runs Graylog on Linux
- Needs PowerStore NAS file-system audit events in Graylog without deploying Windows
- Configures cee-exporter with `type = "gelf"` and directs it at their Graylog GELF input

**Windows sysadmin (secondary)**
- Uses native Windows Event Viewer or Winlogbeat
- Deploys cee-exporter.exe on a Windows host
- Win32 EventLog writer activates automatically on Windows

---

## Functional Requirements

Full requirement list: see [REQUIREMENTS.md](.planning/REQUIREMENTS.md)

Summary of v1.0 scope:
- CEPA protocol: CEPA-01 through CEPA-05
- Semantic mapping: MAP-01 through MAP-06
- GELF output: GELF-01 through GELF-04
- Win32 output: WIN-01, WIN-02
- Multi-backend: MULTI-01
- TLS: TLS-01, TLS-02
- Observability: OBS-01 through OBS-03
- Quality: QUAL-01 through QUAL-06
- Build: BUILD-01, BUILD-02
- Documentation: DOC-01 through DOC-04

---

## Non-Functional Requirements

- **Latency:** HTTP handler must ACK within 3 seconds (CEPA heartbeat constraint)
- **Throughput:** Queue capacity 100,000 events default; handles VCAPS batches of thousands per PUT
- **Portability:** Single binary; no CGO dependencies; compiles for linux/amd64 and windows/amd64
- **Reliability:** TCP GELF reconnects automatically; failed backend does not block others (MultiWriter)
- **Observability:** Health endpoint and structured logs on every received batch

---

## Architecture Summary

```
CEPA HTTP PUT → pkg/server → pkg/parser → pkg/mapper → pkg/queue → pkg/evtx (writers)
```

- **server:** HTTP handler; ACKs immediately; enqueues events
- **parser:** CEE XML → []CEPAEvent
- **mapper:** CEPAEvent → WindowsEvent (CEPA type → Windows EventID + access mask)
- **queue:** Async worker pool; drops events on overflow with WARN log
- **evtx writers:** GELFWriter (UDP/TCP), Win32EventLogWriter, MultiWriter, BinaryEvtxWriter (stub v1)

For architectural decisions, see `docs/adr/`.

---

## v1.0 Scope

All requirements marked v1.0 in REQUIREMENTS.md. Delivered as a single Go binary.

---

## Success Metrics

- `go test ./...` passes with zero failures
- `make build` and `make build-windows` produce runnable binaries
- An operator can follow the README quickstart and see events in Graylog within 15 minutes
