# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-03)

**Core value:** Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.
**Current focus:** Phase 5 — Windows Service

## Current Position

Milestone: v2.0 Operations & Output Expansion
Phase: 5 of 7 (Windows Service)
Plan: 2 of 3 completed
Status: In progress
Last activity: 2026-03-03 — 05-02 complete: TestParseCfgPath 7-case table-driven tests, all 44 tests green

Progress: [████░░░░░░] ~22% (Phase 4 complete, Phase 5 plans 05-01 and 05-02 complete)

## Performance Metrics

**Velocity:**

- Total plans completed: 9
- Average duration: 5 min
- Total execution time: 46 min

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01-quality | 3 | 6 min | 2 min |
| 02-build | 1 | 2 min | 2 min |
| 03-documentation | 2 | 4 min | 2 min |
| 04-observability-linux-service | 3 | 30 min | 10 min |
| 05-windows-service | 2 (in progress) | 6 min | 3 min |

**Recent Trend:**

- Last 5 plans: 03-02 (2 min), 04-02 (1 min), 04-03 (28 min), 05-01 (2 min), 05-02 (4 min)
- Trend: Stable

*Updated after each plan completion*

| Phase 05-windows-service P02 | 4 min | 1 task | 1 file |

## Accumulated Context

### Decisions

Full decision log in PROJECT.md Key Decisions table.

Recent decisions affecting v2.0:
- Use `prometheus/client_golang` v1.23.2+ to avoid CGO_ENABLED=0 breakage from older transitive deps
- Prometheus /metrics on dedicated port 9228 (not on CEPA mux port 12228) — avoids TLS scrape config and log pollution
- Use `crewjam/rfc5424` + `net.Conn` for SyslogWriter — avoids stdlib `log/syslog` Windows build exclusion
- BeatsWriter wraps `go-lumber` SyncClient behind `sync.Mutex` — mirrors GELFWriter reconnect pattern
- BinaryEvtxWriter isolated in Phase 7 — highest complexity, independent of all other phases
- Phase 5 (Windows Service) depends on Phase 4 for `main()` → `run()` refactor before adding service wrapper
- 04-02: Type=simple for Go daemon systemd unit (not Type=notify — sd_notify deferred to OBS-F02)
- 04-02: Wants=network-online.target (not Requires=) to avoid hard boot failure if networkd absent
- [Phase 04-observability-linux-service]: Used prometheus.NewRegistry() (private) not DefaultRegisterer — keeps scrape output clean (cee_* only)
- [Phase 04-observability-linux-service]: Package named ceeprometheus to avoid import collision with prometheus client library
- [Phase 04-observability-linux-service]: service_windows.go placeholder stub created so CGO_ENABLED=0 GOOS=windows build succeeds; Phase 5 replaces with real SCM wrapper
- [Phase 04-observability-linux-service]: Port 9228 for Prometheus metrics — separate from CEPA port 12228; default Enabled=true in MetricsConfig
- [Phase 05-windows-service]: run() accepts ctx parameter for SCM Stop() compatibility — context cancellation bridges into shutdown select alongside SIGTERM/SIGINT
- [Phase 05-windows-service]: kardianos/service v1.2.4 as direct dependency — Windows SCM wrapper uses kardianos/service API in Plan 03
- [Phase 05-windows-service]: parseCfgPath has no build tag — compiles on Linux CI without Win32 surface, enabling full TDD coverage cross-platform
- [Phase 05-windows-service]: Test file uses package main (white-box) per CLAUDE.md convention; stdlib only, no testify

### Pending Todos

None.

### Blockers/Concerns

- Win32 EventID registration: IDs 4663/4660/4670 may need message DLL for correct Event Viewer display — deferred to v2 follow-up
- Phase 7 scope estimate (600-1200 LOC) unvalidated — spike implementation required as first Phase 7 task
- Phase 5: kardianos/service chosen (resolved); Plan 02 (install/uninstall) and Plan 03 (SCM wrapper) remain
- Phase 6: verify go-lumber `SyncDialWith` TLS reconnect API from source before BeatsWriter implementation

## Session Continuity

Last session: 2026-03-03
Stopped at: Completed 05-02-PLAN.md — TestParseCfgPath table-driven tests, all 44 tests green; ready for 05-03 SCM wrapper
Resume file: None
