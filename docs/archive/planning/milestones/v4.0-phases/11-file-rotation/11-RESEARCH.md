# Phase 11: File Rotation - Research

**Researched:** 2026-03-05
**Domain:** Go file rotation, EVTX binary format, Unix signal handling (SIGHUP), go-evtx open-handle model
**Confidence:** HIGH

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| ROT-01 | Operator can set `max_file_size_mb` so the active `.evtx` file is rotated when it reaches that size (0 = unlimited; rotation produces a timestamped archive file) | `rotate()` method design specified; size tracking via `currentSize` field; pre-write size check pattern documented |
| ROT-02 | Operator can set `max_file_count` so only the N most recent archive files are kept and older ones are deleted automatically (0 = unlimited) | `cleanOldFiles()` pattern via `filepath.Glob` + `os.Stat` sort + `os.Remove` documented; archive naming pattern specified |
| ROT-03 | Operator can set `rotation_interval_h` so the active `.evtx` file is rotated on a fixed schedule regardless of size (0 = disabled) | Ticker integration with existing `backgroundLoop` goroutine documented; two-ticker select pattern specified |
| ROT-04 | Operator can send SIGHUP to the process to trigger an immediate `.evtx` file rotation without restarting the daemon | SIGHUP channel wiring from `main.go` → `BinaryEvtxWriter.Rotate()` documented; stdlib `os/signal` pattern confirmed |
</phase_requirements>

---

## Summary

Phase 10 delivered a go-evtx `Writer` that holds `*os.File` open from `New()` through `Close()`, writes chunks incrementally with `f.WriteAt()`, and runs a background goroutine for periodic flush. The `RotationConfig` struct currently has only `FlushIntervalSec`. Phase 11 adds four new fields to `RotationConfig` — `MaxFileSizeMB`, `MaxFileCount`, `RotationIntervalH`, and SIGHUP support — and implements the `rotate()` method that is called by all three automatic triggers and by an explicit `Rotate()` public method.

The core design insight is that `rotate()` is a single function called from three paths: the size check in `WriteRecord`/`WriteRaw`, the time ticker in `backgroundLoop`, and the explicit `Rotate()` call triggered by SIGHUP. All three paths hold `w.mu`, so rotation is always synchronous and race-free. The rotation sequence is: finalize current chunk to disk (flush + CRC + header patch) → `f.Sync()` → `f.Close()` → `os.Rename(activePath, archivePath)` → directory fsync → `os.OpenFile` new file → write placeholder header → optionally call `cleanOldFiles()`.

SIGHUP wiring lives in `cee-exporter` `main.go`, not in go-evtx. go-evtx is a library with no awareness of OS signals. The adapter `BinaryEvtxWriter` in `pkg/evtx/writer_evtx_notwindows.go` exposes a `Rotate() error` method that calls `w.w.Rotate()`. `main.go` adds `signal.Notify(sighupCh, syscall.SIGHUP)` and calls `binaryEvtxWriter.Rotate()` in the signal handler goroutine.

**Primary recommendation:** Implement `rotate()` in go-evtx `evtx.go`, extend `RotationConfig` with new fields, add size check in `WriteRecord`/`WriteRaw`, add rotation ticker to `backgroundLoop`, expose `Rotate() error` as a public method, then wire SIGHUP in cee-exporter `main.go`. Publish go-evtx v0.4.0.

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `os.Rename` | Go stdlib | Atomic file rename (active → archive) | POSIX-atomic on Linux/macOS; Windows `MoveFileEx` under the covers in Go |
| `os.File.Sync` | Go stdlib | `fsync(2)` / `F_FULLFSYNC` before rename | Must precede rename to guarantee durability on crash |
| `filepath.Glob` | Go stdlib | Enumerate archive files matching `base-*.evtx` | Standard glob; used by lumberjack behavioral model |
| `os.Stat` + sort | Go stdlib | Sort archive files by `ModTime()` for count-based pruning | Reliable; mtime-based ordering matches user expectation |
| `os.Remove` | Go stdlib | Delete oldest archive files | Direct, no intermediate steps |
| `syscall.Open` + `syscall.Fsync` | Go stdlib `syscall` | Directory fsync after rename (crash-safe on Linux) | Required for crash-safe rename on Linux ext4/XFS; acceptable overhead for rare rotation events |
| `os/signal.Notify` | Go stdlib | Register SIGHUP channel in `main.go` | Standard Go signal handling; non-blocking with buffered channel |
| `syscall.SIGHUP` | Go stdlib `syscall` | Signal constant | Available on all Unix platforms; `//go:build !windows` for SIGHUP handler |
| `time.NewTicker` | Go stdlib | Rotation interval ticker in `backgroundLoop` | Already used for flush ticker; add second ticker or combine into one goroutine |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `time.Format` | Go stdlib | Archive filename timestamp: `2006-01-02T15-04-05` | Called once per rotation at rename time |
| `path/filepath.Dir` / `filepath.Base` / `filepath.Ext` | Go stdlib | Parse active path into directory + stem for glob | Already imported in `evtx.go` |
| `sort.Slice` | Go stdlib | Sort archive list by `os.FileInfo.ModTime()` | Standard; no external sort library needed |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `os.Rename` | Copy + delete | Rename is atomic on same filesystem; copy+delete is not; never use copy+delete for rotation |
| Native `rotate()` in go-evtx | lumberjack library | lumberjack uses `io.Writer`; incompatible with EVTX binary format that requires controlled chunk/CRC writes; do not use |
| Two tickers (flush + rotation) | Single combined ticker | Two independent tickers in one `select` is simpler and more flexible; rotation interval is typically in hours, flush in seconds; keeping them separate avoids alignment issues |
| SIGHUP in `backgroundLoop` goroutine of go-evtx | SIGHUP in `main.go` | go-evtx is a library; OS signal handling belongs in the application; SIGHUP wired in `main.go`, calls `Rotate()` on the public API |

**Installation:** No new packages. All changes use Go stdlib. go-evtx zero-dependency constraint maintained.

---

## Architecture Patterns

### Where Does Rotation Logic Live?

**Decision: rotation logic lives in go-evtx (`evtx.go`), SIGHUP wiring lives in cee-exporter (`main.go`).**

Rationale:
- The `rotate()` implementation requires direct access to `w.f`, `w.chunkCount`, `w.records`, and `w.path` — all private fields of `Writer`. It cannot be implemented outside the package.
- SIGHUP is an OS-level signal. go-evtx is a library and must not import `os/signal` or `syscall.SIGHUP`. Signal handling belongs in the application (`main.go`).
- `BinaryEvtxWriter` adapter in `pkg/evtx/writer_evtx_notwindows.go` exposes `Rotate() error` which calls `w.w.Rotate()`, making it accessible from `main.go`.

### RotationConfig Extensions

```go
// Source: evtx.go current RotationConfig + Phase 11 additions
type RotationConfig struct {
    FlushIntervalSec  int // 0 = disabled; seconds between periodic flush ticks
    MaxFileSizeMB     int // 0 = unlimited; rotate when file reaches this size
    MaxFileCount      int // 0 = unlimited; keep only N most recent archive files
    RotationIntervalH int // 0 = disabled; rotate every N hours regardless of size
}
```

All four fields are zero-valued by default, preserving backward compatibility.

### Writer Struct Addition

```go
// Source: evtx.go Writer struct — Phase 11 additions
type Writer struct {
    // ... existing fields from Phase 10 ...
    currentSize int64 // bytes written to current file (header + all chunk data)
}
```

`currentSize` is initialized to `evtxFileHeaderSize` (4096) when the file is opened in `New()` and updated after each chunk write in `flushChunkLocked()` and `tickFlushLocked()`. The size check uses `currentSize` before every `WriteRecord`/`WriteRaw` call.

### Recommended Project Structure

Changes are split across both repos:

```
go-evtx/
├── evtx.go           # RotationConfig: add MaxFileSizeMB, MaxFileCount, RotationIntervalH
│                     # Writer: add currentSize int64
│                     # New(): initialize currentSize = evtxFileHeaderSize
│                     # WriteRecord/WriteRaw: size check -> rotate() before flush
│                     # flushChunkLocked(): update currentSize after WriteAt
│                     # backgroundLoop(): add rotation ticker case
│                     # rotate(): new private method
│                     # cleanOldFiles(): new private method
│                     # Rotate(): new public method (calls rotate() under mu)
├── rotation_test.go  # TestWriter_SizeRotation, TestWriter_CountRetention,
│                     # TestWriter_TimeRotation, TestWriter_ManualRotate,
│                     # TestWriter_RotatedFileValid
└── CHANGELOG.md      # v0.4.0 entry

cee-exporter/
├── cmd/cee-exporter/main.go    # extend OutputConfig: MaxFileSizeMB, MaxFileCount,
│                               # RotationIntervalH; pass to RotationConfig;
│                               # add SIGHUP handler goroutine
├── pkg/evtx/writer_evtx_notwindows.go  # expose Rotate() error method
└── go.mod                      # bump go-evtx to v0.4.0
```

### Pattern 1: `rotate()` — The Core Method

**What:** Finalizes the current file, renames it to a timestamped archive, opens a fresh file.
**When to use:** Called from `WriteRecord`/`WriteRaw` (size trigger), `backgroundLoop` (time trigger), and `Rotate()` public method (SIGHUP trigger). Always called with `w.mu` held.

```go
// Source: direct analysis of go-evtx Writer state + EVTX format requirements
// rotate() MUST be called with w.mu held.
func (w *Writer) rotate() error {
    // 1. Flush any in-progress chunk to complete the current file cleanly.
    if len(w.records) > 0 {
        if err := w.flushChunkLocked(); err != nil {
            return fmt.Errorf("go_evtx: rotate flush: %w", err)
        }
    }

    // 2. Final fsync before rename — guarantees all data reaches disk.
    if err := w.f.Sync(); err != nil {
        return fmt.Errorf("go_evtx: rotate sync: %w", err)
    }

    // 3. Close the current file handle.
    if err := w.f.Close(); err != nil {
        return fmt.Errorf("go_evtx: rotate close: %w", err)
    }
    w.f = nil

    // 4. Rename active file to timestamped archive.
    archivePath := archivePathFor(w.path)
    if err := os.Rename(w.path, archivePath); err != nil {
        return fmt.Errorf("go_evtx: rotate rename: %w", err)
    }

    // 5. Directory fsync — required for crash-safe rename on Linux.
    if err := syncDir(filepath.Dir(w.path)); err != nil {
        // Non-fatal: log but continue. Directory fsync failure is OS-level.
        slog.Warn("go_evtx_rotate_dirsync_failed", "path", w.path, "error", err)
    }

    // 6. Open fresh file, write placeholder header.
    f, err := os.OpenFile(w.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
    if err != nil {
        return fmt.Errorf("go_evtx: rotate open new file: %w", err)
    }
    if _, err := f.Write(buildFileHeader(0, 1)); err != nil {
        f.Close()
        return fmt.Errorf("go_evtx: rotate write header: %w", err)
    }
    w.f = f

    // 7. Reset writer state for new file.
    w.chunkCount = 0
    w.recordID = 1
    w.firstID = 1
    w.records = w.records[:0]
    w.currentSize = evtxFileHeaderSize

    // 8. Count-based retention: delete oldest archives if MaxFileCount set.
    if w.cfg.MaxFileCount > 0 {
        if err := w.cleanOldFiles(); err != nil {
            slog.Warn("go_evtx_rotate_cleanup_failed", "path", w.path, "error", err)
        }
    }

    slog.Info("go_evtx_rotated", "archive", archivePath, "active", w.path)
    return nil
}
```

**Note on recordID reset:** After rotation, the new file starts with `recordID = 1`. This is correct — each archive is an independent EVTX file with its own record sequence. Windows Event Viewer and python-evtx treat each file independently.

### Pattern 2: Archive Path Naming

```go
// Source: behavioral model from lumberjack + time.Format docs
// archivePathFor produces a timestamped archive path from the active path.
// Example: "/var/log/audit.evtx" → "/var/log/audit-2026-03-05T14-30-00.evtx"
func archivePathFor(activePath string) string {
    ext := filepath.Ext(activePath)
    base := activePath[:len(activePath)-len(ext)]
    ts := time.Now().UTC().Format("2006-01-02T15-04-05")
    return fmt.Sprintf("%s-%s%s", base, ts, ext)
}
```

**Timestamp format:** `2006-01-02T15-04-05` (Go reference time; colons replaced with hyphens for filesystem compatibility on all platforms). UTC ensures consistent ordering across timezone changes. The format is lexicographically sortable, so `filepath.Glob` + `sort.Slice` by name works as well as mtime sorting.

### Pattern 3: `cleanOldFiles()` — Count-Based Retention

```go
// Source: filepath.Glob + os.Stat pattern; behavioral model from lumberjack
// cleanOldFiles removes archive files beyond MaxFileCount.
// MUST be called with w.mu held (reads w.cfg, w.path).
func (w *Writer) cleanOldFiles() error {
    ext := filepath.Ext(w.path)
    base := w.path[:len(w.path)-len(ext)]
    // Glob matches: base-2006-01-02T15-04-05.evtx
    pattern := base + "-*" + ext
    matches, err := filepath.Glob(pattern)
    if err != nil {
        return fmt.Errorf("go_evtx: cleanOldFiles glob: %w", err)
    }
    if len(matches) <= w.cfg.MaxFileCount {
        return nil
    }

    // Sort by modification time, oldest first.
    type entry struct {
        path  string
        mtime time.Time
    }
    entries := make([]entry, 0, len(matches))
    for _, m := range matches {
        info, err := os.Stat(m)
        if err != nil {
            continue // skip unreadable files
        }
        entries = append(entries, entry{m, info.ModTime()})
    }
    sort.Slice(entries, func(i, j int) bool {
        return entries[i].mtime.Before(entries[j].mtime)
    })

    // Delete oldest until count is within limit.
    toDelete := len(entries) - w.cfg.MaxFileCount
    for i := 0; i < toDelete; i++ {
        if err := os.Remove(entries[i].path); err != nil {
            slog.Warn("go_evtx_cleanup_remove_failed", "path", entries[i].path, "error", err)
        }
    }
    return nil
}
```

### Pattern 4: Size Check in `WriteRecord`/`WriteRaw`

```go
// Source: direct extension of existing WriteRecord pre-flush pattern
// Size check added BEFORE the chunk-capacity check.
func (w *Writer) WriteRecord(eventID int, fields map[string]string) error {
    w.mu.Lock()
    defer w.mu.Unlock()

    // Pre-rotation size check (if MaxFileSizeMB > 0).
    if w.cfg.MaxFileSizeMB > 0 {
        maxBytes := int64(w.cfg.MaxFileSizeMB) * 1024 * 1024
        if w.currentSize >= maxBytes {
            if err := w.rotate(); err != nil {
                return fmt.Errorf("go_evtx: size-rotate: %w", err)
            }
        }
    }

    // ... existing binXMLChunkOffset + buildBinXML + wrapEventRecord logic ...
    // ... existing chunk-capacity check + flushChunkLocked if needed ...
    // ... append to w.records, increment w.recordID ...
    return nil
}
```

**Note:** The size check fires on the SIZE of the current file (`currentSize`), not on the size of the record being written. This means rotation fires when the file is already at or above the threshold. The next record after rotation goes into the new file. This matches lumberjack's behavioral model.

`currentSize` is updated in `flushChunkLocked()` after each `f.WriteAt`:

```go
// Inside flushChunkLocked(), after the f.WriteAt(chunkBytes, chunkOffset) call:
w.currentSize += int64(len(chunkBytes)) // evtxChunkSize = 65536
```

And in `tickFlushLocked()` similarly (tick writes do not advance currentSize for the committed portion; only `flushChunkLocked` advances it when a full chunk is committed).

### Pattern 5: Rotation Ticker in `backgroundLoop`

```go
// Source: existing backgroundLoop pattern (Phase 9/10) + rotation ticker extension
func (w *Writer) backgroundLoop() {
    defer w.wg.Done()

    flushTicker := time.NewTicker(time.Duration(w.cfg.FlushIntervalSec) * time.Second)
    defer flushTicker.Stop()

    var rotTicker *time.Ticker
    var rotC <-chan time.Time
    if w.cfg.RotationIntervalH > 0 {
        rotTicker = time.NewTicker(time.Duration(w.cfg.RotationIntervalH) * time.Hour)
        defer rotTicker.Stop()
        rotC = rotTicker.C
    }

    for {
        select {
        case <-flushTicker.C:
            w.mu.Lock()
            if len(w.records) > 0 {
                _ = w.tickFlushLocked()
            }
            w.mu.Unlock()

        case <-rotC:
            w.mu.Lock()
            _ = w.rotate()
            w.mu.Unlock()

        case <-w.done:
            return
        }
    }
}
```

**Note on nil channel:** When `RotationIntervalH == 0`, `rotC` is `nil`. A receive on a nil channel blocks forever, which means the `case <-rotC:` branch is never selected. This is standard Go idiom for conditional ticker cases. No special handling needed.

**Note on FlushIntervalSec:** The existing `backgroundLoop` only creates the flush ticker when `FlushIntervalSec > 0`. Phase 11 changes: if `RotationIntervalH > 0` but `FlushIntervalSec == 0`, the goroutine must still start. Condition for goroutine start becomes: `FlushIntervalSec > 0 || RotationIntervalH > 0`.

### Pattern 6: Public `Rotate()` Method (SIGHUP trigger)

```go
// Source: pattern for exposing internal method as public API
// Rotate() is safe for concurrent use. It acquires w.mu, calls rotate(), and returns.
// This method is called by the cee-exporter SIGHUP handler.
func (w *Writer) Rotate() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    return w.rotate()
}
```

### Pattern 7: SIGHUP Wiring in `main.go`

```go
// Source: os/signal stdlib docs; //go:build !windows constraint for SIGHUP
// Located in: cmd/cee-exporter/main.go (or a platform-specific file)
// SIGHUP is only meaningful on Unix; Windows ignores it gracefully via build tag.

// In run():
sighupCh := make(chan os.Signal, 1)
signal.Notify(sighupCh, syscall.SIGHUP)

go func() {
    for range sighupCh {
        slog.Info("sighup_received_rotating_evtx")
        if rotator, ok := w.(interface{ Rotate() error }); ok {
            if err := rotator.Rotate(); err != nil {
                slog.Error("sighup_rotate_failed", "error", err)
            } else {
                slog.Info("sighup_rotate_complete")
            }
        }
    }
}()
```

**Interface approach:** `buildWriter` returns `evtx.Writer` which does not include `Rotate()`. Rather than adding `Rotate()` to the `Writer` interface (which would require all 5+ writer types to implement it), use a type assertion `w.(interface{ Rotate() error })`. Only `BinaryEvtxWriter` satisfies it; others are silently skipped.

**Platform constraint:** `syscall.SIGHUP` is not defined on Windows. Two approaches:
1. `//go:build !windows` file for the SIGHUP goroutine (recommended — clean separation)
2. Build-tag the signal registration block within `main.go`

Use approach 1: create `cmd/cee-exporter/sighup_notwindows.go` with `//go:build !windows` and `func installSIGHUP(w evtx.Writer)`, and `cmd/cee-exporter/sighup_windows.go` with a no-op stub.

### Pattern 8: Directory fsync After Rename

```go
// Source: POSIX crash-safety requirement; Go syscall package
// syncDir performs an fsync on the directory containing the renamed file.
// Required on Linux ext4/XFS to guarantee the rename reaches stable storage.
// No-op on Windows (where rename semantics differ).
func syncDir(dirPath string) error {
    fd, err := syscall.Open(dirPath, syscall.O_RDONLY, 0)
    if err != nil {
        return err
    }
    defer syscall.Close(fd)
    return syscall.Fsync(fd)
}
```

Place this in `evtx_unix.go` with `//go:build !windows` and a no-op `evtx_windows.go` stub. The `syscall` package is already part of Go stdlib; no new imports.

### Anti-Patterns to Avoid

- **Rotating without finalizing the EVTX chunk:** The active chunk may have partial records in `w.records`. Always call `flushChunkLocked()` inside `rotate()` before renaming. Skipping this produces a corrupt archive that python-evtx cannot parse.
- **Using `os.WriteFile` or copy+delete for rotation:** `os.Rename` is atomic on the same filesystem. Copy+delete has a window where neither file is complete.
- **Holding `w.mu` during the rename:** `os.Rename` is fast (< 1ms) but blocking under the mutex is acceptable since rotation is rare. No events are dropped — `WriteRecord` callers wait at the mutex.
- **Importing `os/signal` or `syscall.SIGHUP` in go-evtx:** go-evtx is a library. SIGHUP handling belongs in the application layer.
- **Deleting the active (non-timestamped) file in `cleanOldFiles()`:** The glob pattern `base-*` (with hyphen) excludes the active file `base.evtx`. Verify the pattern does not accidentally match the active file.
- **Starting the goroutine unconditionally:** `backgroundLoop` should only start when at least one time-based feature is enabled (`FlushIntervalSec > 0 || RotationIntervalH > 0`).

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| File rotation by size/time/count | Custom rolling-file library | Native `rotate()` in go-evtx using stdlib | lumberjack is incompatible (io.Writer contract); EVTX requires controlled binary writes |
| Archive filename with timestamp | Custom time formatting | `time.Now().UTC().Format("2006-01-02T15-04-05")` | Standard Go time format; lexicographically sortable |
| Oldest-file detection | Manual inode comparison | `os.Stat().ModTime()` + `sort.Slice` | Reliable; matches user mental model |
| SIGHUP handling | Custom signal loop | `signal.Notify(ch, syscall.SIGHUP)` + goroutine | Standard Go pattern; buffered channel prevents signal drop |
| EVTX finalization before rename | Skip or defer | Always call `flushChunkLocked()` + `f.Sync()` in `rotate()` | Without this, the archive has an incomplete final chunk and fails python-evtx validation |
| Directory fsync | Skip | `syscall.Open(dir) + syscall.Fsync` | Required on Linux for crash-safe rename; negligible cost |

**Key insight:** Rotation is a binary-format concern because EVTX chunks must be finalized (CRC patched, header updated) before the file can be safely renamed. lumberjack and other generic log rotators cannot satisfy this requirement.

---

## Common Pitfalls

### Pitfall 1: Rotating Without Finalizing the Last Chunk

**What goes wrong:** `rotate()` renames the active file while `w.records` still contains a partial in-progress chunk. The archive has a gap: the file header's `NextRecordIdentifier` and `ChunkCount` do not match the actual records on disk.

**Why it happens:** Developer calls `os.Rename` directly without first calling `flushChunkLocked()`.

**How to avoid:** `rotate()` always calls `flushChunkLocked()` as its first action if `len(w.records) > 0`. This converts the partial chunk to a complete, CRC-correct, padded 65,536-byte chunk before rename.

**Warning signs:** python-evtx reports fewer records than expected in the archive; `go-evtx Reader` stops short.

### Pitfall 2: `cleanOldFiles()` Glob Accidentally Matches the Active File

**What goes wrong:** The glob pattern `"/var/log/audit-*.evtx"` matches files like `/var/log/audit-2026-01-01T00-00-00.evtx` (correct) but also potentially `/var/log/audit-latest.evtx` if someone used a non-timestamp name.

**Why it happens:** Overly broad glob pattern. But also: if the active file is `/var/log/audit.evtx`, the pattern `audit-*.evtx` will NOT match `audit.evtx` (no hyphen + timestamp), so the active file is safe.

**How to avoid:** Archive naming MUST use a hyphen separator between base name and timestamp, and the active file name MUST NOT contain a hyphen before `.evtx`. Verify with a test: after rotation, assert active file still exists.

**Warning signs:** Active file disappears after `cleanOldFiles()` runs.

### Pitfall 3: Race Between SIGHUP Handler and Active Write

**What goes wrong:** SIGHUP goroutine calls `w.Rotate()` while a concurrent `WriteRecord` call is in progress.

**Why it happens:** `Rotate()` acquires `w.mu` (correct), but if the caller uses the wrong locking order, deadlock can occur.

**How to avoid:** `Rotate()` acquires `w.mu` itself. `WriteRecord` acquires `w.mu` at entry. Both use the same mutex — one will block waiting for the other. This is correct and safe. No deadlock: there is only one mutex, no lock ordering issue.

**Warning signs:** None in well-implemented code. The race detector will catch any actual races.

### Pitfall 4: goroutine for SIGHUP not Starting When FlushIntervalSec == 0

**What goes wrong:** `backgroundLoop` is only started when `FlushIntervalSec > 0`. When SIGHUP arrives, there is no goroutine to call `rotate()` — but `Rotate()` is a direct method call from `main.go`'s signal goroutine, not from `backgroundLoop`. SIGHUP path is independent of `backgroundLoop`.

**How to avoid:** SIGHUP wiring is in `main.go` as a separate goroutine. It calls `w.Rotate()` directly (which acquires `w.mu`). The `backgroundLoop` goroutine for time-based rotation starts if `RotationIntervalH > 0 || FlushIntervalSec > 0`.

**Warning signs:** SIGHUP signal appears received but rotation does not happen.

### Pitfall 5: `currentSize` Not Initialized in `New()`

**What goes wrong:** `currentSize` starts at 0. After writing the 4,096-byte placeholder header in `New()`, the actual file size is 4,096 bytes but `currentSize` is 0. If `MaxFileSizeMB` is very small, the first event never triggers rotation because `0 >= threshold` is false.

**How to avoid:** Initialize `currentSize = evtxFileHeaderSize` (4096) immediately after writing the placeholder header in `New()`. Reset to `evtxFileHeaderSize` (not 0) in `rotate()` after writing the new placeholder header.

**Warning signs:** Size-based rotation fires one chunk late (after one full chunk is written instead of when header + first chunk crosses the threshold).

### Pitfall 6: Windows `os.Rename` Fails if Destination Exists

**What goes wrong:** On Windows, `os.Rename` fails with `ERROR_ALREADY_EXISTS` if a file with the archive name already exists. On Linux/macOS, `os.Rename` atomically replaces the destination.

**Why it happens:** Two rotations happen within the same second, producing the same timestamp in the archive name. Unlikely but possible.

**How to avoid:** Include seconds in the timestamp format (`2006-01-02T15-04-05`). If collision still occurs (two rotations in the same second), append a counter suffix. But in practice, size-based rotation threshold means rotations are seconds apart at minimum.

**Warning signs:** Rotation fails on Windows with a rename error.

### Pitfall 7: Directory fsync on macOS vs Linux

**What goes wrong:** `syscall.Open` + `syscall.Fsync` on a directory works on Linux but behaves differently on macOS (`F_FULLFSYNC` is the preferred call on APFS/HFS+).

**Why it happens:** Platform differences in directory fsync semantics.

**How to avoid:** The `//go:build !windows` stub already handles Windows. For macOS, `syscall.Fsync` on a directory fd is accepted and returns without error (it may be a no-op for the directory metadata but this is acceptable). The correctness risk on macOS is lower than on Linux ext4 (macOS APFS provides stronger crash-safe rename guarantees). Mark directory fsync as best-effort: log warning on error but do not fail the rotation.

---

## Code Examples

Verified patterns from direct source inspection and stdlib documentation:

### Archive Path Construction

```go
// Source: time.Format docs + filepath package
// Active: /var/log/audit.evtx
// Archive: /var/log/audit-2026-03-05T14-30-45.evtx
func archivePathFor(activePath string) string {
    ext := filepath.Ext(activePath)                 // ".evtx"
    base := activePath[:len(activePath)-len(ext)]   // "/var/log/audit"
    ts := time.Now().UTC().Format("2006-01-02T15-04-05")
    return fmt.Sprintf("%s-%s%s", base, ts, ext)    // "/var/log/audit-2026-03-05T14-30-45.evtx"
}
```

### Glob Pattern for Archive Enumeration

```go
// Source: filepath.Glob stdlib docs
// Active: /var/log/audit.evtx
// Pattern: /var/log/audit-*.evtx
// Matches: /var/log/audit-2026-03-05T14-30-45.evtx (NOT audit.evtx)
ext := filepath.Ext(w.path)
base := w.path[:len(w.path)-len(ext)]
pattern := base + "-*" + ext
matches, _ := filepath.Glob(pattern)
```

### Sort Archives by ModTime

```go
// Source: sort.Slice + os.Stat stdlib docs
sort.Slice(entries, func(i, j int) bool {
    return entries[i].mtime.Before(entries[j].mtime)
})
// entries[0] is the oldest; delete from front until len(entries) == MaxFileCount
```

### RotationConfig Extension in go-evtx

```go
// Source: evtx.go current RotationConfig — Phase 11 extension
type RotationConfig struct {
    FlushIntervalSec  int // 0 = disabled; seconds between flush ticks
    MaxFileSizeMB     int // 0 = unlimited; rotate at this file size
    MaxFileCount      int // 0 = unlimited; keep only N archive files
    RotationIntervalH int // 0 = disabled; rotate every N hours
}
```

### RotationConfig Extension in cee-exporter OutputConfig

```go
// Source: main.go OutputConfig — Phase 11 additions
type OutputConfig struct {
    // ... existing fields ...
    FlushIntervalSec  int `toml:"flush_interval_s"`
    MaxFileSizeMB     int `toml:"max_file_size_mb"`  // NEW
    MaxFileCount      int `toml:"max_file_count"`     // NEW
    RotationIntervalH int `toml:"rotation_interval_h"` // NEW
}
```

Passed to `RotationConfig` in `buildWriter()` case `"evtx"`:

```go
case "evtx":
    w, err := evtx.NewNativeEvtxWriter(cfg.EVTXPath, goevtx.RotationConfig{
        FlushIntervalSec:  cfg.FlushIntervalSec,
        MaxFileSizeMB:     cfg.MaxFileSizeMB,
        MaxFileCount:      cfg.MaxFileCount,
        RotationIntervalH: cfg.RotationIntervalH,
    })
    return w, cfg.EVTXPath, err
```

### BinaryEvtxWriter Expose Rotate()

```go
// Source: pkg/evtx/writer_evtx_notwindows.go — Phase 11 addition
// Rotate() triggers an immediate rotation of the active .evtx file.
// Safe for concurrent use. Called by the SIGHUP handler in main.go.
func (b *BinaryEvtxWriter) Rotate() error {
    return b.w.Rotate()
}
```

### SIGHUP Platform Files

```go
// Source: cmd/cee-exporter/sighup_notwindows.go
//go:build !windows

package main

import (
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "github.com/fjacquet/cee-exporter/pkg/evtx"
)

func installSIGHUP(w evtx.Writer) {
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, syscall.SIGHUP)
    go func() {
        for range ch {
            slog.Info("sighup_received")
            if rotator, ok := w.(interface{ Rotate() error }); ok {
                if err := rotator.Rotate(); err != nil {
                    slog.Error("sighup_rotate_failed", "error", err)
                } else {
                    slog.Info("sighup_rotate_complete")
                }
            }
        }
    }()
}
```

```go
// Source: cmd/cee-exporter/sighup_windows.go
//go:build windows

package main

import "github.com/fjacquet/cee-exporter/pkg/evtx"

// installSIGHUP is a no-op on Windows (SIGHUP is not a Windows signal).
func installSIGHUP(_ evtx.Writer) {}
```

Called in `run()` after `buildWriter`:

```go
// After:  w, writerAddr, err := buildWriter(cfg.Output)
installSIGHUP(w)
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| No rotation support | Size/time/count/SIGHUP rotation | Phase 11 | Operators can manage disk usage without manual intervention |
| `RotationConfig{FlushIntervalSec int}` | `RotationConfig` with 4 fields | Phase 11 | All rotation parameters in one config struct |
| No `Rotate()` public API | `Writer.Rotate() error` | Phase 11 | Enables programmatic rotation from application layer |
| SIGHUP not handled | SIGHUP → `Rotate()` via `main.go` | Phase 11 | Standard Unix operator convention satisfied |
| `backgroundLoop` only runs fsync ticker | `backgroundLoop` runs fsync + rotation tickers | Phase 11 | Single goroutine manages all time-based operations |

**Deprecated/outdated after Phase 11:**

- None: Phase 11 is additive. Existing `flushChunkLocked()`, `tickFlushLocked()`, `backgroundLoop`, and `Close()` are extended, not replaced.

---

## Open Questions

1. **Reset `recordID` to 1 after rotation?**
   - What we know: Each archive is an independent EVTX file. EVTX record IDs are per-file. Forensics tools read each file independently.
   - What's unclear: If the operator opens multiple archives in Windows Event Viewer simultaneously, duplicate record IDs across files could cause confusion.
   - Recommendation: Reset `recordID = 1` and `firstID = 1` in `rotate()`. Each file is self-contained. This matches how Windows Event Log service handles log rotation.

2. **What if rotation fires and `w.records` is empty (no events since last rotation)?**
   - What we know: `rotate()` calls `flushChunkLocked()` only if `len(w.records) > 0`. If no events were written, the active file has only the 4,096-byte placeholder header and 0 chunks.
   - What's unclear: Should an empty file be rotated (producing an archive with 0 records)?
   - Recommendation: Yes — rotate even if empty. Time-based rotation fires on schedule; skipping empty rotations would require tracking "last rotation time" separately. The archive is a valid EVTX file (ChunkCount=0, parseable). Operators who dislike empty files can use size-based rotation instead of time-based.

3. **go-evtx version for Phase 11**
   - What we know: go-evtx is at v0.3.0. Phase 11 adds `MaxFileSizeMB`, `MaxFileCount`, `RotationIntervalH` to `RotationConfig` and adds `Rotate()` public method. These are new features (minor version bump per semver).
   - Recommendation: Publish as v0.4.0. Update cee-exporter `go.mod` replace directive to `../go-evtx` pointing at local v0.4.0.

4. **Windows `os.Rename` when destination exists**
   - What we know: On Windows, `os.Rename` fails if destination exists. On Linux/macOS, it atomically replaces.
   - Recommendation: For Phase 11, accept this limitation. If two rotations happen within the same second (extremely unlikely with hourly or size-based rotation), log the rename error and continue. Phase 11 has no Windows CI runner for EVTX rotation anyway (noted in STATE.md).

5. **`sort` package import in go-evtx (currently not imported)**
   - What we know: go-evtx has zero external dependencies and uses only stdlib. `sort` is stdlib.
   - Recommendation: Add `"sort"` to the import in `evtx.go`. This is stdlib — no dependency constraint violated.

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing package (stdlib), go 1.24 |
| Config file | None — `go test ./...` in each module |
| Quick run command | `cd /Users/fjacquet/Projects/go-evtx && go test ./...` |
| Full suite command | `cd /Users/fjacquet/Projects/go-evtx && go test -race ./...` |
| cee-exporter sanity | `cd /Users/fjacquet/Projects/cee-exporter && make test` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| ROT-01 | Writing events past `MaxFileSizeMB` threshold triggers rename to timestamped archive | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_SizeRotation -v ./...` | ❌ Wave 0 |
| ROT-01 | Rotated archive is parseable by go-evtx Reader (headers and CRCs finalized before rename) | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_RotatedFileValid -v ./...` | ❌ Wave 0 |
| ROT-02 | Only N most recent archive files remain after N+1 rotations when `MaxFileCount` is set | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_CountRetention -v ./...` | ❌ Wave 0 |
| ROT-02 | Active (non-timestamped) file is never deleted by cleanOldFiles | unit | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_CountRetention_ActiveFilePreserved -v ./...` | ❌ Wave 0 |
| ROT-03 | `Rotate()` called via time ticker produces archive file when `RotationIntervalH > 0` | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_TimeRotation -v ./...` | ❌ Wave 0 |
| ROT-04 | `Rotate()` public method produces archive immediately without dropping in-flight events | integration | `cd /Users/fjacquet/Projects/go-evtx && go test -run TestWriter_ManualRotate -v ./...` | ❌ Wave 0 |
| ROT-04 | SIGHUP wiring: `installSIGHUP` calls `Rotate()` via type assertion | unit | `cd /Users/fjacquet/Projects/cee-exporter && go test -run TestInstallSIGHUP -v ./cmd/cee-exporter/` | ❌ Wave 0 |
| ROT-01–04 | Zero data races during concurrent WriteRecord + Rotate() calls | race | `cd /Users/fjacquet/Projects/go-evtx && go test -race -run TestWriter_RotateRace ./...` | ❌ Wave 0 |
| All | All existing Phase 10 goroutine and openhandle tests still pass | regression | `cd /Users/fjacquet/Projects/go-evtx && go test ./...` | ✅ (all existing tests) |

### Sampling Rate

- **Per task commit:** `cd /Users/fjacquet/Projects/go-evtx && go test ./...`
- **Per wave merge:** `cd /Users/fjacquet/Projects/go-evtx && go test -race ./...` + `cd /Users/fjacquet/Projects/cee-exporter && make test`
- **Phase gate:** Both suites green + race-clean before `/gsd:verify-work`

### Wave 0 Gaps

- [ ] `/Users/fjacquet/Projects/go-evtx/rotation_test.go` — new file; covers ROT-01 through ROT-04 (TestWriter_SizeRotation, TestWriter_CountRetention, TestWriter_TimeRotation, TestWriter_ManualRotate, TestWriter_RotatedFileValid, TestWriter_RotateRace)
- [ ] `/Users/fjacquet/Projects/go-evtx/evtx_unix.go` — new file; `syncDir()` with `//go:build !windows`
- [ ] `/Users/fjacquet/Projects/go-evtx/evtx_windows.go` — new file; `syncDir()` no-op stub with `//go:build windows`
- [ ] `/Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/sighup_notwindows.go` — new file; `installSIGHUP`
- [ ] `/Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/sighup_windows.go` — new file; no-op stub

---

## Sources

### Primary (HIGH confidence)

- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/evtx.go` — Phase 10 Writer struct, `flushChunkLocked()`, `tickFlushLocked()`, `backgroundLoop()`, `Close()`, `New()` — confirmed open-handle model; `RotationConfig` has only `FlushIntervalSec`; `f *os.File` and `chunkCount uint16` confirmed present
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/binformat.go` — `buildFileHeader()`, `evtxFileHeaderSize=4096`, `evtxChunkSize=65536`; `patchChunkCRC()` — all confirmed correct for use in `rotate()`
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/main.go` — `OutputConfig`, `buildWriter()` case "evtx", signal handling loop (SIGTERM/SIGINT); `signal.Notify` pattern confirmed in use
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/pkg/evtx/writer_evtx_notwindows.go` — `BinaryEvtxWriter` struct + `NewBinaryEvtxWriter` + `WriteEvent` + `Close`; no `Rotate()` method yet (Wave 0 gap)
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/.planning/phases/10-open-handle-incremental-flush/10-VERIFICATION.md` — Phase 10 fully verified 6/6; confirms `f *os.File`, `chunkCount`, `flushChunkLocked()`, `tickFlushLocked()` all present and race-clean
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/.planning/research/FEATURES.md` — prior research on rotation behavioral model; lumberjack incompatibility confirmed; `syncDir` directory-fsync requirement documented; SIGHUP pattern documented as `signal.Notify` + goroutine
- [pkg.go.dev/os#Rename](https://pkg.go.dev/os#Rename) — atomic rename on same filesystem; Windows behavior documented
- [pkg.go.dev/path/filepath#Glob](https://pkg.go.dev/path/filepath#Glob) — glob pattern matching; nil return on no match
- [pkg.go.dev/os/signal#Notify](https://pkg.go.dev/os/signal#Notify) — buffered channel recommended; size 1 minimum for SIGHUP
- [pkg.go.dev/syscall#Fsync](https://pkg.go.dev/syscall#Fsync) — directory fsync on Linux via `syscall.Open` + `syscall.Fsync`

### Secondary (MEDIUM confidence)

- `/Users/fjacquet/Projects/cee-exporter/.planning/research/FEATURES.md` (2026-03-04) — behavioral model from lumberjack; SIGHUP is listed as P2 in original research but is explicitly required by ROT-04 in the final requirements
- STATE.md blocker note: "Windows rename (MoveFileEx) may need manual validation if no Windows CI runner available" — confirmed: Phase 11 will have the same limitation

### Tertiary (LOW confidence)

- None — all findings grounded in direct source inspection or stdlib documentation.

---

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — stdlib only; all patterns derived from direct source inspection of Phase 10 code that is already working and race-clean
- Architecture: HIGH — `rotate()` design is straightforward given the open-handle model already in place; SIGHUP wiring is a standard Go pattern; directory fsync is well-documented
- Pitfalls: HIGH — derived from direct analysis of the code that will be modified; lumberjack incompatibility researched and documented in prior FEATURES.md; Windows rename limitation noted in STATE.md

**Research date:** 2026-03-05
**Valid until:** 2026-04-05 (EVTX format frozen; Go stdlib file I/O and signal semantics are stable)
