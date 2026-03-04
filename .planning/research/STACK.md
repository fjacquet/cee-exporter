# Stack Research

**Domain:** Go daemon — periodic fsync + file rotation for BinaryEvtxWriter
**Milestone:** v4.0 Industrialisation
**Researched:** 2026-03-04
**Confidence:** HIGH

---

## Scope

This document covers ONLY the new v4.0 capabilities:

1. Periodic fsync on an open `*os.File` handle (≤15 s flush guarantee)
2. Size + time + count-based rotation of binary `.evtx` files
3. Ticker-based background goroutine for flush/rotation lifecycle

Existing capabilities (GELF, Win32, Syslog, Beats, TLS, Prometheus, async queue,
CGO_ENABLED=0 static linking) are already validated — they are NOT re-researched here.

---

## Recommended Stack

### Core Technologies

| Technology | Version | Purpose | Why Recommended |
|------------|---------|---------|-----------------|
| `os.File.Sync()` | Go stdlib (any) | Periodic fsync to disk | Direct Go wrapper for `fsync(2)` / `F_FULLFSYNC` on Darwin. No dependency. Returns `error`. Correct primitive for the ≤15 s durability guarantee. |
| `time.NewTicker` | Go stdlib (any) | Periodic flush trigger | Canonical Go pattern for repeating intervals. Delivers on channel; combines with `select` + `ctx.Done()` for clean shutdown. Zero allocation after creation. |
| `context.Context` | Go stdlib (any) | Goroutine lifecycle / cancellation | Propagates shutdown signal from the daemon's main signal handler into the flush goroutine without extra done-channel boilerplate. Matches existing queue pattern. |
| `gopkg.in/natefinch/lumberjack.v2` | v2.2.1 | Size + count + age-based file rotation | Pure Go — confirmed: uses `os.Chown` + `syscall.Stat_t` only, no CGO. Implements `io.WriteCloser`. Exposes `Rotate()` for programmatic rotation. Industry standard for file rotation in Go. |

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `gopkg.in/natefinch/lumberjack.v2` | v2.2.1 | File lifecycle: open/close/rename on size threshold; keep N backups; delete files older than MaxAge days | Use for size-triggered rotation (`MaxSize` MB) and backup count pruning (`MaxBackups`). Always when any rotation is enabled. |
| `time` (stdlib) | — | `time.NewTicker` for both fsync ticker (flush_interval_s) and optional rotation ticker (rotation_interval_h) | Already imported in the package. |
| `sync` (stdlib) | — | `sync.Mutex` to protect the file handle during concurrent writes and rotation | Already present in `BinaryEvtxWriter`. |
| `os` (stdlib) | — | `os.File.Sync()`, `os.OpenFile()`, `os.Rename()`, `os.Stat()` | Already imported. |
| `path/filepath` (stdlib) | — | Constructing rotated-file names with timestamps | Already imported. |

### Development Tools

| Tool | Purpose | Notes |
|------|---------|-------|
| `go test -race ./pkg/evtx/` | Detect data races between flush goroutine and WriteEvent | Requires CGO_ENABLED=1; run manually, not via `make test`. |
| `python-evtx` (existing UAT toolchain) | Validate rotated `.evtx` files remain parseable after rotation | Re-run existing UAT against rotated output files. |

---

## Installation

```bash
# Only one new dependency for v4.0:
go get gopkg.in/natefinch/lumberjack.v2@v2.2.1

# All other required packages are Go stdlib — no installation needed:
# time.NewTicker, context.Context, os.File.Sync(), sync.Mutex, os.Rename
```

---

## Alternatives Considered

| Recommended | Alternative | Why Not |
|-------------|-------------|---------|
| `os.File.Sync()` (stdlib) | `github.com/spf13/fsync` | Adds a dependency for a one-line stdlib call. `os.File.Sync()` IS fsync. Zero reason to wrap it. |
| `gopkg.in/natefinch/lumberjack.v2` | `github.com/lestrrat-go/file-rotatelogs` | **Archived July 2021.** README explicitly warns "DO NOT USE IT". Last release v2.4.0 (Sep 2020). Fully eliminated. |
| `gopkg.in/natefinch/lumberjack.v2` | `github.com/easyCZ/logrotate` | Niche library, far less community adoption. Lumberjack is the de-facto Go standard with years of production use. |
| `gopkg.in/natefinch/lumberjack.v2` (v2) | `gopkg.in/lumberjack.v3` | `pkg.go.dev` explicitly flags v3 as NOT the latest version of its module — v2 is canonical. v3 uses functional options API that offers no advantage for this scope. |
| `time.NewTicker` + `context.Context` | dedicated scheduler library | The existing daemon uses zero scheduler libs. Two stdlib primitives cover the full need — no new transitive deps. |
| Programmatic `Rotate()` call via ticker | `WithMaxAge` / `MaxAge` in lumberjack for time rotation | `MaxAge` only DELETES old files after N days — it does NOT rotate on a schedule. For `rotation_interval_h`, a separate `time.NewTicker` calling `lumberjack.Logger.Rotate()` is the correct approach. |

---

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| `github.com/lestrrat-go/file-rotatelogs` | Archived 2021; author warning "DO NOT USE"; no security patches possible | `gopkg.in/natefinch/lumberjack.v2` |
| `ioutil.WriteFile` for periodic flush | Rewrites entire file O(n) on every tick; incompatible with rotation | Keep `*os.File` open; call `.Sync()` periodically |
| Writing directly to `lumberjack.Logger.Write()` as EVTX byte sink | Lumberjack can rotate mid-write, which would split an EVTX chunk boundary, corrupting the file | Use lumberjack for file lifecycle management (open/rotate/prune); drive actual EVTX byte writes through `BinaryEvtxWriter`'s mutex-protected `*os.File` |
| Any CGO-dependent rotation library | `CGO_ENABLED=0` is a hard project constraint; cross-compile from Linux to Windows would break | Pure-Go libraries only. Lumberjack qualifies. |
| `os.File.WriteAt` for concurrent writes | Breaks sequential EVTX record ordering required by the binary format | Keep the existing single-writer mutex pattern |

---

## Stack Patterns by Variant

**For periodic fsync (flush_interval_s, default 15 s):**
- Create `time.NewTicker(time.Duration(cfg.FlushIntervalS) * time.Second)` in `BinaryEvtxWriter` background goroutine.
- On each tick: acquire mutex, call `w.file.Sync()`, release.
- On `ctx.Done()`: stop ticker, perform final `Sync()`, then close.
- Standard pattern: `for { select { case <-ticker.C: syncFile(); case <-ctx.Done(): return } }`
- `ticker.Stop()` MUST be called (GC will not collect it otherwise).

**For size-based rotation (max_file_size_mb, 0 = unlimited):**
- Configure `lumberjack.Logger{Filename: path, MaxSize: cfg.MaxFileSizeMB}`.
- After completing a full valid EVTX chunk boundary, check `os.Stat(path).Size()`.
- If over threshold, call `rotator.Rotate()` to rename and reopen cleanly.
- If `max_file_size_mb == 0`, skip size check entirely.

**For count-based rotation (max_file_count, 0 = unlimited):**
- Configure `lumberjack.Logger{MaxBackups: cfg.MaxFileCount}`.
- Lumberjack removes oldest backup files automatically on each `Rotate()` call.
- If `max_file_count == 0`, set `MaxBackups: 0` (lumberjack retains all backups).

**For time-based rotation (rotation_interval_h, 0 = disabled):**
- Lumberjack has NO built-in schedule rotation. `MaxAge` only deletes old files by age.
- Use a second `time.NewTicker(time.Duration(cfg.RotationIntervalH) * time.Hour)`.
- On tick: call `rotator.Rotate()` explicitly at a clean chunk boundary.
- If `rotation_interval_h == 0`, skip this ticker.

**If all rotation config is zero (no rotation):**
- Disable lumberjack entirely; keep existing single-file `BinaryEvtxWriter` behaviour.
- Still run the fsync ticker — `flush_interval_s` is independent of rotation.

---

## Key Integration Change

The current `flushToFile()` calls `os.WriteFile()` — a full file rewrite on every flush.
This must change to keep `*os.File` open for incremental appends so that:

1. `Sync()` flushes only in-memory OS buffer (not the whole file).
2. Rotation renames the existing open file and opens a new one without data loss.
3. File size can be tracked continuously without a full stat per write.

The `lumberjack.Logger` can own the file-handle lifecycle (open, rename, prune) while
`BinaryEvtxWriter` holds the `*os.File` reference for direct byte writes and `Sync()` calls.

---

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| `gopkg.in/natefinch/lumberjack.v2 v2.2.1` | Go 1.18+ (project uses 1.24) | No breaking changes from v2.0 to v2.2.1. |
| `gopkg.in/natefinch/lumberjack.v2 v2.2.1` | `CGO_ENABLED=0` | Confirmed pure Go: `os.Chown` + `syscall.Stat_t` only. No C bindings anywhere. |
| `time.NewTicker` | All Go versions | Stable stdlib API since Go 1.0. |
| `os.File.Sync()` | All Go versions | On Darwin (Go 1.12+) maps to `F_FULLFSYNC` (stronger guarantee than plain fsync). On Linux maps to `fsync(2)`. |

---

## Sources

- [pkg.go.dev/gopkg.in/natefinch/lumberjack.v2](https://pkg.go.dev/gopkg.in/natefinch/lumberjack.v2) — struct fields (MaxSize, MaxBackups, MaxAge, Compress), Rotate() method, v2.2.1 release date Feb 2023 (HIGH confidence)
- [github.com/natefinch/lumberjack blob/v2.0/chown.go](https://github.com/natefinch/lumberjack/blob/v2.0/chown.go) — confirmed CGO-free: pure Go `os.Chown` + `syscall.Stat_t` only (HIGH confidence)
- [github.com/lestrrat-go/file-rotatelogs](https://github.com/lestrrat-go/file-rotatelogs) — confirmed ARCHIVED July 2021; README: "DO NOT USE THIS PROJECT" (HIGH confidence)
- [pkg.go.dev/os#File.Sync](https://pkg.go.dev/os#File.Sync) — confirmed maps to `fsync(2)` / `F_FULLFSYNC`; returns error (HIGH confidence)
- [pkg.go.dev/gopkg.in/lumberjack.v3](https://pkg.go.dev/gopkg.in/lumberjack.v3) — confirmed NOT latest version of module; v2 canonical per pkg.go.dev notice (MEDIUM confidence)
- Go stdlib `time` package — `NewTicker`, `ticker.Stop()`, select+`ctx.Done()` pattern; GC will not collect unstopped tickers (HIGH confidence — official docs)

---
*Stack research for: periodic fsync + file rotation in Go (BinaryEvtxWriter v4.0 Industrialisation)*
*Researched: 2026-03-04*
