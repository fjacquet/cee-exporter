---
phase: 09-goroutine-scaffolding-and-fsync
plan: 01
subsystem: evtx
tags: [go-evtx, goroutine, sync, ticker, rotation, background-flush, race-detector]

# Dependency graph
requires:
  - phase: 08.5-go-evtx-oss-module-extraction
    provides: go-evtx v0.1.0 with Writer, WriteRaw, WriteRecord, Close
provides:
  - go-evtx v0.2.0 with RotationConfig struct and background flush goroutine
  - backgroundLoop goroutine with ticker-driven checkpoint writes under w.mu
  - Correct Close() shutdown ordering: close(done) -> wg.Wait() -> mu.Lock() -> final flush
  - Seven goroutine lifecycle tests in goroutine_test.go
  - Zero data races confirmed by go test -race ./...
affects:
  - 09-02 (cee-exporter replace directive to use go-evtx v0.2.0)
  - Phase 10 (open-handle flush builds on goroutine lifecycle)
  - Phase 11 (rotation builds on flush/close ordering)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Goroutine lifecycle: done chan struct{} + sync.WaitGroup for clean shutdown"
    - "Ticker gate: if cfg.FlushIntervalSec > 0 { ... } prevents time.NewTicker(0) panic"
    - "Close() ordering: close(done) -> wg.Wait() -> mu.Lock() -> flush (avoids deadlock)"
    - "TDD: goroutine_test.go written first (RED), then evtx.go updated (GREEN)"

key-files:
  created:
    - ~/Projects/go-evtx/goroutine_test.go
  modified:
    - ~/Projects/go-evtx/evtx.go
    - ~/Projects/go-evtx/evtx_test.go
    - ~/Projects/go-evtx/reader_test.go

key-decisions:
  - "RotationConfig.FlushIntervalSec == 0 disables goroutine entirely; time.NewTicker(0) is never called"
  - "Close() uses close(done) before wg.Wait() — goroutine exits then Close() acquires mu for final flush"
  - "backgroundLoop flushes only when len(w.records) > 0 — avoids empty file writes on idle tickers"
  - "sync.Once not required here since plan specifies Close() called exactly once; noted in code comment"

patterns-established:
  - "go-evtx shutdown pattern: close(done) -> wg.Wait() -> mu.Lock() -> flush"
  - "Ticker guard: always gate time.NewTicker behind interval > 0 check"

requirements-completed: [FLUSH-01, FLUSH-02]

# Metrics
duration: 4min
completed: 2026-03-04
---

# Phase 09 Plan 01: Goroutine Scaffolding and Fsync Summary

**go-evtx v0.2.0: RotationConfig struct + background ticker goroutine with correct done/WaitGroup close ordering and zero data races**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-04T23:23:44Z
- **Completed:** 2026-03-04T23:27:34Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Added RotationConfig struct to go-evtx; New() now accepts (path, cfg RotationConfig)
- Implemented backgroundLoop goroutine that calls flushToFile() under w.mu on each ticker tick; started only when FlushIntervalSec > 0
- Updated Close() with non-negotiable shutdown ordering: close(done) -> wg.Wait() -> mu.Lock() -> final flush
- Wrote seven goroutine lifecycle tests covering ticker fire, graceful shutdown, zero-interval guard, concurrency, and leak detection
- Confirmed zero data races with go test -race ./... -count=1
- Published v0.2.0 tag to github.com/fjacquet/go-evtx

## Task Commits

Each task was committed atomically:

1. **Task 1: Write goroutine tests (RED), then implement RotationConfig + backgroundLoop (GREEN)** - `fa7b552` (feat)
2. **Task 2: Update existing tests for new New() signature, race detector, tag v0.2.0** - `d9bc78d` (test)

**Plan metadata:** (docs commit follows)

_Note: TDD task had a single commit covering both RED test creation and GREEN implementation, per plan instruction to commit after all steps pass._

## Files Created/Modified
- `~/Projects/go-evtx/evtx.go` - Added RotationConfig struct, done/wg fields to Writer, updated New(), added backgroundLoop(), updated Close() with correct ordering
- `~/Projects/go-evtx/goroutine_test.go` - Seven goroutine lifecycle tests (new file)
- `~/Projects/go-evtx/evtx_test.go` - Updated all New(path) calls to New(path, RotationConfig{})
- `~/Projects/go-evtx/reader_test.go` - Updated all New(path) calls to New(path, RotationConfig{})

## Decisions Made
- `RotationConfig.FlushIntervalSec == 0` disables the goroutine entirely; no ticker created; no goroutine leak possible when interval is zero
- Close() shutdown ordering is non-negotiable: acquire done before mu to avoid deadlock with backgroundLoop which holds mu while flushing
- `backgroundLoop` skips flushToFile when `len(w.records) == 0` to avoid creating empty files during idle periods
- reader_test.go was also updated in Task 2 (not mentioned in plan but required for compilation)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Updated reader_test.go New() calls in addition to evtx_test.go**
- **Found during:** Task 1 (GREEN step — running go test ./... after evtx.go update)
- **Issue:** reader_test.go also contained New(path) calls without RotationConfig; plan only mentioned evtx_test.go but reader_test.go was failing compilation
- **Fix:** Updated three New() calls in reader_test.go to New(path, RotationConfig{})
- **Files modified:** ~/Projects/go-evtx/reader_test.go
- **Verification:** go test ./... -count=1 passes; all 30+ tests green
- **Committed in:** d9bc78d (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 blocking)
**Impact on plan:** Necessary to unblock compilation. Strictly a signature migration, no logic change.

## Issues Encountered
- None beyond the deviation above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- go-evtx v0.2.0 is published to github.com/fjacquet/go-evtx with RotationConfig API
- Plan 09-02 can now add a `replace` directive in cee-exporter go.mod pointing to go-evtx v0.2.0
- Goroutine concurrency contract established; Phase 10 (open-handle flush) can build on it

---
*Phase: 09-goroutine-scaffolding-and-fsync*
*Completed: 2026-03-04*
