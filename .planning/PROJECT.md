# cee-exporter

## What This Is

A cross-platform Go daemon that listens for Dell PowerStore CEPA audit events (HTTP PUT / XML) and converts them in real-time to Windows-compatible audit telemetry. On Windows it writes via the native Win32 EventLog API; on all platforms (including Linux) it can emit GELF JSON directly to a Graylog GELF Input. The result is SIEM-ready audit telemetry without requiring a dedicated Windows CEPA server or a CEE installation on a production host.

## Core Value

Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] Receive CEPA HTTP PUT requests and complete the RegisterRequest handshake correctly
- [ ] Respond to heartbeats within the 3-second timeout to prevent SDNAS_CEPP_ALL_SERVERS_UNREACHABLE alerts
- [ ] Parse CEE XML event payloads (single events and VCAPS bulk batches)
- [ ] Map CEPA event types to Windows Event IDs with correct semantic fidelity
- [ ] Write EVTX events via Win32 EventLog API on Windows
- [ ] Emit GELF JSON to Graylog GELF Input (cross-platform, v1 primary Linux path)
- [ ] Support multi-target fan-out (write to multiple backends simultaneously)
- [ ] Support HTTPS/TLS listener (x509 certificate) for encrypted transport
- [ ] Async processing: ACK the HTTP request immediately, transform in background queue
- [ ] Handle VCAPS bulk mode (multiple events per HTTP payload)
- [ ] Unit-tested core packages (parser, mapper, queue, writers)
- [ ] Makefile with build, test, lint, cross-compile targets
- [ ] README with installation, configuration, and TLS setup instructions

### Out of Scope

- Binary .evtx writer for Linux (pure Go BinXML) — deferred to v1.x; GELF covers Graylog use case without it
- Prometheus /metrics endpoint — deferred to v2
- Windows Service installer (.msi, NSSM) — deferred to v2
- HA load-balancer setup (F5 VIP, dual-instance) — operational concern, not code
- PowerStore AppsON deployment guide — documentation only
- CAVA (antivirus scanning) events — not an audit use case
- CEE Linux flavour (RPC transport) — HTTP transport only
- Beats/Lumberjack protocol writer — deferred to v2

## Context

- **Source document**: `docs/PowerStore_ CEPA_CEE vers EVTX.txt` — comprehensive architecture analysis covering CEPA protocol, EVTX format constraints, and semantic mapping
- **CEE guide**: `docs/cee-9-x-windows-guide_en-us.pdf` — official Dell CEE 9.x configuration reference
- **CEPA handshake quirk**: listener must return HTTP 200 OK with empty body to `<RegisterRequest />`; any custom XML in the response causes a fatal parse error on the Dell side
- **GELF path**: cee-exporter emits GELF 1.1 JSON over UDP/TCP directly to Graylog GELF Input — no Winlogbeat agent required, cross-platform
- **Semantic mapping confirmed**: CEPP_CREATE_FILE→4663, CEPP_FILE_READ→4663, CEPP_FILE_WRITE→4663, CEPP_DELETE_FILE→4660, CEPP_SETACL_FILE→4670, CEPP_CLOSE_MODIFIED→4663 (composite)
- **VCAPS mode**: events arrive in batches; FeedInterval and MaxEventsPerFeed are configurable on the CEE side
- **Known bug**: `readBody` in `pkg/server/server.go` uses `http.MaxBytesReader(nil, ...)` — nil ResponseWriter will panic on oversized payloads

## Constraints

- **Language**: Go — cross-platform binary, single deployment artifact
- **Runtime (Windows)**: Win32 `ReportEvent` API via `golang.org/x/sys/windows` — proper EVTX generation guaranteed by OS
- **Runtime (Linux/macOS)**: GELF output (primary); BinaryEvtxWriter is a stub
- **Protocol**: HTTP (plain) for dev/lab; HTTPS/TLS mandatory for production
- **Timing**: CEPA heartbeat timeout is ~3 seconds; HTTP handler must ACK before queuing work
- **No external services**: self-contained binary, no database, no message broker

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go language | Cross-platform binary, strong concurrency primitives, excellent net/http | ✓ Good |
| GELF as primary Linux output | Avoids complex BinXML implementation; direct Graylog integration without agent | ✓ Good |
| BinaryEvtxWriter deferred to v1.x | GELF covers Graylog use case; BinXML is 500-1500 LOC with format complexity | — Pending |
| Win32 API path on Windows | Only supported/correct way to generate valid EVTX on Windows | — Pending |
| Async queue (receive→transform) | CEPA 3s timeout makes synchronous processing too risky at high I/O | ✓ Good |
| HTTPS support in v1 | Plaintext HTTP is a security risk for audit data | — Pending |
| MultiWriter fan-out interface | Enables simultaneous output to multiple backends without code changes | ✓ Good |

---
*Last updated: 2026-03-02 after v1.0 milestone initialization — GELF added, BinXML deferred*
