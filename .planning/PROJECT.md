# cee-exporter

## What This Is

A cross-platform Go daemon that receives Dell PowerStore CEPA audit events (HTTP PUT / XML) and converts them to SIEM-ready telemetry. Supports six output backends: GELF (Graylog), native Win32 EventLog, pure-Go BinaryEvtxWriter (.evtx on Linux), SyslogWriter (RFC 5424), BeatsWriter (Lumberjack v2), and MultiWriter fan-out. Includes automatic TLS certificate management via Let's Encrypt ACME, self-signed certs, or manual cert files. The EVTX writer is backed by the standalone OSS library `github.com/fjacquet/go-evtx` which provides periodic fsync (≤15s), file rotation (size/count/time/SIGHUP), and a Prometheus health gauge.

## Core Value

Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## Requirements

### Validated

- ✓ Receive CEPA HTTP PUT requests and complete the RegisterRequest handshake correctly — v1.0
- ✓ Respond to heartbeats within the 3-second timeout — v1.0
- ✓ Parse CEE XML event payloads (single events and VCAPS bulk batches) — v1.0
- ✓ Map CEPA event types to Windows Event IDs with correct semantic fidelity — v1.0
- ✓ Write EVTX events via Win32 EventLog API on Windows — v1.0
- ✓ Emit GELF JSON to Graylog GELF Input (cross-platform) — v1.0
- ✓ Support multi-target fan-out (write to multiple backends simultaneously) — v1.0
- ✓ Support HTTPS/TLS listener (x509 certificate) for encrypted transport — v1.0
- ✓ Async processing: ACK immediately, transform in background queue — v1.0
- ✓ Handle VCAPS bulk mode (multiple events per HTTP payload) — v1.0
- ✓ Unit-tested core packages (parser, mapper, queue, writers) — v1.0
- ✓ Makefile with build, test, lint, cross-compile targets — v1.0
- ✓ README with installation, configuration, and TLS setup instructions — v1.0
- ✓ Prometheus /metrics endpoint on dedicated port 9228 — v2.0
- ✓ Hardened systemd unit file for Linux service deployment — v2.0
- ✓ Windows Service registration via install/uninstall subcommands — v2.0
- ✓ SyslogWriter (RFC 5424 over UDP/TCP) — v2.0
- ✓ BeatsWriter (Lumberjack v2 with optional TLS) — v2.0
- ✓ Pure-Go BinaryEvtxWriter generating valid .evtx files on Linux — v2.0
- ✓ .evtx files parseable by forensics tools (python-evtx confirmed) — v2.0
- ✓ tls_mode="acme" for automatic Let's Encrypt certificate provisioning — v3.0
- ✓ tls_mode="self-signed" for runtime ECDSA cert generation — v3.0
- ✓ tls_mode="manual" with backward-compatible config migration — v3.0
- ✓ config.toml.example documents all four TLS modes — v3.0
- ✓ github.com/fjacquet/go-evtx OSS module extracted with layered WriteRaw/WriteRecord API — v4.0
- ✓ BinaryEvtxWriter writes all events to disk regardless of session length (EVTX-01 silent drop fix) — v4.0
- ✓ Periodic fsync on BinaryEvtxWriter (flush_interval_s, default 15, ≤15s data-loss guarantee) — v4.0
- ✓ Graceful shutdown drains all buffered events to disk before exit — v4.0
- ✓ File rotation by size (max_file_size_mb), count (max_file_count), and time (rotation_interval_h) — v4.0
- ✓ SIGHUP triggers immediate .evtx file rotation without daemon restart — v4.0
- ✓ Prometheus cee_last_fsync_unix_seconds gauge for SRE fsync health alerting — v4.0
- ✓ validateOutputConfig() rejects invalid flush/rotation values at startup — v4.0
- ✓ config.toml.example documents all four new [output] fields with zero-value semantics — v4.0
- ✓ ADR-012 (flush ticker ownership) and ADR-013 (write-on-close model) in docs/adr/ — v4.0

### Active

*(planning next milestone)*

### Out of Scope

- HA load-balancer setup (F5 VIP, dual-instance) — operational concern, not code
- PowerStore AppsON deployment guide — documentation only
- CAVA (antivirus scanning) events — not an audit use case
- CEE Linux flavour (RPC transport) — HTTP transport only
- MSI installer for Windows Service — `install`/`uninstall` subcommands suffice
- DNS-01 ACME challenge via go-acme/lego — deferred (TLS-F01), adds 160+ deps
- Let's Encrypt staging URL — deferred (TLS-F02), operator concern
- sd_notify READY=1 for systemd Type=notify — deferred (OBS-F02)
- Cross-event EVTX template sharing — deferred (OUT-F01), performance optimization
- Prometheus counter cee_rotation_total — deferred to v5 (OBS-01)
- Compression of rotated .evtx files — blocked on forensics tool support (ROT-F01)
- Multi-chunk EVTX beyond single-chunk-per-flush — deferred to v5 (EVTX-02)
- Startup repair pass for partial-chunk crash files — deferred to v5 (EVTX-03)

## Context

**Shipped v4.0 on 2026-03-05.** ~7,238 Go LOC across cee-exporter (9 packages) + go-evtx (standalone module). 10 commits, 66 files changed in v4.0 (10,596 insertions, 3,477 deletions).

Tech stack: Go 1.24+, `net/http`, `encoding/xml`, `log/slog`, `golang.org/x/sys/windows`, `golang.org/x/crypto/acme/autocert`, `github.com/BurntSushi/toml`, `github.com/prometheus/client_golang`, `github.com/kardianos/service`, `github.com/crewjam/rfc5424`, `github.com/elastic/go-lumber`, `github.com/fjacquet/go-evtx`.

- **Source document**: `docs/PowerStore_ CEPA_CEE vers EVTX.txt`
- **CEE guide**: `docs/cee-9-x-windows-guide_en-us.pdf`
- **CEPA handshake**: HTTP 200 OK with empty body to `<RegisterRequest />`
- **GELF path**: GELF 1.1 JSON over UDP/TCP directly to Graylog — no Winlogbeat
- **BinXML**: Template-based with TemplateInstanceNode (0x0C), inline NameNodes, NormalSubstitution (0x0D)
- **TLS**: Four modes (off/manual/acme/self-signed) via tls_mode config field
- **EVTX durability**: go-evtx v0.5.0 with open-handle model, periodic fsync, file rotation

**Known tech debt:**
- `make test` omits `-race` (incompatible with CGO_ENABLED=0)
- Win32 EventID registration may need message DLL for Event Viewer display
- Phase 7 SC-2 (Windows Event Viewer round-trip) still needs human verification
- go-evtx `go.mod` require shows v0.4.0; replace directive activates v0.5.0 locally — push v0.5.0 tag before dropping replace directive
- ADR-013 status header not updated to "Superseded" after Phase 10 completion

## Constraints

- **Language**: Go — cross-platform binary, single deployment artifact
- **Runtime (Windows)**: Win32 `ReportEvent` API via `golang.org/x/sys/windows`
- **Runtime (Linux)**: GELF, Syslog, Beats, BinaryEvtxWriter (template-based BinXML)
- **Protocol**: HTTP (plain) for dev/lab; HTTPS/TLS (four modes) for production
- **Timing**: CEPA heartbeat timeout ~3 seconds; ACK before queuing
- **No external services**: self-contained binary, no database, no message broker
- **CGO_ENABLED=0**: Static linking on all platforms

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go language | Cross-platform binary, strong concurrency, excellent net/http | ✓ Good |
| GELF as primary Linux output | Direct Graylog integration without agent | ✓ Good |
| BinaryEvtxWriter (template-based BinXML) | Pure-Go .evtx on Linux; python-evtx validates | ✓ Good |
| Win32 API path on Windows | Only correct way to generate valid EVTX on Windows | ✓ Good |
| Async queue (receive→transform) | CEPA 3s timeout makes sync processing risky | ✓ Good |
| CGO_ENABLED=0 | Static linking; Linux→Windows cross-compile; no host toolchain | ✓ Good |
| White-box tests, stdlib only | No testify dependency; access to unexported symbols | ✓ Good |
| kardianos/service for Windows SCM | Clean install/uninstall/stop lifecycle with recovery actions | ✓ Good |
| crewjam/rfc5424 for syslog | Avoids stdlib log/syslog Windows build exclusion | ✓ Good |
| autocert for ACME | Pure-Go, CGO_ENABLED=0 compatible, stdlib crypto only for self-signed | ✓ Good |
| migrateListenConfig for TLS backward compat | Zero-effort upgrade from tls=true to tls_mode="manual" | ✓ Good |
| Prometheus private registry | cee_* metrics only, no default Go runtime clutter | ✓ Good |
| Multi-stage Docker (scratch final) | Minimal attack surface; <10 MB image | ✓ Good |
| Extract go-evtx as OSS module (v4.0) | Enables external forensics tools to write .evtx; separates concerns cleanly | ✓ Good |
| Flush ticker owned by go-evtx Writer, not queue layer (v4.0) | Keeps fsync guarantee at the writer level; queue layer stays writer-agnostic (ADR-012) | ✓ Good |
| Open-handle model replaces write-on-close (v4.0) | Enables true incremental flush via f.WriteAt; fixes silent drop bug for large sessions (ADR-013) | ✓ Good |
| OnFsync callback in RotationConfig (v4.0) | Decouples go-evtx from Prometheus; enables any caller to hook into fsync events | ✓ Good |
| SIGHUP via type assertion (v4.0) | Avoids polluting the Writer interface; SIGHUP only meaningful for file-based writers | ✓ Good |
| replace directive for go-evtx in dev (v4.0) | Allows fast iteration without GOPROXY indexing delay; must be removed before v5 release | ⚠️ Revisit |

---
*Last updated: 2026-03-05 after v4.0 Industrialisation milestone*
