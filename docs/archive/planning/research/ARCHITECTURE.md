# Architecture Research

**Domain:** Periodic fsync + file rotation in BinaryEvtxWriter (Go daemon)
**Researched:** 2026-03-04
**Confidence:** HIGH — based on direct source inspection of the existing codebase

## Standard Architecture

### System Overview: Current State (v3.0)

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         CEPA HTTP Layer                                  │
│  [Dell PowerStore PUT /]  [RegisterRequest handshake]  [Heartbeat ACK]  │
└───────────────────────────────┬─────────────────────────────────────────┘
                                │ WindowsEvent
                                ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                       pkg/queue (async dispatch)                         │
│  channel(cap=100K)   N worker goroutines   Stop()/drain guarantee       │
└───────────────────────────────┬─────────────────────────────────────────┘
                                │ Writer.WriteEvent(ctx, e)
                                ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                    pkg/evtx  Writer interface                            │
│  ┌────────────────┐ ┌─────────────┐ ┌──────────────┐ ┌──────────────┐  │
│  │ BinaryEvtxWriter│ │ GELFWriter  │ │ SyslogWriter │ │ MultiWriter  │  │
│  │ (non-Windows)  │ │ (all platfm)│ │ (all platfm) │ │ (fan-out)    │  │
│  └────────────────┘ └─────────────┘ └──────────────┘ └──────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
                                │
                                ▼ (on Close only)
                        .evtx file on disk
```

### System Overview: Target State (v4.0)

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         CEPA HTTP Layer (unchanged)                      │
└───────────────────────────────┬─────────────────────────────────────────┘
                                │ WindowsEvent
                                ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                       pkg/queue (unchanged interface)                    │
└───────────────────────────────┬─────────────────────────────────────────┘
                                │ Writer.WriteEvent(ctx, e)
                                ▼
┌─────────────────────────────────────────────────────────────────────────┐
│              pkg/evtx  BinaryEvtxWriter (extended)                       │
│                                                                          │
│  WriteEvent() ──► stage records in pending buffer                       │
│                    │                                                     │
│                    └─► check size trigger → rotateLocked() if exceeded  │
│                                                                          │
│  fsyncTicker ──► flush pending to open *os.File + f.Sync()              │
│                                                                          │
│  rotateTicker ──► rotateLocked(): close file, open new timestamped file │
│                                                                          │
│  Open *os.File handle (held between events, not only on Close)          │
└──────────────────────────────────────────────────────────────────────────┘
                │ periodic fdatasync                  │ rotation creates
                ▼                                     ▼
        audit-2026-03-04T120000Z.evtx    audit-2026-03-04T130000Z.evtx ...
```

### Component Responsibilities

| Component | Responsibility | Integration Point |
|-----------|----------------|-------------------|
| `BinaryEvtxWriter` | Hold open file handle; accept writes; run fsync ticker; run rotate ticker; enforce size/count/time limits | Modified: gains `backgroundLoop()` goroutine, `RotationConfig`, `*os.File` handle |
| `OutputConfig` (main.go) | Parse and pass `[output]` TOML fields to writer factory | Modified: new `FlushIntervalSec`, `MaxFileSizeMB`, `MaxFileCount`, `RotationIntervalH` fields |
| `buildWriter()` (main.go) | Construct `BinaryEvtxWriter` with `RotationConfig` from `OutputConfig` | Modified: pass config struct to `NewBinaryEvtxWriter` |
| `Writer` interface (writer.go) | Stable contract for all backends | Unchanged — no new methods needed |
| `pkg/queue` | Async dispatch of events to writers | Unchanged — queue is unaware of flushing or rotation |
| `config.toml.example` | Document all operator-facing fields | Updated with four new rotation/flush fields |

## Recommended Project Structure

No new packages are needed. All changes are contained within existing files:

```
pkg/evtx/
├── writer.go                    # Writer interface — UNCHANGED
├── writer_evtx_notwindows.go    # BinaryEvtxWriter — PRIMARY CHANGE FILE
│                                #   Add: RotationConfig struct
│                                #   Add: open *os.File field
│                                #   Add: pending []byte staging buffer field
│                                #   Add: currentFileBytes int64 field
│                                #   Add: stopCh / stopped channels
│                                #   Add: backgroundLoop() goroutine
│                                #   Add: rotateLocked() helper
│                                #   Add: openRotatedFile() helper
│                                #   Add: pruneOldFiles() helper
│                                #   Modify: NewBinaryEvtxWriter() — accept RotationConfig, open file
│                                #   Modify: WriteEvent() — stage to pending, check size trigger
│                                #   Modify: Close() — stop goroutine, final flush/sync
│                                #   Remove: flushToFile() (replaced by incremental open-handle model)
├── evtx_binformat.go            # UNCHANGED — pure binary helpers
├── writer_native_notwindows.go  # Minimal change: pass RotationConfig to NewBinaryEvtxWriter
└── ...

cmd/cee-exporter/
└── main.go
    ├── OutputConfig              # MODIFIED: add four rotation/flush fields
    ├── defaultConfig()           # MODIFIED: add sensible defaults
    └── buildWriter()             # MODIFIED: map OutputConfig → RotationConfig, pass to constructor

config.toml.example              # MODIFIED: document four new [output] fields
```

### Structure Rationale

- **All rotation logic in `writer_evtx_notwindows.go`:** Rotation is a file-specific concern. The `Writer` interface stays at two methods (`WriteEvent`, `Close`). GELF, Syslog, Beats, and Win32 writers are untouched.
- **No new package:** Scope is one writer backend. A `pkg/rotation` package would be over-engineering for this milestone.
- **Flat fields on `[output]`:** BurntSushi/toml handles flat structs cleanly. All other backends (GELF, Syslog, Beats) already use flat fields on `[output]`. An `[output.evtx]` sub-table would be inconsistent with the existing pattern and adds no value at this scope.

## Architectural Patterns

### Pattern 1: Writer-Owned Background Goroutine

**What:** The fsync ticker and rotation ticker live inside `BinaryEvtxWriter` as a single `backgroundLoop()` goroutine. The goroutine is started by `NewBinaryEvtxWriter` and stopped by `Close()` via a `stopCh chan struct{}`. A `stopped chan struct{}` channel lets `Close()` synchronize before the final flush.

**When to use:** When the goroutine lifecycle is identical to the writer object lifecycle. The queue calls `w.Close()` on `q.Stop()`, so ticker cleanup is automatic and requires no changes to `pkg/queue`.

**Why NOT the queue layer:** The queue is transport-agnostic. It dispatches `WindowsEvent` structs and has no knowledge of file handles, fsync semantics, or file naming. Adding a flush ticker in `pkg/queue` would couple the queue to file-I/O concerns and would require adding a `Flush()` method to the `Writer` interface — forcing all six writer implementations to satisfy a method that is meaningless for GELF, Syslog, Beats, and Win32.

**Trade-offs:**
- Pro: Clean encapsulation, no queue changes, no interface changes
- Pro: Works correctly when `BinaryEvtxWriter` is used inside `MultiWriter` — each writer manages its own ticker independently
- Con: `BinaryEvtxWriter` gains internal goroutines — tests must call `Close()` to avoid goroutine leaks; test helpers should set `FlushIntervalSec=0` to disable the ticker in unit tests

**Sketch:**

```go
type RotationConfig struct {
    FlushIntervalSec  int   // periodic fsync interval; 0 = only on Close
    MaxFileSizeMB     int64 // rotate at this size; 0 = unlimited
    MaxFileCount      int   // keep this many rotated files; 0 = keep all
    RotationIntervalH int   // rotate every N hours; 0 = disabled
}

type BinaryEvtxWriter struct {
    mu               sync.Mutex
    path             string
    f                *os.File
    pending          []byte
    records          []byte
    recordID         uint64
    firstID          uint64
    currentFileBytes int64
    cfg              RotationConfig
    stopCh           chan struct{}
    stopped          chan struct{}
}

func NewBinaryEvtxWriter(evtxPath string, cfg RotationConfig) (*BinaryEvtxWriter, error) {
    w := &BinaryEvtxWriter{
        path:    evtxPath,
        cfg:     cfg,
        stopCh:  make(chan struct{}),
        stopped: make(chan struct{}),
        recordID: 1,
        firstID:  1,
    }
    f, err := w.openRotatedFile(evtxPath)
    if err != nil {
        return nil, err
    }
    w.f = f
    go w.backgroundLoop()
    return w, nil
}
```

### Pattern 2: Open-Handle Incremental Write Model

**What:** Replace the current `os.WriteFile()` on Close with an open `*os.File` held for the writer's lifetime. `WriteEvent` stages encoded records in a `pending []byte` buffer. The fsync tick drains `pending` into the open file with `f.Write(pending)` then calls `f.Sync()`.

**Why this is required:** The current model calls `os.WriteFile` which opens, truncates, writes, and closes on every invocation. This is safe for the Close-only model but cannot provide periodic durability — there is no file handle to sync between events. Additionally, `os.WriteFile` re-serializes all records on every call, which grows O(n) in cost as records accumulate.

**Trade-offs:**
- Pro: True durability guarantee — crash loses at most `FlushIntervalSec` seconds of events
- Pro: Enables size-based rotation via tracking `currentFileBytes`
- Pro: Write cost per tick is proportional to new events since last tick, not total file size
- Con: The EVTX file header fields (`NextRecordIdentifier`, `ChunkCount`) must be patched on each rotation and Close — not just at final Close
- Con: A crash mid-flush leaves an open chunk without a valid CRC — parsers must tolerate this (python-evtx is tolerant; Event Viewer may not be; acceptable for a Linux forensics file)

**Note on EVTX chunk boundary:** Each rotated file remains a single-chunk file (matching the current design). Rotation is the mechanism for bounding file size — not multi-chunk support. This avoids adding multi-chunk complexity to the BinXML writer.

### Pattern 3: Timestamped Rotation File Naming

**What:** On rotation, close the current file and open a new file with a timestamp suffix. The config `evtx_path` value is treated as a base path: the directory and stem are preserved, the timestamp and `.evtx` extension are generated.

**Recommended:** Generate names from the base path. Given `evtx_path = "/var/log/cee-exporter/audit.evtx"`:
- Active file: `/var/log/cee-exporter/audit-2026-03-04T120000Z.evtx`
- Next rotation: `/var/log/cee-exporter/audit-2026-03-04T130000Z.evtx`

The base name is derived by stripping the `.evtx` extension from `evtxPath`, appending a UTC timestamp, then re-adding `.evtx`. Pruning uses `filepath.Glob` on the stripped prefix.

**Trade-offs:**
- Pro: Completed files have stable names — safe to ingest or archive while the daemon continues writing to the newest file
- Pro: File count pruning is trivial (`Glob` + sort by name, which sorts by time since timestamps are lexicographic)
- Con: External consumers must discover the active file by listing the directory rather than opening a fixed path

## Data Flow

### Write Path with Periodic fsync

```
WriteEvent(ctx, e)
    │
    ├─► encode BinXML record bytes  (CPU, in-memory, unchanged from current)
    │
    ├─► w.mu.Lock()
    ├─► w.pending = append(w.pending, rec...)  (stage in pending buffer)
    ├─► check size trigger:
    │       if w.currentFileBytes + int64(len(w.pending)) > MaxFileSizeMB*1024*1024
    │           → call rotateLocked()  (flushes pending, closes file, opens new)
    └─► w.mu.Unlock()
    → return nil  (no disk I/O per event in normal path)

backgroundLoop() goroutine — select on tickers and stopCh:

    fsyncTicker fires every FlushIntervalSec:
        ├─► w.mu.Lock()
        ├─► if len(w.pending) > 0:
        │       n, _ := w.f.Write(w.pending)
        │       w.currentFileBytes += int64(n)
        │       w.pending = w.pending[:0]
        ├─► w.f.Sync()
        └─► w.mu.Unlock()

    rotateTicker fires every RotationIntervalH (if > 0):
        └─► w.mu.Lock() → rotateLocked() → w.mu.Unlock()

rotateLocked() — called with w.mu held:
    ├─► flush w.pending to w.f  (same as fsync path above)
    ├─► patch EVTX file header (NextRecordIdentifier, ChunkCount = 1)
    ├─► w.f.Sync()
    ├─► w.f.Close()
    ├─► newPath = generateTimestampedPath(w.basePath)
    ├─► w.f = openRotatedFile(newPath)  — write placeholder file header
    ├─► w.currentFileBytes = evtxFileHeaderSize
    ├─► w.firstID = w.recordID  (continue record IDs across rotation)
    └─► pruneOldFiles()  — delete oldest files if len(files) > MaxFileCount
```

### Shutdown Path

```
signal received (SIGTERM / SIGINT) or context cancelled
    │
    └─► q.Stop()
            │
            ├─► close(q.ch) — drain channel
            ├─► q.wg.Wait() — workers finish
            └─► w.Close()
                    │
                    ├─► close(w.stopCh)    — signal backgroundLoop to exit
                    ├─► <-w.stopped        — wait for goroutine to exit cleanly
                    ├─► w.mu.Lock()
                    ├─► flush w.pending to w.f
                    ├─► patch EVTX file header
                    ├─► w.f.Sync()
                    └─► w.f.Close()
```

### Config Flow

```
config.toml
[output]
flush_interval_s    = 15
max_file_size_mb    = 100
max_file_count      = 10
rotation_interval_h = 24
        │
        ▼ BurntSushi/toml decodes into OutputConfig
        │
        ▼ buildWriter(cfg.Output) in main.go
        │
        ▼ evtx.NewBinaryEvtxWriter(cfg.EVTXPath, evtx.RotationConfig{
              FlushIntervalSec:  cfg.FlushIntervalSec,
              MaxFileSizeMB:     cfg.MaxFileSizeMB,
              MaxFileCount:      cfg.MaxFileCount,
              RotationIntervalH: cfg.RotationIntervalH,
          })
```

## Scaling Considerations

This is a single-host daemon. The relevant axis is audit event throughput from PowerStore VCAPS batches.

| Concern | Current (v3.0) | With v4.0 rotation |
|---------|----------------|--------------------|
| Write latency per event | ~0 (in-memory append) | ~0 (pending buffer append) |
| Disk I/O frequency | Once on process exit | Every `flush_interval_s` seconds (default 15) |
| Memory for unflushed events | All events since process start | Events accumulated since last fsync tick |
| Max single file size | Unbounded (limited only by chunk size = 65536 B of records) | Bounded by `max_file_size_mb` |
| Disk space management | Manual operator intervention | Automatic via `max_file_count` |
| Crash durability guarantee | Zero (all records lost) | At most `flush_interval_s` seconds of events |

### Scaling Priorities

1. **First bottleneck:** `rotateLocked()` holds `w.mu` while doing disk I/O (file close, new file open). Worker goroutines calling `WriteEvent` will briefly contend for the mutex during rotation. This is acceptable — rotation is infrequent (hourly or size-triggered) and the mutex hold time is bounded by two `f.Close()`/`f.Open()` calls.
2. **Second bottleneck:** Large `w.pending` at fsync time if event burst precedes the tick. Reduce `FlushIntervalSec` or increase `max_file_size_mb` to control memory pressure.

## Anti-Patterns

### Anti-Pattern 1: Flush Ticker in the Queue Layer

**What people do:** Add a `time.Ticker` in `pkg/queue` that calls a `Flush()` method on the `Writer` interface at regular intervals.

**Why it's wrong:** The queue is transport-agnostic. GELF and Syslog writers do not need flushing — network writes are already non-buffered. Adding `Flush()` to the `Writer` interface forces all six implementations (GELFWriter, SyslogWriter, BeatsWriter, Win32Writer, BinaryEvtxWriter, MultiWriter) to implement dead methods. It also couples the queue to file-I/O semantics that belong exclusively to `BinaryEvtxWriter`.

**Do this instead:** Embed the ticker inside `BinaryEvtxWriter`. The writer owns its file handle and is the only component that knows whether an fsync is meaningful.

### Anti-Pattern 2: Rewrite-on-Sync (full `os.WriteFile` per tick)

**What people do:** Keep `flushToFile()` as-is and call it on every fsync tick — re-writing the entire accumulated buffer to disk each time.

**Why it's wrong:** `os.WriteFile` opens, truncates, writes, and closes on every invocation. Truncation creates a window where the file exists but is empty — any crash in that window loses all data. Write cost grows O(n) with accumulated records. The file header is re-serialized from scratch each time.

**Do this instead:** Hold an open `*os.File`. Append only the new (pending) records on each tick. Call `f.Sync()` at the end. Write cost per tick is proportional to new events only.

### Anti-Pattern 3: Extending the Writer Interface for Rotation

**What people do:** Add `Rotate() error` or `Flush() error` to the `evtx.Writer` interface so that callers can trigger these operations explicitly.

**Why it's wrong:** The `Writer` interface has two methods (`WriteEvent`, `Close`). This is the correct surface area. Adding rotation/flush methods breaks all other implementations and makes `MultiWriter.Rotate()` ambiguous (rotate all? rotate only file writers?).

**Do this instead:** `BinaryEvtxWriter` manages its own rotation schedule autonomously via its internal goroutine. External triggers (if ever needed) should type-assert to `*BinaryEvtxWriter` — not go through the interface.

### Anti-Pattern 4: Passing Full `OutputConfig` to `NewBinaryEvtxWriter`

**What people do:** Pass the entire `OutputConfig` struct to the writer constructor to avoid defining a separate config struct.

**Why it's wrong:** `OutputConfig` contains fields for GELF (`gelf_host`, `gelf_port`, `gelf_tls`), Syslog, and Beats that are irrelevant to `BinaryEvtxWriter`. This couples the writer package to the main package's config schema and makes the writer harder to test in isolation.

**Do this instead:** Define `RotationConfig` in `pkg/evtx`. `main.go` maps the relevant `OutputConfig` fields to `RotationConfig` at construction time. The writer package has no import dependency on the main package.

## Integration Points

### New Config Fields — TOML Layout

Flat fields on the existing `[output]` section, consistent with all other backend fields:

```toml
[output]
type      = "evtx"
evtx_path = "/var/log/cee-exporter/audit.evtx"

# BinaryEvtxWriter durability and rotation (effective only when type = "evtx" on Linux)
flush_interval_s    = 15   # fsync to disk every N seconds; 0 = only on Close
max_file_size_mb    = 100  # rotate when file exceeds N MiB; 0 = no size-based rotation
max_file_count      = 10   # keep at most N rotated files; 0 = keep all
rotation_interval_h = 24   # rotate every N hours; 0 = no time-based rotation
```

### Go Struct Changes

`OutputConfig` in `main.go` gains four fields:

```go
type OutputConfig struct {
    // ... all existing fields unchanged ...

    // BinaryEvtxWriter rotation and durability (evtx type only)
    FlushIntervalSec  int   `toml:"flush_interval_s"`
    MaxFileSizeMB     int64 `toml:"max_file_size_mb"`
    MaxFileCount      int   `toml:"max_file_count"`
    RotationIntervalH int   `toml:"rotation_interval_h"`
}
```

`defaultConfig()` defaults:

```go
Output: OutputConfig{
    Type:             "gelf",
    GELFHost:         "localhost",
    GELFPort:         12201,
    GELFProtocol:     "udp",
    FlushIntervalSec: 15, // ≤15s durability guarantee by default
    // MaxFileSizeMB, MaxFileCount, RotationIntervalH default to 0 (disabled)
},
```

### Internal Boundaries

| Boundary | Communication | Change Required |
|----------|---------------|-----------------|
| `main.go` ↔ `pkg/evtx` | `NewBinaryEvtxWriter(path, RotationConfig)` | Signature change — add `RotationConfig` parameter |
| `writer_native_notwindows.go` ↔ `BinaryEvtxWriter` | `NewBinaryEvtxWriter` call | Update to pass `RotationConfig{}` (zero = defaults, no rotation) |
| `pkg/queue` ↔ `evtx.Writer` | `Writer.WriteEvent`, `Writer.Close` | No change — interface is stable |
| `BinaryEvtxWriter` ↔ `os.File` | Open handle held for writer lifetime | Major change — replace `os.WriteFile` with incremental append |

### Suggested Build Order

Dependencies determine sequencing. Tasks that can parallelize are grouped.

**Step 1 (foundation — do first, unblocks all other tasks):**
- Define `RotationConfig` struct in `pkg/evtx/writer_evtx_notwindows.go`
- Update `NewBinaryEvtxWriter` signature to accept `RotationConfig`
- Update `writer_native_notwindows.go` to pass zero `RotationConfig` (backward-compatible)
- Add `OutputConfig` fields and `defaultConfig()` values in `main.go`
- Update `buildWriter()` to map `OutputConfig` → `RotationConfig`

**Step 2 (open-handle model — highest risk, do before any goroutine work):**
- Replace `os.WriteFile` with open `*os.File` held at construction
- Add `pending []byte` staging buffer and `currentFileBytes` counter
- Update `WriteEvent` to append to `pending` instead of `w.records`
- Rewrite `Close()` to flush pending, patch file header, sync, close
- Write targeted tests verifying EVTX binary correctness with the new model

**Step 3 (fsync goroutine — depends on Step 2):**
- Add `stopCh` / `stopped` channels to `BinaryEvtxWriter`
- Implement `backgroundLoop()` with fsync ticker
- Update `Close()` to signal and wait for goroutine before final flush

**Step 4 (rotation — depends on Steps 2 and 3, can be done in parallel sub-tasks):**
- Implement `rotateLocked()` helper
- Implement `openRotatedFile()` helper (generates timestamped path)
- Add size-based trigger check in `WriteEvent`
- Add time-based rotation ticker in `backgroundLoop()`
- Implement `pruneOldFiles()` helper (depends on rotation naming being stable)

**Step 5 (documentation and ADRs — can be done alongside Step 4):**
- Update `config.toml.example` with the four new fields and operator guidance
- Write ADR-01: Flush ticker ownership (writer layer vs queue layer)
- Write ADR-02: Open-handle vs write-on-close model and EVTX crash tolerance

## Sources

- Direct source inspection: `pkg/evtx/writer_evtx_notwindows.go` (current `BinaryEvtxWriter`)
- Direct source inspection: `pkg/evtx/writer.go` (`Writer` interface, `WindowsEvent` struct)
- Direct source inspection: `pkg/evtx/evtx_binformat.go` (EVTX binary format helpers)
- Direct source inspection: `pkg/queue/queue.go` (queue lifecycle and `Stop()` contract)
- Direct source inspection: `cmd/cee-exporter/main.go` (`OutputConfig`, `buildWriter`, `defaultConfig`)
- Direct source inspection: `config.toml.example` (existing TOML layout conventions)
- Project context: `.planning/PROJECT.md` (v4.0 milestone goals and constraints)

---
*Architecture research for: periodic fsync + file rotation in BinaryEvtxWriter*
*Researched: 2026-03-04*
