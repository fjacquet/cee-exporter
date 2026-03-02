# cee-exporter

## What This Is

A cross-platform Go daemon that acts as a CEPA endpoint listener: it receives Dell PowerStore audit events (HTTP PUT / XML) from the Common Event Publishing Agent (CEPA) and converts them in real-time to Windows EVTX format. On Windows it writes via the native Win32 EventLog API; on Linux it generates binary `.evtx` files using a pure Go EVTX writer. The result is SIEM-ready audit telemetry without requiring a dedicated Windows CEPA server or a CEE installation on a production host.

## Core Value

Any Windows-compatible SIEM can ingest Dell PowerStore file-system audit events as native EVTX, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] Receive CEPA HTTP PUT requests and complete the RegisterRequest handshake correctly
- [ ] Respond to heartbeats within the 3-second timeout to prevent SDNAS_CEPP_ALL_SERVERS_UNREACHABLE alerts
- [ ] Parse CEE XML event payloads (single events and VCAPS bulk batches)
- [ ] Map CEPA event types to Windows Event IDs with correct semantic fidelity
- [ ] Write EVTX events via Win32 EventLog API on Windows
- [ ] Generate binary `.evtx` files via pure Go writer on Linux
- [ ] Support HTTPS/TLS listener (x509 certificate) for encrypted transport
- [ ] Async processing: ACK the HTTP request immediately, transform in background queue
- [ ] Handle VCAPS bulk mode (multiple events per HTTP payload)

### Out of Scope

- Prometheus /metrics endpoint — deferred to v2
- Windows Service installer (.msi, NSSM) — deferred to v2
- HA load-balancer setup (F5 VIP, dual-instance) — operational concern, not code
- PowerStore AppsON deployment guide — documentation only
- CAVA (antivirus scanning) events — not an audit use case
- CEE Linux flavour (RPC transport) — HTTP transport only

## Context

- **Source document**: `docs/PowerStore_ CEPA_CEE vers EVTX.txt` — comprehensive architecture analysis covering CEPA protocol, EVTX format constraints, and semantic mapping
- **CEE guide**: `docs/cee-9-x-windows-guide_en-us.pdf` — official Dell CEE 9.x configuration reference
- **CEPA handshake quirk**: listener must return HTTP 200 OK with empty body to `<RegisterRequest />`; any custom XML in the response causes a fatal parse error on the Dell side
- **EVTX binary format**: complex (BinXML encoding, chunk headers, checksums); no production-quality Go writing library exists — must implement or find a low-level approach
- **Semantic mapping confirmed**: CEPP_CREATE_FILE→4656/4663, CEPP_FILE_READ→4663, CEPP_FILE_WRITE→4663, CEPP_DELETE_FILE→4660/4659, CEPP_SETACL_FILE→4670, CEPP_CLOSE_MODIFIED→composite (no direct Windows equivalent)
- **VCAPS mode**: events arrive in batches; FeedInterval and MaxEventsPerFeed are configurable on the CEE side

## Constraints

- **Language**: Go — cross-platform binary, single deployment artifact
- **Runtime (Windows)**: Win32 `ReportEvent` API via `golang.org/x/sys/windows` — proper EVTX generation guaranteed by OS
- **Runtime (Linux)**: pure Go EVTX binary writer — no OS support; must implement BinXML serialization correctly
- **Protocol**: HTTP (plain) for dev/lab; HTTPS/TLS mandatory for production
- **Timing**: CEPA heartbeat timeout is ~3 seconds; HTTP handler must ACK before queuing work
- **No external services**: self-contained binary, no database, no message broker

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go language | Cross-platform binary, strong concurrency primitives, excellent net/http | — Pending |
| Pure Go EVTX writer for Linux | Avoid requiring Windows runtime; keeps deployment simple | — Pending |
| Win32 API path on Windows | Only supported/correct way to generate valid EVTX on Windows | — Pending |
| Async queue (receive→transform) | CEPA 3s timeout makes synchronous processing too risky at high I/O | — Pending |
| HTTPS support in v1 | Document flags plaintext HTTP as a security risk for audit data | — Pending |

---
*Last updated: 2026-03-02 after initialization*
