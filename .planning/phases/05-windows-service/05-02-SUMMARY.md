---
phase: 05-windows-service
plan: "02"
subsystem: testing
tags: [go, tdd, windows-service, parseCfgPath, table-driven-tests]

# Dependency graph
requires:
  - phase: 05-01-windows-service
    provides: "service_helpers.go with parseCfgPath implementation, run() accepting context.Context"
provides:
  - "TestParseCfgPath: 7-case table-driven test suite for parseCfgPath in service_helpers_test.go"
  - "Formal TDD coverage for parseCfgPath — extracts -config flag value from os.Args"
affects: [05-03-windows-service]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "White-box test (package main) for internal helper functions"
    - "Table-driven t.Run tests with stdlib only, no testify"
    - "TDD RED-GREEN-REFACTOR: test file committed before/with implementation"

key-files:
  created:
    - cmd/cee-exporter/service_helpers_test.go
  modified: []

key-decisions:
  - "parseCfgPath has no build tag so it compiles and tests on Linux CI without Win32 surface"
  - "Test uses package main (white-box) to access unexported symbols per CLAUDE.md convention"
  - "7 table-driven cases cover: default, -config, --config, subcommand prefix, dangling flag, other flags, uninstall"

patterns-established:
  - "TDD for CLI flag parsing helpers: test first, implement minimal, no build tags needed"

requirements-completed: [DEPLOY-03]

# Metrics
duration: 4min
completed: 2026-03-03
---

# Phase 5 Plan 02: parseCfgPath TDD Tests Summary

**Table-driven TestParseCfgPath covering 7 cases (default, -config, --config, subcommand prefix, dangling flag, other flags, uninstall) with stdlib-only white-box tests compilable on all platforms**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-03T16:35:53Z
- **Completed:** 2026-03-03T16:39:52Z
- **Tasks:** 1 (TDD RED-GREEN-REFACTOR)
- **Files modified:** 1 created (service_helpers_test.go)

## Accomplishments

- Created service_helpers_test.go with TestParseCfgPath covering all 7 specified test cases
- Confirmed RED state (compile error: parseCfgPath undefined, then FAIL with stub returning "")
- Confirmed GREEN state (all 7 subtests pass with existing implementation from 05-01)
- No build tag on test helpers — full CI coverage on Linux without Win32 API surface
- Full suite: 44/44 tests pass, no regressions

## Task Commits

Each task was committed atomically:

1. **RED: TestParseCfgPath 7-case table-driven test file** - `2c06eb4` (test)
2. **GREEN: parseCfgPath implementation already present from 05-01** - `94ccded` (feat, prior plan)

_Note: The GREEN implementation was committed in 05-01 (parseCfgPath in service_helpers.go). The 05-02 plan's contribution is the formal TDD test suite that validates all edge cases._

## Files Created/Modified

- `cmd/cee-exporter/service_helpers_test.go` — TestParseCfgPath with 7 table-driven cases: no args, -config, --config, install subcommand, dangling flag, other flags, uninstall

## Decisions Made

- parseCfgPath has no build tag — pure Go, no OS constraints, compiles on Linux CI
- Test file declares `package main` (white-box) matching project convention in CLAUDE.md
- Stdlib only: no testify or external test dependencies per CLAUDE.md constraint

## Deviations from Plan

None - plan executed exactly as written. The implementation was already present from 05-01 (which noted "Implement parseCfgPath() stub"), enabling the GREEN phase immediately after writing tests.

## Issues Encountered

- `runWithServiceManager(run)` appeared to have a type mismatch in main.go at plan start, but investigation revealed 05-01 had already refactored `runWithServiceManager` to accept `func(ctx context.Context)` matching `run`'s signature — the build passed cleanly after understanding the 05-01 state.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- parseCfgPath is fully tested and verified — ready for Phase 5 Plan 03 (real SCM wrapper on Windows)
- service_windows.go placeholder stub awaits replacement with kardianos/service SCM integration
- All 44 tests green, CGO_ENABLED=0 builds pass on linux/amd64 and windows/amd64

## Self-Check: PASSED

- `cmd/cee-exporter/service_helpers.go` — FOUND
- `cmd/cee-exporter/service_helpers_test.go` — FOUND
- `.planning/phases/05-windows-service/05-02-SUMMARY.md` — FOUND
- Commit `2c06eb4` (test: RED test file) — FOUND
- Commit `94ccded` (feat: implementation from 05-01) — FOUND
- All 44 tests pass

---
_Phase: 05-windows-service_
_Completed: 2026-03-03_
