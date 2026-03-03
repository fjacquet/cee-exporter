# cee-exporter

## What This Is

A cross-platform Go daemon that listens for Dell PowerStore CEPA audit events (HTTP PUT / XML) and converts them in real-time to Windows-compatible audit telemetry. On Windows it writes via the native Win32 EventLog API; on all platforms (including Linux) it can emit GELF JSON directly to a Graylog GELF Input. The result is SIEM-ready audit telemetry without requiring a dedicated Windows CEPA server or a CEE installation on a production host.

## Core Value

Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## Requirements

### Validated

- ✓ Receive CEPA HTTP PUT requests and complete the RegisterRequest handshake correctly — v1.0
- ✓ Respond to heartbeats within the 3-second timeout to prevent SDNAS_CEPP_ALL_SERVERS_UNREACHABLE alerts — v1.0
- ✓ Parse CEE XML event payloads (single events and VCAPS bulk batches) — v1.0
- ✓ Map CEPA event types to Windows Event IDs with correct semantic fidelity — v1.0
- ✓ Write EVTX events via Win32 EventLog API on Windows — v1.0
- ✓ Emit GELF JSON to Graylog GELF Input (cross-platform, v1 primary Linux path) — v1.0
- ✓ Support multi-target fan-out (write to multiple backends simultaneously) — v1.0
- ✓ Support HTTPS/TLS listener (x509 certificate) for encrypted transport — v1.0
- ✓ Async processing: ACK the HTTP request immediately, transform in background queue — v1.0
- ✓ Handle VCAPS bulk mode (multiple events per HTTP payload) — v1.0
- ✓ Unit-tested core packages (parser, mapper, queue, writers) — v1.0
- ✓ Makefile with build, test, lint, cross-compile targets — v1.0
- ✓ README with installation, configuration, and TLS setup instructions — v1.0

### Active

- [ ] Prometheus /metrics endpoint (`cee_events_received_total`, `cee_events_dropped_total`, `cee_queue_depth`)
- [ ] Windows Service installer (NSSM or native service registration)
- [ ] Systemd unit file for Linux deployment
- [ ] Pure-Go BinaryEvtxWriter generating valid .evtx files on Linux (BinXML format)
- [ ] BeatsWriter (Lumberjack v2 protocol) for Logstash/Graylog Beats Input

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

**Shipped v1.0 on 2026-03-03.** ~2,138 Go LOC. Tech stack: Go 1.24, `net/http`, `encoding/xml`, `log/slog`, `golang.org/x/sys/windows`, `github.com/BurntSushi/toml`.

- **Source document**: `docs/PowerStore_ CEPA_CEE vers EVTX.txt` — comprehensive architecture analysis covering CEPA protocol, EVTX format constraints, and semantic mapping
- **CEE guide**: `docs/cee-9-x-windows-guide_en-us.pdf` — official Dell CEE 9.x configuration reference
- **CEPA handshake quirk**: listener must return HTTP 200 OK with empty body to `<RegisterRequest />`; any custom XML causes fatal parse error on Dell side
- **GELF path**: cee-exporter emits GELF 1.1 JSON over UDP/TCP directly to Graylog GELF Input — no Winlogbeat agent, cross-platform
- **Semantic mapping confirmed**: CEPP_CREATE_FILE→4663, CEPP_FILE_READ→4663, CEPP_FILE_WRITE→4663, CEPP_DELETE_FILE→4660, CEPP_SETACL_FILE→4670, CEPP_CLOSE_MODIFIED→4663
- **Tech debt**: `make test` omits `-race` (incompatible with CGO_ENABLED=0); Win32 EventID registration may need message DLL for full Event Viewer display

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
| BinaryEvtxWriter deferred to v2 | GELF covers Graylog use case; BinXML is 500-1500 LOC with format complexity | ✓ Good |
| Win32 API path on Windows | Only supported/correct way to generate valid EVTX on Windows | ✓ Good |
| Async queue (receive→transform) | CEPA 3s timeout makes synchronous processing too risky at high I/O | ✓ Good |
| HTTPS/TLS listener in v1 | Plaintext HTTP is a security risk for audit data in transit | ✓ Good |
| MultiWriter fan-out interface | Enables simultaneous output to multiple backends without code changes | ✓ Good |
| CGO_ENABLED=0 | Static linking; enables Linux→Windows cross-compile with no host toolchain | ✓ Good |
| White-box tests, stdlib only | Avoids testify dependency; same-package tests access unexported symbols | ✓ Good |
| `defer func() { _ = r.Body.Close() }()` | errcheck requires explicit error discard for deferred Close calls | ✓ Good |
| Multi-stage Docker (scratch final) | Minimal attack surface; <10 MB image; no shell in prod container | ✓ Good |
| mkdocs-material for docs site | GitHub Pages deployment from `docs/` with MkDocs Material theme | ✓ Good |

---
*Last updated: 2026-03-03 after v1.0 milestone shipped*
