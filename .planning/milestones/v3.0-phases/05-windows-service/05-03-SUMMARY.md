---
phase: 05-windows-service
plan: "03"
subsystem: infra
tags: [go, windows-service, kardianos-service, scm, install, uninstall, recovery-actions, context-cancellation]

# Dependency graph
requires:
  - phase: 05-01
    provides: kardianos/service v1.2.4 dependency, run(ctx context.Context) signature
  - phase: 05-02
    provides: parseCfgPath() helper function (service_helpers.go)
provides:
  - Full Windows SCM wrapper in service_windows.go (svcProgram, svcConfig, runWithServiceManager)
  - install subcommand registers service with SCM using Automatic Delayed Start
  - uninstall subcommand removes service from SCM cleanly
  - Stop() bridges SCM stop control code into run() context cancellation
  - Recovery actions: restart after 5s on any failure, reset after 24h
affects:
  - 06-syslog-beats (no change — service layer is fully wired)
  - 07-evtx (no change — service layer is fully wired)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "service.Interface pattern: Start() launches goroutine (non-blocking), Stop() calls cancel()"
    - "SCM Arguments stores only -config path — never includes subcommand (prevents infinite install loop)"
    - "kardianos/service KeyValue options map for DelayedAutoStart and OnFailure recovery"

key-files:
  created: []
  modified:
    - cmd/cee-exporter/service_windows.go

key-decisions:
  - "Start() must return immediately — runFn launched in goroutine; blocking Start() causes SCM timeout"
  - "Stop() calls cancel() only — never os.Exit(); SCM performs cleanup after Stop() returns"
  - "Arguments in svcConfig stores [-config, cfgPath] only — subcommand stripped to prevent SCM boot loop"
  - "OnFailureDelayDuration is string '5s' (not int) per kardianos/service v1.2.4 API"
  - "OnFailureResetPeriod is int 86400 (seconds) — resets failure count after 24h"

patterns-established:
  - "SCM Stop() → context cancel bridge: Stop() calls p.cancel(), run()'s select on ctx.Done() handles shutdown"
  - "Subcommand dispatch before flag.Parse(): check os.Args[1] before delegating to s.Run() or s.Install()"

requirements-completed: [DEPLOY-03, DEPLOY-04, DEPLOY-05]

# Metrics
duration: 1min
completed: 2026-03-03
---

# Phase 5 Plan 03: Windows Service — Full SCM Wrapper Summary

**kardianos/service SCM integration replacing stub: install/uninstall subcommands, Automatic Delayed Start, recovery restart-on-failure, and Stop() to context.CancelFunc bridge**

## Performance

- **Duration:** 1 min
- **Started:** 2026-03-03T16:42:48Z
- **Completed:** 2026-03-03T16:43:48Z
- **Tasks:** 1
- **Files modified:** 1

## Accomplishments

- Replaced service_windows.go stub with full service.Interface implementation using kardianos/service v1.2.4
- svcProgram.Start() launches run() in goroutine (non-blocking, complies with SCM 30s timeout)
- svcProgram.Stop() calls cancel() to bridge SCM stop control into run()'s context cancellation select
- svcConfig() configures Automatic Delayed Start (avoids boot timeout) and recovery actions (5s restart, 24h reset, non-crash exits included)
- runWithServiceManager() dispatches install/uninstall subcommands before flag.Parse(); parseCfgPath() strips subcommand from SCM Arguments

## Task Commits

Each task was committed atomically:

1. **Task 1: Implement full service_windows.go SCM wrapper** - `1621dde` (feat)

**Plan metadata:** (docs commit follows)

## Files Created/Modified

- `cmd/cee-exporter/service_windows.go` - Full SCM wrapper: svcProgram (service.Interface), svcConfig (Automatic Delayed Start + recovery), runWithServiceManager (install/uninstall dispatch + s.Run())

## Decisions Made

- Start() must not block — SCM expects Start() to return quickly; runFn launched in goroutine
- Stop() calls cancel() only, never os.Exit() — SCM handles cleanup after Stop() returns; calling os.Exit() from Stop() bypasses SCM state machine
- Arguments stores only [-config, cfgPath] — parseCfgPath() strips the subcommand token to prevent SCM re-running "install" on every boot (infinite install loop)
- OnFailureDelayDuration is string "5s" per kardianos/service v1.2.4 KeyValue API (not int)
- OnFailureResetPeriod is int 86400 (seconds) to reset failure counter after 24 hours

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

Manual verification on Windows requires Administrator privileges:

1. DEPLOY-03: `cee-exporter.exe install` — service appears in services.msc as "CEE Exporter" with "Automatic (Delayed Start)"
2. DEPLOY-04: `cee-exporter.exe uninstall` — service disappears from services.msc and registry
3. DEPLOY-05: Recovery tab in services.msc shows "Restart the Service (5 seconds delay)" on first failure; "Enable actions for stops with errors" is ticked

Linux CI verification (automated) passed:

- CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... — OK
- CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... — OK
- go test ./... — 44 tests pass

## Next Phase Readiness

- Phase 5 (Windows Service) is fully complete — all three plans executed
- DEPLOY-03, DEPLOY-04, DEPLOY-05 requirements addressed
- service_windows.go is production-ready; no further changes needed in Phase 6 or 7
- Phase 6 (Syslog/Beats writers) can proceed independently

## Self-Check: PASSED

- FOUND: cmd/cee-exporter/service_windows.go
- FOUND: .planning/phases/05-windows-service/05-03-SUMMARY.md
- FOUND: commit 1621dde (feat(05-03): implement full Windows SCM wrapper)

---
*Phase: 05-windows-service*
*Completed: 2026-03-03*
