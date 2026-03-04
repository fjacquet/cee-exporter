---
phase: 01-quality
plan: "01"
subsystem: testing
tags: [go, http, maxbytesreader, bug-fix, regression-test]

# Dependency graph
requires: []
provides:
  - readBody function with non-nil ResponseWriter passed to http.MaxBytesReader
  - Regression tests for readBody: TestReadBodyOversized and TestReadBodyNormal
affects: [01-02, 01-03]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Pass http.ResponseWriter to http.MaxBytesReader to enable proper connection teardown on oversized bodies"
    - "White-box (same-package) test pattern: package server in server_test.go"

key-files:
  created:
    - pkg/server/server_test.go
  modified:
    - pkg/server/server.go

key-decisions:
  - "Pass http.ResponseWriter (not nil) to http.MaxBytesReader so that >64 MiB bodies close the connection gracefully instead of panicking"

patterns-established:
  - "readBody(w http.ResponseWriter, r *http.Request) signature pattern: always thread ResponseWriter through body-limiting helpers"

requirements-completed: [QUAL-05]

# Metrics
duration: 2min
completed: 2026-03-02
---

# Phase 1 Plan 01: Fix readBody nil ResponseWriter Panic Summary

**Fixed nil ResponseWriter panic in readBody by passing http.ResponseWriter to http.MaxBytesReader, with regression tests proving no panic on >64 MiB payloads**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-02T20:42:21Z
- **Completed:** 2026-03-02T20:43:28Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Fixed the only known panic in the codebase: `http.MaxBytesReader(nil, ...)` replaced with `http.MaxBytesReader(w, ...)`
- Updated `readBody` function signature to accept `http.ResponseWriter` as first argument
- Updated `ServeHTTP` call site to pass `w` as first argument to `readBody`
- Added `TestReadBodyOversized`: proves 64 MiB + 1 body returns non-nil error without panic
- Added `TestReadBodyNormal`: proves 1 KiB body is read correctly with no error
- All tests pass with `-race` flag (no data races)

## Task Commits

Each task was committed atomically:

1. **Task 1: Fix readBody nil ResponseWriter** - `34b6d79` (fix)
2. **Task 2: Write readBody regression test** - `863e0f2` (test)

## Files Created/Modified
- `pkg/server/server.go` - Updated readBody signature and MaxBytesReader call; updated ServeHTTP call site
- `pkg/server/server_test.go` - New regression tests: TestReadBodyOversized and TestReadBodyNormal

## Decisions Made
- Passed http.ResponseWriter (not nil) to http.MaxBytesReader: this is the correct behavior as it allows the reader to close the connection properly when the body size limit is exceeded, avoiding a nil pointer dereference panic.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- The readBody panic is resolved; Phase 1 Plan 02 (build quality) can proceed without risk of this crash
- Server package now has test coverage; CI can catch regressions on body size limits

---
*Phase: 01-quality*
*Completed: 2026-03-02*
