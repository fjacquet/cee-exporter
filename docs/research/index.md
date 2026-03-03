# Research Archive

This directory contains all research conducted before and during the cee-exporter v2.0
milestone. Each document records the technology choices, architecture patterns, pitfalls,
and code examples verified for a specific feature area.

Research was conducted on **2026-03-03** with high confidence ratings derived from direct
codebase inspection and official library documentation.

---

## Project-level research

| Document | Description |
|----------|-------------|
| [Project Summary](project-summary.md) | Executive summary — stack, features, pitfalls, phase ordering rationale |
| [Technology Stack](technology-stack.md) | All libraries evaluated, selected, and rejected with rationale |
| [Architecture](architecture.md) | v2 component diagram, integration points, config additions |
| [Features](features.md) | Must-have vs. should-have vs. deferred feature analysis |
| [Pitfalls](pitfalls.md) | 27 concrete pitfalls across all feature areas with prevention strategies |

---

## Phase research

| Phase | Document | Key findings |
|-------|----------|--------------|
| 01 Quality | [phase-01-quality.md](phase-01-quality.md) | stdlib-only testing, `http.MaxBytesReader(nil)` bug fix, white-box GELF test pattern |
| 02 Build | [phase-02-build.md](phase-02-build.md) | CGO_ENABLED=0 cross-compilation, static linking, Makefile targets |
| 03 Documentation | [phase-03-documentation.md](phase-03-documentation.md) | mkdocs-material setup, ADR conventions, operator guide structure |
| 04 Observability & Linux Service | [phase-04-observability.md](phase-04-observability.md) | Prometheus `NewCounterFunc` wrapping atomics, separate port 9228, hardened systemd unit |
| 05 Windows Service | [phase-05-windows-service.md](phase-05-windows-service.md) | `kardianos/service` v1.2.4, `DelayedAutoStart`, `SetRecoveryActionsOnNonCrashFailures`, SCM 30-s timeout |
| 06 SIEM Writers | [phase-06-siem-writers.md](phase-06-siem-writers.md) | `crewjam/rfc5424` for RFC 5424, `go-lumber` SyncClient for Beats, TCP octet-counting framing, TLS dialer injection |
| 07 BinaryEvtxWriter | [phase-07-binary-evtx.md](phase-07-binary-evtx.md) | Pure-Go EVTX from scratch, static BinXML template, FILETIME encoding, dual-range CRC32, UTF-16LE strings |
| 08 TLS Automation | [phase-08-tls-automation.md](phase-08-tls-automation.md) | **CEPA HTTP-only constraint**, `autocert` ACME, TLS-ALPN-01 on port 443, self-signed stdlib generation, rate-limit pitfalls |

---

## Critical findings to be aware of

### CEPA protocol is HTTP-only

> The Dell PowerStore CEPA client sends events over **plain HTTP only**. Enabling TLS
> on port 12228 does NOT encrypt the PowerStore-to-exporter path. Always register
> `http://` (not `https://`) in PowerStore CEPA configuration.
>
> Source: [Phase 8 research](phase-08-tls-automation.md) — confidence MEDIUM (multiple
> third-party guides confirm; Dell official docs blocked by 403; no counter-evidence found).

### kardianos/service supersedes x/sys direct for Windows SCM

> Research 05 found three critical gaps with using `golang.org/x/sys/windows/svc`
> directly: missing `SetRecoveryActionsOnNonCrashFailures`, no `DelayedAutoStart`,
> and unhandled SCM edge-case errors. `kardianos/service` v1.2.4 handles all three.
>
> Decision recorded in [ADR-010](../adr/ADR-010-kardianos-service-windows-scm.md),
> superseding [ADR-007](../adr/ADR-007-windows-svc-x-sys.md).

### No pure-Go EVTX writer library exists

> All known Go EVTX projects (`0xrawsec/golang-evtx`, `Velocidex/evtx`) are parsers
> only. BinaryEvtxWriter must be implemented from scratch using stdlib.
> See [ADR-009](../adr/ADR-009-binary-evtx-scratch.md).
