---
phase: 09-goroutine-scaffolding-and-fsync
plan: "02"
subsystem: evtx-writer
tags:
  - go-evtx
  - RotationConfig
  - FlushIntervalSec
  - ADR
  - config-wiring
dependency_graph:
  requires:
    - "09-01: go-evtx v0.2.0 published with RotationConfig and backgroundLoop"
  provides:
    - "FlushIntervalSec operator-configurable parameter wired through cee-exporter config"
    - "ADR-012 flush ticker ownership decision"
    - "ADR-013 write-on-close model documentation"
  affects:
    - pkg/evtx/writer_evtx_notwindows.go
    - pkg/evtx/writer_native_notwindows.go
    - pkg/evtx/writer_native_windows.go
    - cmd/cee-exporter/main.go
tech_stack:
  added:
    - "go-evtx v0.2.0 (replace directive → local path; published v0.2.0 missing fromFILETIME)"
  patterns:
    - "RotationConfig passthrough from TOML config to goevtx.New() at startup"
    - "Platform-symmetric factory signatures (both !windows and windows accept RotationConfig)"
key_files:
  created:
    - docs/adr/ADR-012-flush-ticker-ownership.md
    - docs/adr/ADR-013-write-on-close-model.md
  modified:
    - go.mod
    - go.sum
    - pkg/evtx/writer_evtx_notwindows.go
    - pkg/evtx/writer_evtx_notwindows_test.go
    - pkg/evtx/writer_native_notwindows.go
    - pkg/evtx/writer_native_windows.go
    - cmd/cee-exporter/main.go
decisions:
  - "Use replace directive for go-evtx: published v0.2.0 missing fromFILETIME in binformat.go; local path used until fixed upstream"
  - "Update writer_native_windows.go signature to match !windows for API symmetry (RotationConfig accepted but ignored — Win32 EventLog is synchronous)"
  - "Update test callers to pass goevtx.RotationConfig{} — goroutine disabled in tests (FlushIntervalSec=0)"
metrics:
  duration_seconds: 632
  completed_date: "2026-03-04"
  tasks_completed: 2
  tasks_total: 2
  files_changed: 9
requirements_satisfied:
  - FLUSH-01
  - ADR-01
  - ADR-02
---

# Phase 09 Plan 02: go-evtx v0.2.0 RotationConfig wiring and ADR documentation Summary

**One-liner:** Wire go-evtx RotationConfig through cee-exporter config stack (TOML -> OutputConfig -> buildWriter -> goevtx.New) and commit ADR-012/ADR-013 documenting flush ticker ownership and write-on-close semantics.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Update go.mod and adapter files for go-evtx v0.2.0, wire FlushIntervalSec through main.go | f9cb142 | go.mod, go.sum, writer_evtx_notwindows.go, writer_evtx_notwindows_test.go, writer_native_notwindows.go, writer_native_windows.go, main.go |
| 2 | Write ADR-012 (flush ticker ownership) and ADR-013 (write-on-close model) | 8d4b1b5 | docs/adr/ADR-012-flush-ticker-ownership.md, docs/adr/ADR-013-write-on-close-model.md |

## What Was Built

### Task 1: go-evtx v0.2.0 integration

- `NewBinaryEvtxWriter(evtxPath string, cfg goevtx.RotationConfig)` — new signature passes cfg to `goevtx.New()`
- `NewNativeEvtxWriter(evtxPath string, cfg goevtx.RotationConfig)` — updated on both `!windows` (delegates to BinaryEvtxWriter) and `windows` (accepts cfg for API symmetry, ignores it — Win32 EventLog is synchronous)
- `OutputConfig.FlushIntervalSec int` with `toml:"flush_interval_s"` added
- `defaultConfig()` sets `FlushIntervalSec: 15`
- `buildWriter()` "evtx" case now calls `evtx.NewNativeEvtxWriter(cfg.EVTXPath, goevtx.RotationConfig{FlushIntervalSec: cfg.FlushIntervalSec})`
- All test callers updated to `NewBinaryEvtxWriter(path, goevtx.RotationConfig{})` (goroutine disabled in tests)

### Task 2: ADR documentation

- **ADR-012**: Argues flush ticker belongs in go-evtx Writer layer. Core argument: adding `Flush()` to `evtx.Writer` interface would force meaningless stubs on GELF, Win32, Syslog, Beats. Writer interface remains at 2 methods.
- **ADR-013**: Documents current checkpoint-write model honestly — bounded loss window, parseable checkpoints, graceful shutdown completeness. Documents what is NOT guaranteed: no `f.Sync()` (impossible with `os.WriteFile`), no multi-chunk support. Documents Phase 10 planned supersession with open-handle model.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking issue] Published go-evtx v0.2.0 missing `fromFILETIME` in binformat.go**
- **Found during:** Task 1 — `make build` failed with `undefined: fromFILETIME` in go-evtx v0.2.0
- **Issue:** The published GOPROXY-indexed v0.2.0 has `binformat.go` without `fromFILETIME`, but `reader.go` and `binxml_reader.go` reference it. The local repo has `fromFILETIME` in `binformat.go`. The published package is broken.
- **Fix:** Applied `go mod edit -replace github.com/fjacquet/go-evtx=../go-evtx` + `go mod tidy` to use local path which has the complete `binformat.go`.
- **Files modified:** go.mod, go.sum
- **Commit:** f9cb142

**2. [Rule 1 - Bug] Test callers using old `NewBinaryEvtxWriter(path)` single-arg signature**
- **Found during:** Task 1 — `make test` failed with "not enough arguments in call to NewBinaryEvtxWriter"
- **Issue:** `writer_evtx_notwindows_test.go` had 6 call sites using the old single-arg signature
- **Fix:** Added `goevtx "github.com/fjacquet/go-evtx"` import, replaced all calls with `NewBinaryEvtxWriter(path, goevtx.RotationConfig{})` (goroutine disabled with FlushIntervalSec=0)
- **Files modified:** pkg/evtx/writer_evtx_notwindows_test.go
- **Commit:** f9cb142

**3. [Rule 2 - API symmetry] Windows factory needed matching RotationConfig signature**
- **Found during:** Task 1 — `writer_native_windows.go` had single-arg `NewNativeEvtxWriter(_ string)` which would have diverged from non-Windows signature
- **Fix:** Updated `writer_native_windows.go` to accept `(_ string, _ goevtx.RotationConfig)` — cfg accepted but ignored (Win32 EventLog is synchronous)
- **Files modified:** pkg/evtx/writer_native_windows.go
- **Commit:** f9cb142

## Verification Results

All 9 verification checks passed:
- `make test` — all packages green
- `make build` — binary compiles (CGO_ENABLED=0 GOOS=linux GOARCH=amd64)
- `FlushIntervalSec` present in OutputConfig with `toml:"flush_interval_s"` tag
- `FlushIntervalSec: 15` in defaultConfig()
- `RotationConfig` used in writer_evtx_notwindows.go
- ADR-012 and ADR-013 exist in docs/adr/
- ADR-012 contains "Writer interface" argument
- ADR-013 contains "f.Sync" limitation documentation

## Self-Check: PASSED

All key files found on disk. Both task commits verified in git log.

| Check | Result |
|-------|--------|
| writer_evtx_notwindows.go | FOUND |
| writer_native_notwindows.go | FOUND |
| writer_native_windows.go | FOUND |
| main.go | FOUND |
| ADR-012 | FOUND |
| ADR-013 | FOUND |
| Commit f9cb142 | FOUND |
| Commit 8d4b1b5 | FOUND |
