---
phase: 05-windows-service
plan: "01"
subsystem: infra
tags: [go, windows-service, kardianos-service, context, graceful-shutdown]

# Dependency graph
requires:
  - phase: 04-observability-linux-service
    provides: run() extraction from main() and service_notwindows.go shim
provides:
  - github.com/kardianos/service v1.2.4 as direct dependency
  - run(ctx context.Context) signature for SCM-driven shutdown
  - service_notwindows.go passing context.Background() to run()
  - service_windows.go stub updated to match new func(ctx context.Context) signature
affects:
  - 05-02 (install/uninstall subcommands)
  - 05-03 (Windows SCM wrapper using kardianos/service)

# Tech tracking
tech-stack:
  added: [github.com/kardianos/service v1.2.4]
  patterns: [context-propagation for graceful shutdown, SCM stop via context cancellation]

key-files:
  created: []
  modified:
    - go.mod
    - go.sum
    - cmd/cee-exporter/main.go
    - cmd/cee-exporter/service_notwindows.go
    - cmd/cee-exporter/service_windows.go
    - cmd/cee-exporter/service_helpers.go

key-decisions:
  - "run() accepts ctx parameter — context cancellation from SCM Stop() bridges into shutdown select alongside SIGTERM/SIGINT"
  - "queueCtx derived from ctx parameter (not context.Background()) — propagates SCM cancellation into queue workers"
  - "service_windows.go stub updated minimally (context.Background() shim) to stay compilable until Plan 03 replaces it"

patterns-established:
  - "Shutdown select: case <-sig and case <-ctx.Done() in parallel for dual OS-signal + SCM cancel"
  - "Queue context derived from run() parameter — ctx flows top-down from service manager to all subsystems"

requirements-completed: [DEPLOY-03, DEPLOY-04, DEPLOY-05]

# Metrics
duration: 2min
completed: 2026-03-03
---

# Phase 5 Plan 01: Windows Service — Dependency and Context Refactor Summary

**kardianos/service v1.2.4 added as direct dependency; run() refactored to accept context.Context for SCM Stop() compatibility on both linux/amd64 and windows/amd64**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-03T16:36:02Z
- **Completed:** 2026-03-03T16:38:00Z
- **Tasks:** 1
- **Files modified:** 6

## Accomplishments
- Added `github.com/kardianos/service v1.2.4` as direct dependency in go.mod (not indirect)
- Changed `run()` signature to `func run(ctx context.Context)` enabling SCM Stop() to cancel the daemon cleanly
- Shutdown select handles both `<-sig` (SIGTERM/SIGINT) and `<-ctx.Done()` (SCM cancel)
- Queue context derived from the ctx parameter (not hardcoded `context.Background()`) so SCM cancellation propagates
- Both `service_notwindows.go` and `service_windows.go` stub updated to `func(ctx context.Context)` signature

## Task Commits

Each task was committed atomically:

1. **Task 1: Add kardianos/service dependency and refactor run() to accept context.Context** - `94ccded` (feat)

**Plan metadata:** (docs commit follows)

## Files Created/Modified
- `go.mod` - Added github.com/kardianos/service v1.2.4 as direct dependency
- `go.sum` - Updated checksums for kardianos/service
- `cmd/cee-exporter/main.go` - run() now accepts context.Context; shutdown select handles ctx.Done()
- `cmd/cee-exporter/service_notwindows.go` - Updated to pass context.Background() to func(ctx context.Context)
- `cmd/cee-exporter/service_windows.go` - Stub updated to match new func(ctx context.Context) signature
- `cmd/cee-exporter/service_helpers.go` - parseCfgPath() stub implemented (was returning "" — now correctly parses -config/--config from args)

## Decisions Made
- run() receives the context from the service manager, not creating its own — this enables the Windows SCM Stop() method to cancel the context and trigger clean shutdown without relying on POSIX signals
- Queue context derived from the run() ctx parameter so SCM cancellation propagates to all queue workers
- service_windows.go updated minimally to match new signature while remaining a stub — Plan 03 will replace it entirely with a real SCM wrapper

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed parseCfgPath() stub returning empty string**
- **Found during:** Task 1 (running `go test ./...`)
- **Issue:** `parseCfgPath()` in `service_helpers.go` was a stub returning `""` — 8 tests in `TestParseCfgPath` were failing
- **Fix:** Implemented the function to scan args for `-config`/`--config` flag and return the next arg, defaulting to `"config.toml"` (a linter had already applied the fix when the file was re-read)
- **Files modified:** `cmd/cee-exporter/service_helpers.go`
- **Verification:** `go test ./...` — all 44 tests pass (was 36 pass / 8 fail before)
- **Committed in:** `94ccded` (part of Task 1 commit)

**2. [Rule 3 - Blocking] Simplified main() to call runWithServiceManager(run) directly**
- **Found during:** Task 1 (Windows/amd64 build)
- **Issue:** The previous plan (04-03) had changed `main()` to `runWithServiceManager(func() { run(ctx) })` passing a `func()` wrapper. After updating `runWithServiceManager` to expect `func(ctx context.Context)`, the Windows build failed with type mismatch
- **Fix:** Removed the closure wrapper in `main()`; called `runWithServiceManager(run)` directly as the plan specified
- **Files modified:** `cmd/cee-exporter/main.go`
- **Verification:** Both `CGO_ENABLED=0 GOOS=linux` and `GOOS=windows` builds succeed
- **Committed in:** `94ccded` (part of Task 1 commit)

---

**Total deviations:** 2 auto-fixed (1 bug fix, 1 blocking build fix)
**Impact on plan:** Both fixes required for correctness and build compatibility. No scope creep.

## Issues Encountered
- kardianos/service was initially added as `// indirect` by `go get` since the package isn't imported in code yet. Moved it to the direct require block manually in go.mod to satisfy the plan requirement.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- kardianos/service v1.2.4 dependency is in place for Plan 02 (install/uninstall subcommands) and Plan 03 (Windows SCM wrapper)
- run(ctx context.Context) signature established — Plan 03's SCM wrapper can cancel the context via Stop() method
- All tests pass; both platform builds are green

## Self-Check: PASSED

- FOUND: go.mod
- FOUND: cmd/cee-exporter/main.go
- FOUND: cmd/cee-exporter/service_notwindows.go
- FOUND: cmd/cee-exporter/service_windows.go
- FOUND: cmd/cee-exporter/service_helpers.go
- FOUND: 05-01-SUMMARY.md
- FOUND: commit 94ccded (feat(05-01): add kardianos/service dependency...)

---
*Phase: 05-windows-service*
*Completed: 2026-03-03*
