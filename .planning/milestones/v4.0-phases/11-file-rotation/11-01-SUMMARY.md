---
phase: 11-file-rotation
plan: "01"
subsystem: go-evtx
tags: [rotation, evtx, tdd, go-evtx, v0.4.0]
dependency_graph:
  requires: [go-evtx v0.3.0]
  provides: [go-evtx v0.4.0 with full rotation support]
  affects: [cee-exporter pkg/evtx BinaryEvtxWriter]
tech_stack:
  added: [syscall.Fsync for dir sync, sort.Slice for archive cleanup]
  patterns: [nil-channel select idiom for optional tickers, private rotate() with caller-holds-lock contract]
key_files:
  created:
    - /Users/fjacquet/Projects/go-evtx/rotation_test.go
    - /Users/fjacquet/Projects/go-evtx/evtx_unix.go
    - /Users/fjacquet/Projects/go-evtx/evtx_windows.go
  modified:
    - /Users/fjacquet/Projects/go-evtx/evtx.go
    - /Users/fjacquet/Projects/go-evtx/CHANGELOG.md
decisions:
  - "rotate() requires caller holds w.mu; Rotate() acquires mu then calls rotate() — single mutex = no deadlock"
  - "archivePathFor uses hyphens for colons in timestamp (2006-01-02T15-04-05) for filesystem portability"
  - "cleanOldFiles glob pattern base-*.evtx never matches active base.evtx (hyphen separator)"
  - "evtx_unix.go not evtx_linux.go: explicit //go:build !windows tag, _unix has no special Go meaning"
  - "backgroundLoop nil-channel idiom: disabled tickers add zero overhead (receive on nil channel blocks forever)"
  - "currentSize initialized to evtxFileHeaderSize in New() — placeholder header already written"
metrics:
  duration: "~25 minutes"
  completed: "2026-03-05"
  tasks: 3
  files_created: 3
  files_modified: 2
---

# Phase 11 Plan 01: File Rotation Summary

**One-liner:** Size/time/count rotation via single rotate() method with Rotate() public API — go-evtx v0.4.0

## What Was Implemented

### New Fields in RotationConfig

```go
type RotationConfig struct {
    FlushIntervalSec  int // 0 = disabled; must be >= 0
    MaxFileSizeMB     int // 0 = disabled; rotate when file >= N MiB
    MaxFileCount      int // 0 = unlimited; keep only N newest archives
    RotationIntervalH int // 0 = disabled; rotate every N hours
}
```

### New Field in Writer

```go
currentSize int64 // initialized to evtxFileHeaderSize in New()
```

Tracked via `flushChunkLocked()`: `w.currentSize += int64(evtxChunkSize)` per committed chunk.

### New Methods

| Method | Signature | Notes |
|--------|-----------|-------|
| `rotate()` | `func (w *Writer) rotate() error` | Private; caller must hold w.mu |
| `Rotate()` | `func (w *Writer) Rotate() error` | Public; acquires w.mu, then calls rotate() |
| `archivePathFor()` | `func archivePathFor(activePath string) string` | UTC timestamp, hyphens for colons |
| `cleanOldFiles()` | `func (w *Writer) cleanOldFiles() error` | Private; caller must hold w.mu |

### rotate() Sequence (caller holds w.mu)

1. If `len(w.records) > 0`: call `flushChunkLocked()`
2. If `w.chunkCount == 0`: return early (nothing to archive)
3. `w.f.Sync()` + `w.f.Close()`
4. `os.Rename(w.path, archivePathFor(w.path))`
5. `syncDir(filepath.Dir(w.path))` — log warn on error, continue
6. Open fresh file, write `buildFileHeader(0, 1)`, assign to `w.f`
7. Reset: `chunkCount=0, recordID=1, firstID=1, records[:0], currentSize=evtxFileHeaderSize`
8. If `MaxFileCount > 0`: `cleanOldFiles()` — log warn on error

### Archive Naming

`base-2006-01-02T15-04-05.evtx` — UTC, hyphens replace colons for filesystem portability.

### New Platform Files

- **`evtx_unix.go`** (`//go:build !windows`): `syncDir()` using `syscall.Open` + `syscall.Fsync` + `syscall.Close`
- **`evtx_windows.go`** (`//go:build windows`): `syncDir()` no-op (NTFS rename durable without fsync)

### backgroundLoop Changes

Uses nil-channel idiom for optional rotation ticker:

```go
var rotC <-chan time.Time
if w.cfg.RotationIntervalH > 0 {
    rt := time.NewTicker(...)
    defer rt.Stop()
    rotC = rt.C
}
// In select: case <-rotC fires only when RotationIntervalH > 0
```

Goroutine start condition updated: `FlushIntervalSec > 0 || RotationIntervalH > 0`.

## Test Results

| Test | Status |
|------|--------|
| TestWriter_SizeRotation | GREEN |
| TestWriter_CountRetention | GREEN |
| TestWriter_TimeRotation | GREEN |
| TestWriter_ManualRotate | GREEN |
| TestWriter_RotatedFileValid | GREEN |
| TestWriter_RotateRace | GREEN |
| All Phase 9 tests (goroutine_test.go) | GREEN |
| All Phase 10 tests (openhandle_test.go) | GREEN |
| All existing tests (evtx_test.go, binformat_test.go, reader_test.go) | GREEN |
| go test -race ./... | PASS — zero data races |

## go-evtx v0.4.0

- Tag SHA: `f4596493bb0dfffcb3466e996086062429fcb27e`
- Pushed to: `https://github.com/fjacquet/go-evtx`
- Tag command: `git tag -a v0.4.0 -m "v0.4.0: file rotation (size/time/count) with Rotate() public API"`

## Commits (in go-evtx repo)

| Hash | Message |
|------|---------|
| c7f314e | test(11-01): add failing RED tests for rotation (ROT-01 through ROT-04) |
| 23dfada | chore(11-01): add syncDir platform files (evtx_unix.go, evtx_windows.go) |
| f459649 | feat(rotation): implement size/time/count rotation and Rotate() public API — v0.4.0 |

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check: PASSED

- [x] rotation_test.go exists at /Users/fjacquet/Projects/go-evtx/rotation_test.go
- [x] evtx_unix.go exists at /Users/fjacquet/Projects/go-evtx/evtx_unix.go
- [x] evtx_windows.go exists at /Users/fjacquet/Projects/go-evtx/evtx_windows.go
- [x] evtx.go modified with rotation methods
- [x] CHANGELOG.md has v0.4.0 entry above v0.3.0
- [x] v0.4.0 tag exists and pushed to GitHub
- [x] go test -race ./... passes with zero data races
- [x] All 6 rotation tests GREEN
- [x] All Phase 9 and Phase 10 tests still pass
