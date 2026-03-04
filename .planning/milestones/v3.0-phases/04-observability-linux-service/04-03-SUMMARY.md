---
phase: 04-observability-linux-service
plan: 03
subsystem: infra
tags: [go, prometheus, metrics, service-manager, config, toml]

# Dependency graph
requires:
  - phase: 04-01
    provides: pkg/prometheus/handler.go with ceeprometheus.Serve(addr)
provides:
  - run() function extracted from main() — prerequisite for Phase 5 Windows Service wrapper
  - MetricsConfig struct with Enabled=true, Addr=0.0.0.0:9228 defaults wired into Config
  - Prometheus metrics goroutine started inside run() on dedicated port 9228
  - service_notwindows.go shim (//go:build !windows) — runWithServiceManager calls fn directly
  - service_windows.go stub (//go:build windows) — Phase 5 placeholder for SCM wrapper
  - config.toml.example [metrics] section documenting enabled and addr fields
affects:
  - 05-windows-service
  - operators deploying cee-exporter (new [metrics] config section)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - main() → runWithServiceManager(run) dispatch pattern for service manager compatibility
    - run() contains all daemon logic; main() is a one-liner delegation
    - platform-specific service manager shims via build tags (service_notwindows.go / service_windows.go)

key-files:
  created:
    - cmd/cee-exporter/service_notwindows.go
    - cmd/cee-exporter/service_windows.go
  modified:
    - cmd/cee-exporter/main.go
    - config.toml.example

key-decisions:
  - "service_windows.go placeholder stub created now so CGO_ENABLED=0 GOOS=windows build succeeds; Phase 5 replaces it with real SCM wrapper"
  - "Metrics goroutine inserted after CEPA HTTP server goroutine and before cee_exporter_ready log line"
  - "Port 9228 for metrics, separate from CEPA port 12228"

patterns-established:
  - "main() is a one-liner: runWithServiceManager(run)"
  - "run() contains all daemon logic verbatim — never inline in main()"
  - "Platform service shims use build tags: service_notwindows.go (!windows) and service_windows.go (windows)"

requirements-completed: [OBS-05]

# Metrics
duration: 28min
completed: 2026-03-03
---

# Phase 4 Plan 03: main.go Refactor — run() Extraction, MetricsConfig, Prometheus Wiring Summary

**Prometheus metrics server wired on port 9228 via ceeprometheus.Serve() goroutine inside extracted run() function, with service_notwindows.go shim enabling Phase 5 Windows Service wrapper**

## Performance

- **Duration:** 28 min
- **Started:** 2026-03-03T14:42:44Z
- **Completed:** 2026-03-03T15:11:35Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Extracted run() from main() — main() is now a one-liner: `runWithServiceManager(run)`
- Added MetricsConfig struct (Enabled=true, Addr="0.0.0.0:9228") to Config; wired ceeprometheus.Serve goroutine
- Created service_notwindows.go (//go:build !windows) shim so Phase 5 can drop in service_windows.go SCM wrapper
- Updated config.toml.example with [metrics] section documenting enabled and addr fields
- All 36 existing tests pass; CGO_ENABLED=0 builds succeed for both linux and windows targets

## Task Commits

Each task was committed atomically:

1. **Task 1: Refactor main.go — extract run(), add MetricsConfig, wire metrics goroutine** - `edfa7da` (feat)
2. **Task 2: Create service_notwindows.go shim and update config.toml.example** - `0295e81` (feat)

**Plan metadata:** (docs commit follows)

## Files Created/Modified
- `cmd/cee-exporter/main.go` - run() extracted from main(); MetricsConfig added; ceeprometheus.Serve goroutine wired
- `cmd/cee-exporter/service_notwindows.go` - //go:build !windows shim; runWithServiceManager calls fn directly
- `cmd/cee-exporter/service_windows.go` - //go:build windows placeholder stub for Phase 5 SCM wrapper
- `config.toml.example` - [metrics] section added (enabled=true, addr=0.0.0.0:9228)

## Decisions Made
- Created `service_windows.go` placeholder stub (not in the original plan) so that `CGO_ENABLED=0 GOOS=windows go build` succeeds immediately — required by the plan's verification criteria. Phase 5 will replace it with the real Windows Service Control Manager implementation.
- Metrics goroutine inserted after CEPA HTTP server starts, before `cee_exporter_ready` log line — consistent with startup sequence.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Added service_windows.go placeholder stub**
- **Found during:** Task 1 (Windows cross-compile verification)
- **Issue:** `CGO_ENABLED=0 GOOS=windows go build ./cmd/cee-exporter/...` failed with "undefined: runWithServiceManager" because service_notwindows.go has `//go:build !windows` and there was no Windows equivalent
- **Fix:** Created cmd/cee-exporter/service_windows.go with `//go:build windows` and a trivial runWithServiceManager shim identical in behavior to the non-Windows version; Phase 5 will replace it with a real SCM wrapper
- **Files modified:** cmd/cee-exporter/service_windows.go (created)
- **Verification:** CGO_ENABLED=0 GOOS=windows build succeeds
- **Committed in:** edfa7da (Task 1 commit)

---

**Total deviations:** 1 auto-fixed (1 blocking)
**Impact on plan:** The Windows stub is essential for the plan's own verification criteria. No scope creep — it is a placeholder, not a feature.

## Issues Encountered
None beyond the Windows build stub described above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 5 (Windows Service): run() is ready to be wrapped by kardianos/service or x/sys/windows/svc — replace service_windows.go
- Prometheus metrics endpoint will be live on port 9228 when daemon starts (Enabled=true default)
- Operators: add [metrics] section to config.toml if customisation needed (see config.toml.example)

---
*Phase: 04-observability-linux-service*
*Completed: 2026-03-03*
