---
phase: 11-file-rotation
plan: 02
subsystem: evtx
tags: [go-evtx, evtx, rotation, sighup, platform-split, config]

# Dependency graph
requires:
  - phase: 11-01
    provides: go-evtx v0.4.0 with Rotate() method and full RotationConfig (MaxFileSizeMB, MaxFileCount, RotationIntervalH)
provides:
  - BinaryEvtxWriter.Rotate() error method delegating to go-evtx Writer
  - installSIGHUP() platform-split files (sighup_notwindows.go, sighup_windows.go)
  - OutputConfig rotation fields (MaxFileSizeMB, MaxFileCount, RotationIntervalH)
  - buildWriter() case "evtx" passes all four RotationConfig fields
  - installSIGHUP(w) called in run() after buildWriter() success
  - go.mod bumped to go-evtx v0.4.0
affects:
  - Phase 12 (config/observability wiring)
  - ops: SIGHUP-triggered rotation now available to operators

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "SIGHUP handler via type assertion: w.(interface{ Rotate() error }) — avoids polluting Writer interface"
    - "Platform-split files: sighup_notwindows.go + sighup_windows.go with correct build tags"
    - "Rotation config forwarded from TOML OutputConfig to go-evtx RotationConfig end-to-end"

key-files:
  created:
    - cmd/cee-exporter/sighup_notwindows.go
    - cmd/cee-exporter/sighup_windows.go
  modified:
    - pkg/evtx/writer_evtx_notwindows.go
    - cmd/cee-exporter/main.go
    - go.mod

key-decisions:
  - "SIGHUP type assertion: w.(interface{ Rotate() error }) — NOT added to Writer interface, preserves minimal interface design"
  - "sighup_notwindows.go + sighup_windows.go naming — follows project convention; _linux.go would be Linux-only"
  - "MaxFileSizeMB, MaxFileCount, RotationIntervalH default 0 in defaultConfig() — 0 = unlimited/disabled per requirements"
  - "installSIGHUP called after buildWriter() success, before queue construction in run()"

patterns-established:
  - "Platform-split by build tag for signal handling: no SIGHUP on Windows, functional SIGHUP goroutine on all other platforms"
  - "Type assertion pattern for optional writer capabilities without interface pollution"

requirements-completed: [ROT-01, ROT-02, ROT-03, ROT-04]

# Metrics
duration: 2min
completed: 2026-03-05
---

# Phase 11 Plan 02: File Rotation Wiring Summary

**SIGHUP-triggered .evtx rotation wired end-to-end: BinaryEvtxWriter.Rotate() delegates to go-evtx v0.4.0, platform-split installSIGHUP() uses type assertion, and three rotation config fields (MaxFileSizeMB, MaxFileCount, RotationIntervalH) flow from TOML through OutputConfig to goevtx.RotationConfig**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-05T07:23:06Z
- **Completed:** 2026-03-05T07:25:07Z
- **Tasks:** 2
- **Files modified:** 5

## Accomplishments
- BinaryEvtxWriter.Rotate() error method added, delegating to b.w.Rotate() in go-evtx
- Platform-split SIGHUP handler: sighup_notwindows.go registers SIGHUP signal + goroutine with type assertion; sighup_windows.go is a no-op stub
- OutputConfig extended with MaxFileSizeMB, MaxFileCount, RotationIntervalH int fields (all default 0 = unlimited/disabled)
- buildWriter() case "evtx" now passes all four RotationConfig fields to go-evtx
- installSIGHUP(w) called in run() after buildWriter() success, before queue construction
- go.mod bumped from v0.3.0 to v0.4.0 (replace directive to ../go-evtx preserved)
- All 9 packages pass make test; CGO_ENABLED=0 Linux and Windows builds succeed

## Task Commits

Each task was committed atomically:

1. **Task 1: Add BinaryEvtxWriter.Rotate() and create SIGHUP platform files** - `be78c36` (feat)
2. **Task 2: Extend OutputConfig + buildWriter() + call installSIGHUP + bump go.mod to v0.4.0** - `18d3814` (feat)

**Plan metadata:** (docs commit follows)

## Files Created/Modified
- `pkg/evtx/writer_evtx_notwindows.go` - Added Rotate() error method delegating to b.w.Rotate()
- `cmd/cee-exporter/sighup_notwindows.go` - New: installSIGHUP for !windows, SIGHUP goroutine + type assertion
- `cmd/cee-exporter/sighup_windows.go` - New: no-op installSIGHUP stub for Windows
- `cmd/cee-exporter/main.go` - Extended OutputConfig, updated buildWriter() case "evtx", added installSIGHUP call in run()
- `go.mod` - go-evtx bumped from v0.3.0 to v0.4.0

## Decisions Made
- SIGHUP uses type assertion `w.(interface{ Rotate() error })` instead of adding Rotate() to Writer interface — keeps the interface minimal; non-evtx writers silently skip
- Platform files named sighup_notwindows.go and sighup_windows.go per project convention (never _linux.go)
- Three new OutputConfig fields default to 0 in defaultConfig() — semantics: 0 = unlimited/disabled (matches go-evtx RotationConfig)
- installSIGHUP called between buildWriter() and queue construction so the writer is fully initialized before the SIGHUP goroutine can call Rotate()

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

Operators can now send SIGHUP to the cee-exporter process to trigger immediate .evtx rotation:

```bash
kill -HUP $(pgrep cee-exporter)
```

Or configure rotation via config.toml (all fields apply only when `type = "evtx"`):

```toml
[output]
type              = "evtx"
evtx_path         = "/var/log/audit/security.evtx"
flush_interval_s  = 15      # periodic checkpoint write
max_file_size_mb  = 100     # rotate when file >= 100 MiB
max_file_count    = 30      # keep 30 archive files
rotation_interval_h = 24   # rotate every 24 hours
```

## Next Phase Readiness

- Phase 11 complete: all four ROT-* requirements fulfilled
- Phase 12 (config/observability) can now reference rotation fields as established config API
- No blockers

---
*Phase: 11-file-rotation*
*Completed: 2026-03-05*
