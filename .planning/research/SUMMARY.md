# Project Research Summary

**Project:** cee-exporter v2.0 — Operations & Output Expansion
**Domain:** Go audit-event bridge daemon — ops instrumentation + SIEM output writers
**Researched:** 2026-03-03
**Confidence:** HIGH (architecture derived from direct codebase inspection; library choices verified via pkg.go.dev official docs)

## Executive Summary

cee-exporter v2.0 extends a mature, well-structured Go daemon by adding 6 new capabilities across two categories: operational readiness (Prometheus metrics, systemd unit, Windows Service) and SIEM output expansion (SyslogWriter, BeatsWriter, BinaryEvtxWriter). The existing v1 architecture — with its clean `Writer` interface, `pkg/metrics` atomic counters, and `pkg/queue` worker pool — is well-suited for all 6 features. None require architectural rewrites; each slots into established extension points. The 4-phase build order recommended by research (Prometheus+Systemd, Windows Service, Syslog+Beats, BinaryEvtxWriter) reflects genuine dependency ordering and risk management, not arbitrary sequencing.

The library selection is minimal and defensible. Only 3 new external dependencies are introduced: `prometheus/client_golang@v1.23.2` (industry standard, pure Go), `elastic/go-lumber@v0.1.1` (official Elastic Lumberjack v2 client), and `crewjam/rfc5424@v0.1.0` (minimal cross-platform RFC 5424 formatter). The Windows Service wrapping reuses `golang.org/x/sys/windows/svc` already present in `go.mod` via the `kardianos/service` abstraction. The systemd unit file is a pure text artifact requiring zero code changes. All features maintain the `CGO_ENABLED=0` constraint for fully static cross-compiled binaries.

BinaryEvtxWriter is the single highest-risk item in v2.0. No pure-Go EVTX writer library exists; the implementation must be built from scratch against the MS-EVEN6 binary specification, with an estimated scope of 600–1200 LOC spread across a dedicated `pkg/evtx/binxml/` subpackage. This feature must be isolated in its own phase, developed against a round-trip test fixture, and must not block delivery of the other 5 features. The remaining 27 documented pitfalls are concrete and preventable — most reduce to applying patterns already established in `GELFWriter` (mutex-protected reconnect, single source of truth for counters) to the new writers.

---

## Key Findings

### Recommended Stack

The v1 stack requires only 3 net-new external dependencies. All are pure Go, all build with `CGO_ENABLED=0`, all cross-compile from Linux to Windows.

See full analysis: `.planning/research/STACK.md`

**Core technologies:**

- `github.com/prometheus/client_golang` v1.23.2 — Prometheus counter/gauge registration and HTTP handler; only officially backed Go Prometheus library; separate `9xxx` port required
- `github.com/elastic/go-lumber` v0.1.1 — Lumberjack v2 wire protocol client for Logstash/Graylog Beats Input; official Elastic library; protocol is frozen (stable since Beats 6.x)
- `github.com/crewjam/rfc5424` v0.1.0 — RFC 5424 message construction writing to `io.Writer`; minimal, cross-platform; avoids stdlib `log/syslog` which fails to build on Windows
- `golang.org/x/sys/windows/svc` (already in go.mod) — Windows SCM service registration, start/stop lifecycle; no new module required
- `github.com/kardianos/service` v1.x — cross-platform service wrapper that delegates to `x/sys/windows/svc` on Windows and is a no-op shim on Linux; July 2025 release
- stdlib (`encoding/binary`, `hash/crc32`, `unicode/utf16`) — BinaryEvtxWriter implementation; zero new external dependencies for the highest-complexity feature

**Critical version constraint:** `prometheus/client_golang` v1.23.2+ required to avoid the `go.uber.org/atomic`/`grafana/regexp` transitive dependency that broke `CGO_ENABLED=0` in earlier versions.

---

### Expected Features

See full analysis: `.planning/research/FEATURES.md`

**Must have (table stakes) — operators cannot deploy without these:**

- **Prometheus `/metrics` endpoint** — standard ops contract for any Go daemon in 2026; atomic counters already exist in `pkg/metrics`, exposure is 60–80 LOC; use `cee_events_received_total`, `cee_events_dropped_total`, `cee_queue_depth`, `cee_writer_errors_total`
- **Systemd unit file** — Linux production deployment without a unit file is not repeatable; pure text artifact; use `Type=simple` (not `Type=notify` unless `sd_notify` is wired in); zero code changes
- **Windows Service registration** — SCM-managed service is the Windows production contract; NSSM is unacceptable in production; `svc/mgr` already imported; expose `install`/`uninstall`/`start`/`stop` subcommands
- **SyslogWriter (RFC 5424)** — every SIEM speaks syslog; structured data (SD-ELEMENT) carries all audit fields; broadest compatibility target matrix

**Should have (differentiators) — justify the v2.0 release:**

- **BeatsWriter (Lumberjack v2)** — direct ingestion by Logstash and Graylog Beats Input without a sidecar agent; ECS field alignment for Kibana/Elastic compatibility
- **BinaryEvtxWriter (pure-Go BinXML)** — native `.evtx` files readable by Event Viewer and forensics tools on Linux; closes the only remaining cross-platform EVTX gap from v1

**Defer (anti-features — do not build in v2.0):**

- MSI installer for Windows Service — ship `cee-exporter.exe install` CLI subcommand instead
- Prometheus push gateway — pull model is correct for a long-running daemon
- RFC 3164 legacy BSD syslog — unstructured; cannot carry audit fields reliably
- Grafana dashboards / alerting rules — operational concern, document recommended PromQL queries instead
- NSSM wrapper — external binary, breaks single-artifact deploy contract

---

### Architecture Approach

The v2 integration adds components at four well-defined extension points without touching the core event pipeline (server → queue → parser → mapper → writer). The CEPA handler, queue, parser, mapper, and `pkg/metrics` are all unchanged. See full diagram: `.planning/research/ARCHITECTURE.md`

**Major components added in v2:**

1. `pkg/prometheus/handler.go` — standalone HTTP server on port 9228; reads `pkg/metrics.M` atomics via `Snapshot()` and exposes them as Prometheus metrics using a private `prometheus.NewRegistry()` (not `DefaultRegisterer`); runs in its own goroutine independent of the CEPA mux
2. `pkg/evtx/writer_beats.go` — implements `Writer` interface; wraps `go-lumber` `SyncClient` with a `sync.Mutex`; mirrors `GELFWriter` reconnect pattern; maps `WindowsEvent` fields to ECS-aligned `map[string]interface{}`
3. `pkg/evtx/writer_syslog.go` — implements `Writer` interface; uses `crewjam/rfc5424` for message construction; sends via raw `net.Conn` (UDP or TCP); no build tags — all platforms
4. `pkg/evtx/writer_binary_evtx.go` + `pkg/evtx/binxml/` subpackage — replaces stub; writes EVTX file header + chunks + BinXML token streams; build tag `!windows`; stdlib only
5. `cmd/cee-exporter/service_windows.go` + `service_notwindows.go` — `kardianos/service` wrapper; `main()` split into `main()` + `run()`; Windows-only service registration exposed via `-service install|uninstall|start|stop` flags
6. `deploy/systemd/cee-exporter.service` — static file, no Go code

**Key architectural decision — separate metrics port:** The Prometheus `/metrics` endpoint must NOT share the CEPA mux on port 12228. The CEPA mux may be TLS-only; Prometheus scrapes would need TLS client configuration. A separate port (9228 default) with its own `http.ServeMux` avoids scrape log pollution in CEPA logs and mux method conflicts. This is the industry standard for Go exporters.

**Config additions (go.mod + config.toml schema):**

```
[metrics]    enabled, addr (0.0.0.0:9228)
[output]     beats_host, beats_port, beats_tls
             syslog_host, syslog_port, syslog_protocol, syslog_tls
```

New `type` values: `"beats"`, `"syslog"` (adds to existing `"gelf"`, `"evtx"`, `"multi"`).

---

### Critical Pitfalls

27 pitfalls documented across 6 feature areas. See full analysis: `.planning/research/PITFALLS.md`

Top 5 by consequence severity:

1. **Prometheus mux collision (CRITICAL)** — Registering `/metrics` on `DefaultServeMux` or on the CEPA mux causes 404/405 scrape failures or a port-binding race at startup. Prevention: dedicated port 9228, private `prometheus.NewRegistry()`, separate `http.ServeMux`.

2. **BinaryEvtxWriter dual chunk checksum (CRITICAL)** — EVTX chunks have two independent CRC32 fields (event records checksum at offset 52, header checksum at offset 124) computed over different byte ranges in a specific order. If either is wrong or confused, Event Viewer rejects the file as corrupt with no useful error message. Prevention: compute event records CRC first, write it at offset 52, then compute header CRC covering the already-updated chunk header.

3. **Windows Service SCM 30-second startup timeout (CRITICAL)** — The SCM clock starts at `CreateProcess`; Go runtime init + config loading + TLS setup before `svc.Run()` can breach 30 seconds on boot-loaded VMs. Prevention: send `StartPending` as the very first action in `Execute()`; use `StartAutoDelayed` service type (120-second window) instead of `StartAutomatic`.

4. **SyslogWriter log/syslog Windows build failure (CRITICAL)** — stdlib `log/syslog` has `//go:build !windows` embedded; importing it breaks the Windows cross-compile. Prevention: use `crewjam/rfc5424` + `net.Conn` — no platform build constraints anywhere in the dependency.

5. **BeatsWriter SyncClient not thread-safe (HIGH)** — `go-lumber` `SyncClient` has no internal locking; the existing queue runs N concurrent workers. Concurrent `Send()` calls corrupt the Lumberjack framing state. Prevention: wrap `SyncClient.Send()` in a `sync.Mutex` inside `BeatsWriter.WriteEvent()` — the same pattern `GELFWriter` already uses.

Additional high-impact pitfalls to address per phase:
- Prometheus: cardinality explosion if `ObjectName`/`SubjectUsername` used as metric labels (use only `event_id`, `writer`, `status`)
- Prometheus: parallel counter drift if `pkg/metrics.Store` atomics and `prometheus.Counter` are incremented independently (wrap, don't duplicate)
- BinaryEvtxWriter: record trailing size copy missing causes entire chunk to be unreadable
- BinaryEvtxWriter: string table offsets are chunk-relative, not file-relative
- BinaryEvtxWriter: stub/real type name conflict — remove `writer_evtx_stub.go` before adding real implementation
- BeatsWriter: TLS silently dropped on reconnect if `SyncDial` used instead of custom `dialFunc`
- Windows Service: `os/signal.Notify(SIGTERM)` conflicts with SCM control handler; gate behind `!svc.IsWindowsService()`

---

## Implications for Roadmap

Based on research, the 4-phase structure below reflects actual dependency ordering and risk progression. All phases after Phase 1 are independent of each other except where noted.

### Phase 1: Observability Foundation

**Rationale:** Prometheus metrics and the systemd unit file are the two lowest-risk, highest-ops-value items. Prometheus can be built and tested entirely on Linux without a Windows environment. The systemd unit file is a zero-code text artifact. Together they constitute the operational readiness baseline that makes the daemon deployable in managed Linux environments. This phase also includes the `run()` extraction refactor in `main.go` (splitting `main()` into `main()` + `run()`), which is a prerequisite for Phase 2 Windows Service integration.

**Delivers:**
- `/metrics` endpoint on port 9228 with 4 Prometheus metrics (`cee_events_received_total`, `cee_events_dropped_total`, `cee_queue_depth`, `cee_writer_errors_total`)
- `deploy/systemd/cee-exporter.service` — production-ready hardened unit file
- `main()` → `run()` refactor (prerequisite for Phase 2)
- `go.mod` addition: `prometheus/client_golang@v1.23.2`

**Uses:** `github.com/prometheus/client_golang` v1.23.2 (new), `golang.org/x/sys` (existing)

**Implements:** `pkg/prometheus/handler.go` (new), `deploy/systemd/cee-exporter.service` (new), `cmd/cee-exporter/main.go` (modified)

**Avoids:** Pitfall 1 (mux collision), Pitfall 2 (blocking Collect), Pitfall 3 (cardinality explosion), Pitfall 18 (missing network-online.target), Pitfall 20 (Type=forking for Go binary), Pitfall 23 (parallel counter drift)

**Research flag:** Standard patterns. Skip `research-phase` — Prometheus instrumentation and systemd unit files are exhaustively documented.

---

### Phase 2: Windows Service

**Rationale:** Depends on the `run()` extraction from Phase 1. Uses `golang.org/x/sys/windows/svc` already in `go.mod` — no new module unless `kardianos/service` is chosen for the cross-platform abstraction. Medium complexity (~150–200 LOC). Must be developed and tested in a Windows environment (or Windows CI). Isolated in `service_windows.go` / `service_notwindows.go` so it cannot affect Linux builds.

**Delivers:**
- `cee-exporter.exe install` / `uninstall` / `start` / `stop` / `status` CLI subcommands
- SCM-managed service with Automatic Delayed Start and restart recovery actions
- Windows Event Log start/stop entries
- `go.mod` addition: `github.com/kardianos/service` (if chosen over direct `x/sys/windows/svc`)

**Uses:** `golang.org/x/sys/windows/svc` (already in go.mod), optionally `github.com/kardianos/service`

**Implements:** `cmd/cee-exporter/service_windows.go` (new), `cmd/cee-exporter/service_notwindows.go` (new), `cmd/cee-exporter/main.go` (modified — add `-service` flag)

**Avoids:** Pitfall 4 (30-second SCM startup timeout — send StartPending first), Pitfall 5 (SIGTERM/SCM conflict — gate os/signal.Notify behind !IsWindowsService()), Pitfall 6 (panic in Execute — wrap body in defer recover()), Pitfall 24 (NSSM dependency — use native svc.Run())

**Research flag:** Needs review of `kardianos/service` vs direct `x/sys/windows/svc` API. The architecture research recommends `kardianos/service` for the clean abstraction; the stack research recommends direct `x/sys`. Resolve by inspecting kardianos/service source to confirm it delegates to `x/sys` with no overhead. Either choice is valid; decide before coding.

---

### Phase 3: SIEM Writers (Syslog + Beats)

**Rationale:** SyslogWriter and BeatsWriter are independent of each other and of Phase 2. Both implement the existing `Writer` interface at well-defined extension points. Both can be developed and tested on Linux. SyslogWriter should be built first because it has slightly lower risk (no ACK windowing protocol) and broader SIEM compatibility. BeatsWriter second because it requires a running Logstash or Graylog Beats Input for integration testing. Parallel development is possible if two engineers are available.

**Delivers:**
- `SyslogWriter`: RFC 5424 structured syslog over UDP/TCP to any syslog server; all audit fields in SD-ELEMENT; no Windows build constraints
- `BeatsWriter`: Lumberjack v2 client sending to Logstash/Graylog Beats Input; TLS support; ECS-aligned field names
- `go.mod` additions: `crewjam/rfc5424@v0.1.0` (SyslogWriter), `elastic/go-lumber@v0.1.1` (BeatsWriter)
- New config fields: `syslog_*`, `beats_*` in `[output]` section
- New `type` values: `"syslog"`, `"beats"` in `buildWriter()`

**Uses:** `github.com/crewjam/rfc5424` v0.1.0, `github.com/elastic/go-lumber` v0.1.1, stdlib `net`

**Implements:** `pkg/evtx/writer_syslog.go` (new), `pkg/evtx/writer_beats.go` (new), `main.go` `buildWriter()` (modified), `config.toml.example` (modified)

**Avoids:**
- Pitfall 11 (AsyncClient callback deadlock — use SyncClient)
- Pitfall 12 (SyncClient not thread-safe — sync.Mutex, mirror GELFWriter)
- Pitfall 13 (TLS dropped on reconnect — use custom dialFunc for both initial and reconnect paths)
- Pitfall 14 (log/syslog Windows build failure — use crewjam/rfc5424 which has no platform constraints)
- Pitfall 15 (stdlib syslog not RFC 5424 compliant — use library, not stdlib)
- Pitfall 16 (nil pointer on closed UDP conn — hold mutex for close-and-nil sequence)
- Pitfall 17 (unescaped backslash in SD-PARAM values — use library with built-in escaping)
- Pitfall 22 (MultiWriter masks per-writer failures — add `writer` label to error counter)
- Pitfall 26 (BeatsWriter missing read timeout — set `v2.Timeout(5s)`)

**Research flag:** SyslogWriter uses a known, stable pattern. BeatsWriter is MEDIUM confidence — the go-lumber API is official but the library has not been updated since 2021. Verify `SyncDial` vs `SyncDialWith` TLS pattern by reading go-lumber source before coding the reconnect path.

---

### Phase 4: BinaryEvtxWriter

**Rationale:** Isolated last because it is the highest-complexity feature in v2.0, has no external library, requires the deepest format knowledge, and is entirely independent of all other phases. Blocking other deliverables on this would be wrong — the other 5 features provide immediate production value. The BinXML implementation should be built incrementally: file header first, then chunk header with checksums, then a minimal BinXML token stream for a single EventID, validated at each step against the `0xrawsec/golang-evtx` parser before proceeding.

**Delivers:**
- Real `BinaryEvtxWriter` replacing the stub in `writer_evtx_stub.go`
- Valid `.evtx` files readable by Windows Event Viewer, Splunk, Elastic Agent, Autopsy
- `pkg/evtx/binxml/` subpackage: `chunk.go`, `header.go`, `token.go`, `sid.go`, `filetime.go`
- Round-trip test suite using `0xrawsec/golang-evtx` as a test-only parse dependency
- Zero new external runtime dependencies (stdlib only)

**Uses:** `encoding/binary`, `hash/crc32`, `unicode/utf16`, `bytes`, `os`, `sync` (all stdlib)

**Implements:** `pkg/evtx/writer_binary_evtx.go` `//go:build !windows` (new, replaces stub), `pkg/evtx/binxml/*.go` (new subpackage), `pkg/evtx/writer_evtx_stub.go` (removed or tagged `//go:build ignore`)

**Avoids:**
- Pitfall 7 (dual chunk checksum — compute event records CRC first at offset 52, then header CRC at offset 124)
- Pitfall 8 (record trailing size copy — buffer record in bytes.Buffer, append size copy before write)
- Pitfall 9 (string table offsets are chunk-relative — maintain chunk-local intern map, reset per chunk)
- Pitfall 10 (stub/real symbol collision — remove or ignore the stub file before adding real implementation)
- Pitfall 25 (chunk boundary overflow — check bytesUsed + recordSize before appending each record)

**Research flag:** NEEDS deeper research during planning. The BinXML token encoding (especially NormalSubstitution, TemplateInstance tokens, SID binary encoding, FILETIME encoding) requires working through the MS-EVEN6 specification in detail before writing code. Recommend a `research-phase` task specifically for BinXML token format and a spike implementation that writes a single valid event record and validates it before the full implementation begins.

---

### Phase Ordering Rationale

- **Phase 1 before Phase 2:** The `main()` → `run()` refactor in Phase 1 is a prerequisite for the `runWithServiceManager(run)` pattern in Phase 2. If done out of order, Phase 2 would need to refactor `main.go` itself, creating unnecessary merge conflict surface.
- **Phases 3 and 4 after Phase 1:** Both add to `buildWriter()` in `main.go`. Having the Prometheus-refactored `main.go` as a stable base before adding writer cases reduces churn.
- **Phase 4 last:** BinaryEvtxWriter is independent of all other phases technically, but its complexity warrants isolation at the end when the codebase is stable and the writer infrastructure is proven.
- **Phase 3 writers parallel-capable:** `writer_syslog.go` and `writer_beats.go` have zero dependency on each other. A second engineer can start BeatsWriter while the first completes SyslogWriter.
- **Systemd unit in Phase 1 (not standalone):** Shipping it with Prometheus ensures the unit file's `ExecStart` path and environment variables align with the same binary that exposes `/metrics`. They belong together operationally.

---

### Research Flags

**Needs deeper research during planning (`research-phase` recommended):**

- **Phase 4 (BinaryEvtxWriter):** BinXML token encoding (TemplateInstance, NormalSubstitution), SID binary format, FILETIME encoding, and string table management require format-spec-level research before coding. Strongly recommend a spike implementation validated against `wevtutil qe` or the Rust `omerbenamram/evtx` crate before committing to the full implementation.
- **Phase 2 (Windows Service):** Resolve `kardianos/service` vs direct `x/sys/windows/svc` API choice. Both research files give different recommendations; this needs a 30-minute review of kardianos/service source to determine if the abstraction adds value for this use case.

**Standard patterns — skip `research-phase`:**

- **Phase 1 (Prometheus):** Official `prometheus/client_golang` docs are exhaustive; the Collector pattern for wrapping atomics is well-established.
- **Phase 1 (Systemd):** Unit file is a known artifact; patterns verified against current systemd man pages.
- **Phase 3 (SyslogWriter):** `crewjam/rfc5424` API is minimal; RFC 5424 is a stable spec.
- **Phase 3 (BeatsWriter):** `go-lumber` `SyncClient` API is straightforward; verify TLS path from source before coding (30 minutes, not a full research phase).

---

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All library choices verified via pkg.go.dev official docs; versions confirmed from GitHub releases pages; CGO_ENABLED=0 compatibility confirmed per source/issue reports |
| Features | HIGH | Feature expectations derived from production Go daemon conventions; table stakes list is consensus among Prometheus, systemd, and Windows service documentation |
| Architecture | HIGH | Derived directly from reading the v1 codebase; integration points are unambiguous; extension patterns already established by GELFWriter and Win32Writer |
| Pitfalls | HIGH | 27 pitfalls derived from: codebase inspection, official library docs, confirmed Go issue reports (golang/go#23479, golang/go#66666), and libevtx format specification |

**Overall confidence: HIGH**

### Gaps to Address

- **go-lumber TLS reconnect path:** The exact API for TLS-aware reconnect (`SyncDialWith` vs `NewSyncClientWithConn`) is not definitively confirmed from documentation alone. Read go-lumber source for the `client/v2` package before implementing `BeatsWriter.connect()`. Risk: LOW (affects one function in one writer).

- **kardianos/service vs direct x/sys/windows/svc:** The two research files give conflicting recommendations. STACK.md recommends direct `x/sys/windows/svc`; ARCHITECTURE.md recommends `kardianos/service`. Resolution: if the kardianos/service abstraction cleanly delegates to `x/sys` without reimplementing SCM protocol, prefer it for the simpler API. If it adds behavior that conflicts with the existing `x/sys` EventLog writer, use direct `x/sys`. This is a 30-minute decision, not a blocker.

- **BinaryEvtxWriter scope estimate:** The 600–1200 LOC estimate is based on format specification review, not a working implementation. The actual scope may differ significantly. A spike implementation (targeting a single valid EventID 4663 record) should be the first deliverable in Phase 4 to validate the estimate before full implementation is committed.

- **BeatsWriter go-lumber maintenance status:** `elastic/go-lumber` has not been updated since January 2021. The Lumberjack v2 protocol is frozen, so library staleness is not a functional risk. However, any Go toolchain compatibility issues (e.g., `go vet` changes, new linter rules) would require a fork. Log this as a low-priority maintenance note.

---

## Sources

### Primary (HIGH confidence)

- `prometheus/client_golang` GitHub releases / pkg.go.dev — v1.23.2 confirmed 2025-09-05; Collector interface, custom registry, promhttp.HandlerFor
- `golang.org/x/sys/windows/svc` pkg.go.dev — v0.31.0; Execute method, service state machine, svc/mgr CreateService API
- `elastic/go-lumber` client/v2 pkg.go.dev — SyncClient.Send(), AsyncClient, TLS dial options, callback deadlock warning
- `crewjam/rfc5424` pkg.go.dev — v0.1.0; Message type, WriteTo(io.Writer) API; pure Go confirmed
- `kardianos/service` pkg.go.dev — v1.x; Interface (Start/Stop/Run), July 2025 release confirmed
- libyal/libevtx EVTX format specification — chunk header layout, dual checksums, string table offsets, BinXML token vocabulary
- `0xrawsec/golang-evtx` source — ChunkHeader struct; confirmed read-only parser (no write capability)
- `golang/go#23479` — 30-second SCM startup timeout root cause confirmed
- `golang/go#66666` — `log/syslog` missing RFC 5424 VERSION field; Windows build exclusion confirmed
- RFC 5424 (IETF datatracker) — STRUCTURED-DATA escaping rules (`]`, `"`, `\`), SD-ID uniqueness constraint
- systemd.service man page — Type=, After=, ExecStart absolute path requirement, network-online.target
- Direct codebase inspection: `main.go`, `writer_gelf.go`, `writer.go`, `metrics.go`, `queue.go`, `server.go`, `health.go`

### Secondary (MEDIUM confidence)

- `nathanaelle/syslog5424` pkg.go.dev — confirmed UDP exclusion by design (informs library rejection)
- `elastic/go-lumber` GitHub (v0.1.1, 56 stars, CI passes) — stability assessment for unmaintained library
- MS-EVEN6 BinXML specification (learn.microsoft.com) — BinXML token byte assignments; informative (not normative for implementation details below token level)
- `coreos/go-systemd/daemon` pkg.go.dev — optional sd_notify READY=1 integration for `Type=notify`

### Tertiary (LOW confidence / deferred validation)

- BinaryEvtxWriter LOC estimate (600–1200) — based on format spec review; validate with spike implementation in Phase 4
- go-lumber TLS reconnect exact API — confirm from source before BeatsWriter implementation

---
*Research completed: 2026-03-03*
*Ready for roadmap: yes*
