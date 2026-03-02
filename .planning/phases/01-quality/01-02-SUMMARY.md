---
phase: 01-quality
plan: "02"
subsystem: testing
tags: [go, testing, table-driven, parser, mapper, cepa, windows-event]

# Dependency graph
requires:
  - phase: 01-quality
    provides: "parser.go and mapper.go core implementations already present"
provides:
  - "pkg/parser/parser_test.go: white-box table-driven tests for XML parsing"
  - "pkg/mapper/mapper_test.go: white-box table-driven tests for CEPA-to-Windows event mapping"
affects: [01-quality, 02-build, 03-docs]

# Tech tracking
tech-stack:
  added: []
  patterns: [table-driven tests with t.Run, white-box testing via same-package declaration]

key-files:
  created:
    - pkg/parser/parser_test.go
    - pkg/mapper/mapper_test.go
  modified: []

key-decisions:
  - "White-box tests (package parser / package mapper) to access internal types directly"
  - "stdlib only — no testify or external test libraries added to go.mod"
  - "Table-driven subtests with t.Run for readable failure messages"

patterns-established:
  - "Table-driven test pattern: define struct slice with name/input/want fields, iterate with t.Run"
  - "White-box package declaration: test file uses same package as implementation"

requirements-completed: [QUAL-01, QUAL-02]

# Metrics
duration: 2min
completed: 2026-03-02
---

# Phase 1 Plan 02: Unit Tests for Parser and Mapper Summary

**26 table-driven stdlib-only tests verifying CEPA XML parsing and CEPA-to-Windows EventID/AccessMask mapping across all 6 event categories**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-02T09:22:26Z
- **Completed:** 2026-03-02T09:24:06Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- TestParse with 6 subtests: single CEEEvent, VCAPS EventBatch (2 events), malformed XML, empty payload, wrong root element, and timestamp parsing
- TestIsRegisterRequest with 5 subtests covering RegisterRequest tag detection edge cases
- TestMapEventID with 10 subtests covering all 6 CEPA categories, directory variants, and unknown-type fallback to EventID 4663 with mask 0x0
- TestMapFieldPropagation verifying all 10 WindowsEvent fields are correctly propagated from CEPAEvent
- TestMapHostnameFallback verifying os.Hostname() fallback when empty hostname passed
- All 26 tests pass with -race detector, no data races

## Task Commits

Each task was committed atomically:

1. **Task 1: Write parser unit tests** - `e4707fc` (test)
2. **Task 2: Write mapper unit tests** - `e15c322` (test)

## Files Created/Modified
- `pkg/parser/parser_test.go` - White-box table-driven tests for Parse() and IsRegisterRequest()
- `pkg/mapper/mapper_test.go` - White-box table-driven tests for Map() covering EventID, AccessMask, field propagation, hostname fallback

## Decisions Made
- Used white-box (same-package) test declarations to access internal rawEvent types directly without exporting them
- stdlib only — `testing` and `time` imports only; no testify dependency added to go.mod
- Table-driven tests with t.Run for clear per-subtest failure messages

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- QUAL-01 and QUAL-02 are fully satisfied
- Parser and mapper packages have verified correctness guarantees
- Ready to proceed with QUAL-03 through QUAL-05 (handler tests, build pipeline, linting)

---
*Phase: 01-quality*
*Completed: 2026-03-02*
