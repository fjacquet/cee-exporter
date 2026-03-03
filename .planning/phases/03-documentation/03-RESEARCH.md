# Phase 3: Documentation - Research

**Researched:** 2026-03-03
**Domain:** Technical documentation for a Go daemon (README, ADR, PRD, CHANGELOG)
**Confidence:** HIGH

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| DOC-01 | README covers installation, prerequisites, quick-start (GELF to Graylog) | Quickstart pattern, Graylog GELF input steps, binary installation patterns documented below |
| DOC-02 | README documents all config.toml fields with examples | Full config struct extracted from main.go; every field, type, default, and example verified |
| DOC-03 | README covers TLS setup (self-signed cert generation example) | openssl one-liner verified with SAN support; Go built-in generate_cert.go also documented |
| DOC-04 | README covers CEPA registration (Dell PowerStore Event Publishing Pool configuration) | Step-by-step PowerStore UI navigation extracted from Dell documentation and PeerSoftware KB |
</phase_requirements>

---

## Summary

Phase 3 is a pure documentation phase. No new Go code is written. The deliverables are: (1) a README.md at project root that satisfies all four DOC requirements, (2) ADR files documenting the key architectural decisions already made, (3) a PRD describing what cee-exporter is and does, and (4) a CHANGELOG.md initializing the v1.0 release history.

The README is the primary artefact. Its four sections — quickstart, config reference, TLS setup, CEPA registration — map directly to DOC-01 through DOC-04. The additional artefacts (ADR, PRD, CHANGELOG) are standard open-source project hygiene that aid future contributors and operators in understanding the project's history and scope.

The technical content for all sections is already fully known: the `config.toml.example` file and `main.go` `Config` struct define every field; the CEPA protocol quirks are documented in `docs/PowerStore_CEPA_CEE vers EVTX.txt` and the project memory; and the standard openssl commands for self-signed certs with SAN are well-established.

**Primary recommendation:** Write README.md in a single plan, then write the supplementary artefacts (ADR, PRD, CHANGELOG) in a second plan. Treat the README as the canonical operator guide; keep ADR files concise (one page each per the Nygard format).

---

## Standard Stack

### Core

This is a documentation phase. There are no library dependencies to add. The tools used are:

| Tool | Purpose | Why Standard |
|------|---------|--------------|
| Markdown | Format for README, ADR, PRD, CHANGELOG | Universal, renders on GitHub, consumed by all editors |
| openssl (system CLI) | Generate self-signed TLS certificate | Universally available on Linux/macOS; Go `generate_cert.go` is the alternative |
| `go run crypto/tls/generate_cert.go` | Alternative TLS cert generator | Built into Go stdlib, no openssl dependency |

### Document Artefacts to Produce

| File | Location | Format |
|------|----------|--------|
| README.md | Project root | Markdown, operator-facing |
| CHANGELOG.md | Project root | Keep a Changelog 1.0.0 format |
| docs/adr/ADR-001-language-go.md | docs/adr/ | Nygard ADR format |
| docs/adr/ADR-002-gelf-primary-linux.md | docs/adr/ | Nygard ADR format |
| docs/adr/ADR-003-async-queue.md | docs/adr/ | Nygard ADR format |
| docs/adr/ADR-004-binary-evtx-deferred.md | docs/adr/ | Nygard ADR format |
| docs/PRD.md | docs/ | PRD format |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Single README.md | Separate docs/ site | README is sufficient for v1; a docs site adds tooling complexity with no benefit at this scale |
| openssl one-liner | `go run generate_cert.go` | openssl is more familiar to Linux ops; Go tool requires Go installed but avoids openssl dependency |
| Nygard ADR format | MADR (Markdown ADR) | Nygard is the original, simplest format; MADR adds more fields than needed here |

---

## Architecture Patterns

### Recommended Document Structure

```
/ (project root)
├── README.md                   # Primary operator guide (DOC-01..DOC-04)
├── CHANGELOG.md                # Version history (Keep a Changelog format)
├── config.toml.example         # Already exists — referenced by README
├── docs/
│   ├── PRD.md                  # Product Requirements Document
│   └── adr/
│       ├── ADR-001-language-go.md
│       ├── ADR-002-gelf-primary-linux.md
│       ├── ADR-003-async-queue.md
│       └── ADR-004-binary-evtx-deferred.md
└── .planning/                  # Already exists — internal planning only
```

### Pattern 1: README Structure for Operator Daemon

The canonical Go operator README structure for a daemon/service:

```markdown
# project-name

One-sentence description of what it does and who it's for.

## Overview
## Prerequisites
## Quick Start              ← DOC-01
## Configuration Reference  ← DOC-02
## TLS Setup               ← DOC-03
## Dell PowerStore CEPA Registration  ← DOC-04
## Troubleshooting
## Building from Source
## License
```

**What:** Sections ordered from "I just downloaded this" to "I need to configure advanced features."
**When to use:** Always for operator-facing daemons. Operators read top-to-bottom; put the quickstart before the deep config.

### Pattern 2: Nygard ADR Format

```markdown
# ADR-NNN: [Short noun phrase]

**Status:** accepted | proposed | deprecated | superseded by ADR-XXX

## Context

Forces at play. State facts neutrally. Include tensions.

## Decision

We will [active voice statement of the decision].

## Consequences

All consequences — positive, negative, and neutral. One to three paragraphs.
```

**What:** Lightweight, one-to-two page decision log stored in version control.
**When to use:** For every significant architectural decision that has trade-offs a future developer would need to understand.

### Pattern 3: Keep a Changelog Format

```markdown
# Changelog

All notable changes to this project will be documented in this file.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)
Versioning: [Semantic Versioning](https://semver.org/spec/v2.0.0.html)

## [Unreleased]

## [1.0.0] - 2026-03-03

### Added
- [feature description]

### Fixed
- [bug fix description]
```

**What:** Human-readable version history organized by release and change category.
**Categories:** Added, Changed, Deprecated, Removed, Fixed, Security.

### Pattern 4: PRD for Infrastructure Component

For a self-contained daemon rather than a user-facing product:

```markdown
# Product Requirements Document: cee-exporter

## Overview
## Problem Statement
## Goals and Non-Goals
## User Personas
## Functional Requirements (reference REQUIREMENTS.md)
## Non-Functional Requirements
## Architecture Summary
## v1.0 Scope
## Out of Scope
## Success Metrics
```

### Anti-Patterns to Avoid

- **Documenting internals in README:** README is for operators, not developers. Architecture details belong in ADRs or a separate ARCHITECTURE.md.
- **Configuration table without defaults:** Every config field must have type, default, and at least one example value — operators copy-paste from README.
- **Missing SAN in TLS cert example:** Modern Go TLS (≥ 1.15) rejects certificates without Subject Alternative Names. The openssl command MUST include `-addext "subjectAltName=..."`.
- **Vague Graylog steps:** "Add a GELF input" is insufficient. The README must name the exact menu path (`System > Inputs > Launch new input > GELF UDP`).
- **Omitting the RegisterRequest quirk in CEPA section:** The Dell CEPA handshake requires an HTTP 200 OK with strictly empty body. Any XML in the response causes a fatal parse error. This MUST be documented so operators understand cee-exporter's behavior.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| TLS certificate generation | Custom Go cert generator | openssl CLI or `go run generate_cert.go` | Both are battle-tested, widely documented |
| ADR tooling | Custom script | Markdown + Git (no tool needed at this scale) | adr-tools adds complexity; plain markdown files in docs/adr/ are sufficient |
| Changelog automation | Custom changelog generator | Manual for v1.0; git-cliff or conventional commits tooling can be added later | Automation adds CI complexity not justified at v1.0 with one release |

**Key insight:** This is a documentation phase. The only thing to "build" is clear prose. Resist the urge to add tooling for tooling's sake.

---

## Common Pitfalls

### Pitfall 1: TLS Certificate Missing SAN

**What goes wrong:** Operator generates certificate without Subject Alternative Name, connects a modern PowerStore client or browser, gets `x509: certificate relies on legacy Common Name field, use SANs instead` error.

**Why it happens:** Pre-2019 openssl workflows used `-subj '/CN=hostname'` without SAN extension. Go ≥ 1.15 enforces SAN requirement by default.

**How to avoid:** Use the `-addext "subjectAltName=DNS:hostname,IP:1.2.3.4"` flag (openssl ≥ 1.1.1) or the `-extfile` approach. Always include both DNS and IP entries for the server.

**Warning signs:** TLS handshake fails with `certificate has no suitable IP SANs` or `certificate relies on legacy CN`.

**Correct openssl command:**
```bash
openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout server.key -out server.crt \
  -subj "/CN=cee-exporter" \
  -addext "subjectAltName=IP:192.168.1.10,DNS:cee-exporter.corp.local"
```

### Pitfall 2: CEPA Port Blocked by Firewall

**What goes wrong:** Dell PowerStore cannot reach cee-exporter; SDNAS_CEPP_ALL_SERVERS_UNREACHABLE alerts fire.

**Why it happens:** Default port 12228 is non-standard and may not be open in corporate firewalls.

**How to avoid:** Explicitly document in the README that inbound TCP/UDP port 12228 must be open from the PowerStore NAS server IP to the cee-exporter host.

**Warning signs:** RegisterRequest never arrives; heartbeat timeout alerts on PowerStore.

### Pitfall 3: RegisterRequest Response Body Not Empty

**What goes wrong:** Dell CEPA publisher receives a non-empty HTTP 200 response to its RegisterRequest, fails to parse it as XML, and terminates the event publishing session.

**Why it happens:** Operators who customize cee-exporter may inadvertently add response bodies. This is a CEPA protocol requirement, not an HTTP convention.

**How to avoid:** Document this explicitly in the README's CEPA section: "cee-exporter responds to RegisterRequest with HTTP 200 OK and an empty body — this is required by the CEPA protocol."

### Pitfall 4: GELF UDP Packet Loss on High-Volume NAS

**What goes wrong:** At high I/O rates (VCAPS mode, thousands of events per PUT), UDP packets may be silently dropped at the OS or network level.

**Why it happens:** UDP has no delivery guarantee. VCAPS batches can be large.

**How to avoid:** Document in the README that `gelf_protocol = "tcp"` is recommended for production to avoid packet loss. UDP is fine for lab/test.

### Pitfall 5: Operator Uses Wrong NAS Server Scope for CEPA

**What goes wrong:** Operator creates Event Publishing Pool on the wrong NAS server, events from a different NAS server never arrive.

**Why it happens:** PowerStore has multiple NAS servers; CEPA is configured per-NAS-server.

**How to avoid:** Document that CEPA must be configured on the specific NAS server whose file-system events you want to capture.

---

## Code Examples

### Config Reference (extracted from main.go and config.toml.example)

Complete config.toml field reference — every field with type, default, and example:

```toml
# Top-level
# hostname (string, default: os.Hostname())
# The hostname embedded in every generated event.
# hostname = "powerstore-nas01"

[listen]
addr      = "0.0.0.0:12228"   # string, default: "0.0.0.0:12228"
tls       = false               # bool, default: false
# cert_file = "/etc/cee-exporter/tls/server.crt"   # string, default: ""
# key_file  = "/etc/cee-exporter/tls/server.key"   # string, default: ""

[output]
type = "gelf"                   # string: "gelf"|"evtx"|"multi", default: "gelf"
# targets = ["gelf", "evtx"]   # []string, only for type="multi"
gelf_host     = "localhost"     # string, default: "localhost"
gelf_port     = 12201           # int, default: 12201
gelf_protocol = "udp"           # string: "udp"|"tcp", default: "udp"
gelf_tls      = false           # bool, default: false (TCP only)
# evtx_path = "/var/log/cee-exporter"  # string, default: ""

[queue]
capacity = 100000               # int, default: 100000
workers  = 4                    # int, default: 4

[logging]
level  = "info"                 # string: "debug"|"info"|"warn"|"error", default: "info"
format = "json"                 # string: "json"|"text", default: "json"
```

**Environment variable overrides:**
- `CEE_LOG_LEVEL` — overrides `[logging] level`
- `CEE_LOG_FORMAT` — overrides `[logging] format`

### TLS Self-Signed Certificate Generation

```bash
# Generate a 4096-bit RSA key + self-signed certificate valid for 10 years
# Replace the IP/DNS values with your actual cee-exporter host address
openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout server.key \
  -out server.crt \
  -subj "/CN=cee-exporter" \
  -addext "subjectAltName=IP:192.168.1.10,DNS:cee-exporter.corp.local"
```

Requires openssl >= 1.1.1 (for `-addext`). For older openssl, use an extfile:

```bash
# Alternative using extfile (works with openssl < 1.1.1)
cat > san.cnf <<'EOF'
[req]
distinguished_name = req_dn
x509_extensions = v3_req
prompt = no
[req_dn]
CN = cee-exporter
[v3_req]
subjectAltName = IP:192.168.1.10,DNS:cee-exporter.corp.local
EOF

openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout server.key -out server.crt -config san.cnf
```

Configure cee-exporter:
```toml
[listen]
addr      = "0.0.0.0:12228"
tls       = true
cert_file = "/etc/cee-exporter/tls/server.crt"
key_file  = "/etc/cee-exporter/tls/server.key"
```

### Graylog GELF UDP Input Setup

```
1. Open Graylog web interface
2. Navigate to System > Inputs
3. Select "GELF UDP" from the "Select input" dropdown
4. Click "Launch new input"
5. Fill in:
   - Title: "PowerStore CEPA Events"
   - Bind address: 0.0.0.0
   - Port: 12201
6. Click "Launch"
```

Test that Graylog is receiving:
```bash
echo -n '{"version":"1.1","host":"test","short_message":"cee-exporter test","level":6}' \
  | nc -w0 -u graylog.corp.local 12201
```

### Dell PowerStore CEPA Event Publishing Pool

Steps (PowerStore Web UI):

```
1. Navigate to Storage > NAS Servers
2. Select the target NAS Server
3. Go to Security & Events tab > Events Publishing sub-tab
4. Click "Create new event publisher" (or Modify if one exists)
5. Assign a Publisher Name (e.g., "cee-exporter")
6. Under Publishing Pools, click Create Pool
7. Enter Pool Name (e.g., "default-pool")
8. Add CEPA Server:
   - Enter cee-exporter host IP or FQDN
   - Port: 12228 (default)
9. Under Event Configuration > Post-Events tab:
   - Click "Select all"
   - Uncheck: CloseDir, OpenDir, FileRead, OpenFileReadOffline, OpenFileWriteOffline
   - Leave Pre-Events and Post-Error-Events unchecked
10. Click Apply
```

**Critical:** Port 12228 must be reachable from the PowerStore NAS server's IP to the cee-exporter host. Verify with:
```bash
# From a host that can reach both:
nc -zv <cee-exporter-ip> 12228
```

### ADR Template (Nygard Format)

```markdown
# ADR-001: Use Go as implementation language

**Status:** accepted

## Context

The exporter must run as a single binary on both Linux and Windows without
requiring a runtime installation. It must handle concurrent HTTP requests
within the CEPA 3-second heartbeat timeout. The team has Go expertise.

## Decision

We will implement cee-exporter in Go.

## Consequences

Cross-platform compilation is trivial (GOOS/GOARCH env vars). The binary
has no external runtime dependencies. goroutines and channels map naturally
to the async queue pattern required by CEPA timing constraints. Win32 API
access is available via golang.org/x/sys/windows. The trade-off is that
pure-Go binary EVTX generation requires BinXML format implementation
(deferred to v2).
```

### CHANGELOG.md Initial Structure

```markdown
# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-03-03

### Added
- CEPA HTTP listener with RegisterRequest handshake and heartbeat support
- CEE XML parser for single-event and VCAPS bulk batch payloads
- Semantic mapping of 6 CEPA event types to Windows Event IDs (4660, 4663, 4670)
- GELF 1.1 writer over UDP and TCP with automatic TCP reconnection
- Win32 EventLog writer for Windows (ReportEvent API)
- MultiWriter fan-out to multiple backends simultaneously
- HTTPS/TLS listener with configurable x509 certificate
- TLS certificate expiry warning at startup (WARN when < 30 days remain)
- GET /health JSON endpoint (uptime, queue depth, event counters)
- Structured JSON logging via slog
- Async worker queue with configurable capacity and worker count
- TOML configuration file with environment variable overrides
- Makefile with build, build-windows, test, lint, clean targets
- Cross-compiled Windows binary (CGO_ENABLED=0, GOOS=windows GOARCH=amd64)

### Fixed
- http.MaxBytesReader nil ResponseWriter panic on payloads > 64 MiB
```

### PRD Section Structure for cee-exporter

```markdown
# Product Requirements Document: cee-exporter

**Version:** 1.0.0
**Date:** 2026-03-03

## Problem Statement

Dell PowerStore file-system audit events (CEPA/CEE protocol) are only natively
consumed by Windows CEE agents. Organizations using Linux-based SIEMs (Graylog,
Elasticsearch) or who cannot deploy Windows infrastructure need a bridge.

## Goals

- Any SIEM can ingest PowerStore file-system audit events without a Windows host
- Zero runtime dependencies beyond the Go binary
- CEPA protocol-compliant listener that handles heartbeat timing requirements

## Non-Goals (v1.0)

- Binary .evtx file generation on Linux (v2)
- Prometheus metrics endpoint (v2)
- Windows Service installer (v2)
- HA load-balancer configuration

## User Personas

- Linux sysadmin deploying Graylog who needs PowerStore NAS audit events
- Windows sysadmin using native Event Log infrastructure

## Architecture Summary

See PROJECT.md. Pipeline: CEPA HTTP PUT → server → parser → mapper → queue → writers
```

---

## ADRs to Write

Based on the key decisions documented in PROJECT.md and STATE.md:

| ADR # | Decision | Status |
|-------|----------|--------|
| ADR-001 | Use Go as implementation language | accepted |
| ADR-002 | GELF as primary Linux output (not binary .evtx) | accepted |
| ADR-003 | Async queue between HTTP handler and event writers | accepted |
| ADR-004 | Binary EVTX writer deferred to v2 | accepted |
| ADR-005 | CGO_ENABLED=0 for static linking on both targets | accepted |

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| openssl with CN only | openssl with `-addext subjectAltName` | Go 1.15 (2020) | Go rejects certs without SAN; must use modern form |
| gzip-compressed GELF UDP | Uncompressed GELF JSON | N/A | Graylog accepts both; uncompressed is simpler for debugging |
| Separate CHANGELOG per environment | Single CHANGELOG.md (Keep a Changelog 1.0.0) | ~2017 | Universal standard now |
| Long-form ADR with 10+ fields | Nygard 3-field ADR (Context, Decision, Consequences) | 2011 (Nygard), dominant ~2019 | Simpler and more widely adopted |

**Deprecated/outdated:**
- `openssl req -subj '/CN=hostname'` without SAN: rejected by Go TLS, modern browsers, and Java since ~2020
- GELF chunked UDP messages for large payloads: still supported by Graylog but TCP is cleaner

---

## Open Questions

1. **Should README document Windows deployment separately from Linux?**
   - What we know: The binary runs on both; the Win32 EventLog writer requires Windows; Graylog GELF path is cross-platform
   - What's unclear: Does the target operator audience include Windows admins?
   - Recommendation: Yes — include a short "Windows Deployment" subsection noting that the Win32 writer activates automatically and that `cee-exporter.exe` is produced by `make build-windows`

2. **Should TLS be documented as mandatory for production?**
   - What we know: Plaintext HTTP exposes audit event data on the network; CEPA traffic is sensitive
   - What's unclear: Whether the PowerStore CEPA publisher supports HTTPS to the CEE server
   - Recommendation: Based on docs review, PowerStore sends events over plain HTTP to the CEPA server by default. Note in README that TLS at the cee-exporter listener protects data in transit if CEPA is configured with an HTTPS endpoint. Mark as "recommended for production."

3. **CHANGELOG initial date: use implementation date or documentation date?**
   - What we know: Core pipeline was implemented pre-roadmap; tests/build added 2026-03-02
   - Recommendation: Use 2026-03-03 (today / documentation completion date) as v1.0.0 date — it's the first documented release. This is the conventional approach.

4. **Dell PowerStore CEPA: does it support HTTPS to the CEE server?**
   - What we know: The default is HTTP on port 12228; the PeerSoftware KB and Dell docs don't mention HTTPS for the PUT transport
   - What's unclear: Whether newer PowerStore firmware versions support TLS for event publishing
   - Recommendation: Document TLS as applying to the cee-exporter HTTP listener (useful if CEPA sends over HTTPS), but note it may not be used with default PowerStore CEPA configuration. Flag for operator to verify with Dell support.

---

## Sources

### Primary (HIGH confidence)

- `cmd/cee-exporter/main.go` — Full Config struct with all fields, types, defaults extracted directly
- `config.toml.example` — Canonical config file format and comments
- `docs/PowerStore_CEPA_CEE vers EVTX.txt` — Architecture analysis with protocol details
- `.planning/PROJECT.md` — Key decisions table used for ADR derivation
- `.planning/REQUIREMENTS.md` — DOC-01..DOC-04 requirement text
- https://keepachangelog.com/en/1.0.0/ — Keep a Changelog format specification (fetched, verified)
- https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions.html — Nygard ADR format (fetched, verified)

### Secondary (MEDIUM confidence)

- https://kb.peersoftware.com/kb/dell-powerstore-configuration-guide — PowerStore CEPA configuration steps (fetched, detailed and consistent with Dell docs)
- https://go2docs.graylog.org/current/getting_in_log_data/gelf.html — Graylog GELF input setup (verified via WebSearch)
- https://www.dell.com/support/manuals/en-us/powerstore-500t/pwrstr-cfg-nfs/events-publishing — Dell PowerStore event publishing (403 on direct fetch; steps confirmed via WebSearch and PeerSoftware KB)
- WebSearch results for openssl SAN certificate generation — multiple sources agree on `-addext` approach for openssl >= 1.1.1

### Tertiary (LOW confidence)

- WebSearch for Graylog GELF menu path (`System > Inputs`) — single source confirmation; recommend verifying against actual Graylog instance

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — no new libraries; pure documentation
- README content (config, TLS): HIGH — extracted directly from source code
- CEPA configuration steps: MEDIUM — confirmed via PeerSoftware KB and WebSearch; direct Dell docs returned 403
- ADR format: HIGH — fetched from Nygard's original blog post
- CHANGELOG format: HIGH — fetched from keepachangelog.com
- Graylog GELF menu path: MEDIUM — WebSearch confirms; not fetched from official docs

**Research date:** 2026-03-03
**Valid until:** 2026-04-03 (30 days — stable documentation domain; CEPA config steps may vary by PowerStore firmware version)
