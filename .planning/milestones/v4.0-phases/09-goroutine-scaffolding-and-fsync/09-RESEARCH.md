# Phase 9: Goroutine Scaffolding and fsync - Research

**Researched:** 2026-03-04
**Domain:** Go concurrency — ticker-based background goroutine, graceful shutdown, fsync semantics in go-evtx
**Confidence:** HIGH

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| FLUSH-01 | Operator can set `flush_interval_s` (default 15) so BinaryEvtxWriter calls `f.Sync()` every N seconds | Goroutine + ticker pattern documented; RotationConfig struct design specified; zero-value panic guard identified |
| FLUSH-02 | BinaryEvtxWriter flushes and fsyncs all buffered events to disk before the process exits on graceful shutdown | Full shutdown chain documented: SIGTERM → queue.Stop() → writer.Close() → goroutine.Wait(); no buffered data lost |
| ADR-01 | ADR documents flush ticker ownership inside writer layer (not queue layer) | Architecture rationale documented; Writer interface stability constraint identified |
| ADR-02 | ADR documents write-on-close model and planned supersession by open-handle in Phase 10 | Current model in evtx.go fully analysed; Phase 10 transition path documented |
</phase_requirements>

---

## Summary

Phase 9 adds a background goroutine to `go-evtx`'s `Writer` struct that fires a ticker every `FlushIntervalSec` seconds. Because the current go-evtx v0.1.0 uses a write-on-close model (`os.WriteFile` in `Close()`), there is no persistent file handle to call `f.Sync()` on during the writer's lifetime. Phase 9 therefore adopts the **checkpoint-write pattern**: each ticker tick calls `w.flushToFile()` (writing the file atomically from the in-memory `w.records` buffer), which produces a complete, parseable EVTX file at each tick. This is semantically correct — each checkpoint replaces the previous file with a more complete version — and defers the open-handle refactor to Phase 10 where it belongs.

The primary engineering challenge in Phase 9 is not the I/O logic but the concurrency lifecycle: the goroutine must exit cleanly when `Close()` is called, the background flush and the `Close()` flush must not race on `w.records`, and `go test -race` must report zero races. The canonical Go pattern is `done chan struct{}` + `sync.WaitGroup` paired with an existing `sync.Mutex` that guards all record and file operations. The cee-exporter adapter (`BinaryEvtxWriter`) receives a `RotationConfig` struct at construction time and passes `FlushIntervalSec` to `goevtx.New()`.

Phase 9 also delivers ADR-01 and ADR-02 as committed documents in `docs/adr/`. Both ADRs are non-trivial: ADR-01 must justify why flush ticker ownership belongs in the writer layer (not queue or adapter layer), and ADR-02 must document the write-on-close model and why open-handle is deferred.

**Primary recommendation:** Implement the checkpoint-write pattern in go-evtx with `done chan struct{}` + `sync.WaitGroup`. Protect all `w.records` and file operations under `w.mu`. Gate the ticker with `if cfg.FlushIntervalSec > 0` to prevent `time.NewTicker(0)` panic. Write two ADRs. Wire `FlushIntervalSec` through cee-exporter config with a default of 15.

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `time.NewTicker` | Go stdlib | Periodic flush trigger | Zero-allocation after construction; select+done pattern is canonical; no external dep |
| `sync.Mutex` | Go stdlib | Guards `w.records` and all file I/O | Already used in go-evtx `Writer`; extend existing lock scope rather than introduce a new one |
| `sync.WaitGroup` | Go stdlib | Synchronize goroutine exit on Close() | The only correct way to ensure goroutine has finished before Close() returns |
| `os.File.Sync()` | Go stdlib | `fsync(2)` / `F_FULLFSYNC` — actual durability guarantee | Returns error; direct OS call; no alternatives exist |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `done chan struct{}` | Go idiom | Signal goroutine to stop | Pair with WaitGroup; closed by Close(); goroutine exits on receive |
| `context.Context` | Go stdlib | Propagate daemon shutdown | Already used in queue workers; NOT needed inside go-evtx goroutine — Close() is the shutdown signal |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `done chan struct{}` | `context.Context` in goroutine | Context adds cancellation propagation but goroutine has no I/O ops that use it; `done` is simpler and correct for this scope |
| Checkpoint-write (periodic full flush) | No-op goroutine skeleton | No-op delivers FLUSH-01 only as a stub; operator-visible fsync does not actually occur until Phase 10; misleading |
| Checkpoint-write | Open-handle incremental flush | Open-handle is Phase 10's work; doing it in Phase 9 collapses two phases and risks the EVTX header correctness pitfalls that Phase 10 is scoped to solve |

**Installation:** No new packages. All changes use Go stdlib. go-evtx remains dependency-free (`go.mod` has no dependencies). cee-exporter gains no new dependencies.

---

## Architecture Patterns

### Recommended Project Structure

Changes are confined to two repositories:

```
go-evtx/
└── evtx.go          # Writer struct gains done, wg, goroutine, RotationConfig

cee-exporter/
├── pkg/evtx/
│   └── writer_evtx_notwindows.go  # BinaryEvtxWriter gains RotationConfig passthrough
└── cmd/cee-exporter/
    └── main.go      # OutputConfig gains FlushIntervalSec; defaultConfig() sets 15
```

No new files. No new packages.

### Pattern 1: Writer-Owned Goroutine with done + WaitGroup

**What:** Background goroutine started by `New()`, stopped by `Close()` using a `done` channel and a `WaitGroup`.

**When to use:** Any long-lived object that owns a background loop that must stop before the object can be safely closed.

**Example:**
```go
// Source: Go standard library idiom (documented in sync.WaitGroup godoc)
type Writer struct {
    mu   sync.Mutex
    path string
    records []byte
    recordID uint64
    firstID  uint64
    // Phase 9 additions:
    cfg  RotationConfig
    done chan struct{}
    wg   sync.WaitGroup
}

// RotationConfig holds flush/rotation parameters.
// FlushIntervalSec must be > 0; enforced at go-evtx New() time.
type RotationConfig struct {
    FlushIntervalSec int // default 15; 0 disables background flush
}

func New(path string, cfg RotationConfig) (*Writer, error) {
    // ... existing validation ...
    w := &Writer{
        path:     path,
        recordID: 1,
        firstID:  1,
        cfg:      cfg,
        done:     make(chan struct{}),
    }
    if cfg.FlushIntervalSec > 0 {
        w.wg.Add(1)
        go w.backgroundLoop()
    }
    return w, nil
}

func (w *Writer) backgroundLoop() {
    defer w.wg.Done()
    ticker := time.NewTicker(time.Duration(w.cfg.FlushIntervalSec) * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            w.mu.Lock()
            if len(w.records) > 0 {
                _ = w.flushToFile() // checkpoint write: overwrites existing file
            }
            w.mu.Unlock()
        case <-w.done:
            return
        }
    }
}

func (w *Writer) Close() error {
    close(w.done)   // signal goroutine to stop
    w.wg.Wait()     // wait for goroutine to exit
    w.mu.Lock()
    defer w.mu.Unlock()
    if len(w.records) == 0 {
        return nil
    }
    return w.flushToFile() // final flush after goroutine has exited
}
```

### Pattern 2: Config Passthrough in cee-exporter

**What:** `RotationConfig` flows from TOML config → `OutputConfig` → `NewBinaryEvtxWriter()` → `goevtx.New()`.

**When to use:** Phase 9 only introduces `FlushIntervalSec`; remaining config fields are added in Phase 12.

**Example:**
```go
// Source: cee-exporter/cmd/cee-exporter/main.go pattern — mirrors existing OutputConfig style
type OutputConfig struct {
    // ... existing fields ...
    FlushIntervalSec int `toml:"flush_interval_s"` // default 15; 0 disables
}

func defaultConfig() Config {
    return Config{
        // ... existing defaults ...
        Output: OutputConfig{
            // ... existing defaults ...
            FlushIntervalSec: 15,
        },
    }
}

// In buildWriter, case "evtx":
w, err := evtx.NewNativeEvtxWriter(cfg.EVTXPath, goevtx.RotationConfig{
    FlushIntervalSec: cfg.FlushIntervalSec,
})
```

```go
// Source: cee-exporter/pkg/evtx/writer_evtx_notwindows.go
func NewBinaryEvtxWriter(evtxPath string, cfg goevtx.RotationConfig) (*BinaryEvtxWriter, error) {
    w, err := goevtx.New(evtxPath, cfg)
    if err != nil {
        return nil, fmt.Errorf("binary_evtx_writer: %w", err)
    }
    return &BinaryEvtxWriter{w: w}, nil
}
```

```go
// Source: cee-exporter/pkg/evtx/writer_native_notwindows.go
// Pass zero RotationConfig for backward compatibility (goroutine disabled when FlushIntervalSec=0)
func NewNativeEvtxWriter(path string, cfg ...goevtx.RotationConfig) (Writer, error) {
    var rcfg goevtx.RotationConfig
    if len(cfg) > 0 {
        rcfg = cfg[0]
    }
    return NewBinaryEvtxWriter(path, rcfg)
}
```

### Pattern 3: ADR Document Format

**What:** Numbered ADR following existing `docs/adr/ADR-NNN-*.md` convention.

**Files to create:**
- `docs/adr/ADR-012-flush-ticker-ownership.md` (ADR-01 requirement)
- `docs/adr/ADR-013-write-on-close-model.md` (ADR-02 requirement)

**ADR-01 key argument:** The `Writer` interface has two methods (`WriteEvent`, `Close`). Adding `Flush()` to the interface would require every writer backend (GELF, Win32, Syslog, Beats) to implement a method they have no meaningful use for. Owning the ticker inside `BinaryEvtxWriter` (via go-evtx) keeps the interface stable and isolates the durability concern to the one backend that needs it.

**ADR-02 key argument:** The current write-on-close model (`os.WriteFile` on `Close()`) is a complete, atomic file write. It provides no intermediate durability but is correct for sessions where events fit in a single chunk. Phase 9 extends it with a checkpoint pattern (periodic full rewrites). Phase 10 will supersede it with an open-handle model that enables true incremental fsync without full file rewrites.

### Anti-Patterns to Avoid

- **Calling `ticker.Stop()` without a done channel:** `ticker.Stop()` does not close the channel. The goroutine will leak, blocking forever on `<-ticker.C` after `Close()` returns. Always pair `ticker.Stop()` with `<-w.done`.
- **Calling `flushToFile()` from the goroutine without `w.mu`:** Creates a write race with `WriteRecord()` and `Close()`. Every code path touching `w.records` or the file must hold `w.mu` for the complete operation.
- **Starting the goroutine when `FlushIntervalSec == 0`:** `time.NewTicker(0)` panics. Gate with `if cfg.FlushIntervalSec > 0`.
- **Calling `close(w.done)` twice:** Double-close panics in Go. Protect with a `sync.Once` or ensure `Close()` can only be called once.
- **`w.wg.Wait()` inside `w.mu.Lock()`:** Deadlock. The goroutine needs `w.mu` to do its final tick; if `Close()` holds `w.mu` while waiting, the goroutine can never acquire it. Pattern: `close(w.done)` → `w.wg.Wait()` → `w.mu.Lock()` → final flush → `w.mu.Unlock()`.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Goroutine lifecycle | Custom state machine with bool flags | `done chan struct{}` + `sync.WaitGroup` | Booleans are racy without additional synchronization; channels are the Go-idiomatic, race-detector-safe primitive |
| Periodic trigger | Sleep loop | `time.NewTicker` | Sleep loop has drift; ticker is monotonic; select+done is the shutdown pattern |
| File durability | `os.File.Close()` for durability | `os.File.Sync()` | `Close()` does NOT call `fsync(2)` on Linux; only `f.Sync()` issues the syscall |

**Key insight:** In Phase 9's checkpoint-write model, `f.Sync()` is called implicitly — `os.WriteFile` opens, writes, and closes the file, and OS close does not guarantee durability. The correct statement is: the checkpoint-write pattern gives bounded durability only if the OS write cache is treated as unreliable. True `f.Sync()` cannot be added until Phase 10 provides a persistent file handle. ADR-02 must document this nuance honestly.

---

## Common Pitfalls

### Pitfall 1: WaitGroup/Done Deadlock

**What goes wrong:** `Close()` acquires `w.mu`, then calls `w.wg.Wait()`. The background goroutine is mid-tick and waiting on `w.mu` to complete its flush. Both sides wait forever.

**Why it happens:** Incorrect ordering — lock acquired before waiting for goroutine exit.

**How to avoid:** The sequence in `Close()` MUST be:
1. `close(w.done)` — signal goroutine
2. `w.wg.Wait()` — wait for goroutine to exit (without holding any lock)
3. `w.mu.Lock()` — now safe to acquire; goroutine is dead
4. Final flush under lock
5. `w.mu.Unlock()`

**Warning signs:** `go test -race` hangs (deadlock) rather than reporting a race.

### Pitfall 2: Goroutine Leak from ticker.Stop() Misuse

**What goes wrong:** `ticker.Stop()` is called but the goroutine continues to run because `<-ticker.C` may have a pending tick already in the channel buffer.

**Why it happens:** Per Go docs: "`Stop` does not close the channel, to prevent a read from the channel succeeding incorrectly after a call to Stop." A concurrent tick already in the buffer will still be received.

**How to avoid:** Always pair `ticker.Stop()` with a `done` channel check in the select. The `for/select` pattern shown in Pattern 1 handles this correctly — the `<-w.done` case takes priority over the buffered tick.

**Warning signs:** `go test -race ./... -count=100` with goroutine leak detector shows goroutines alive after `Close()`.

### Pitfall 3: go-evtx API Signature Change

**What goes wrong:** `goevtx.New(path string)` currently takes one argument. Phase 9 adds `RotationConfig`. The cee-exporter adapter calls the old signature and compiles but gets zero config.

**Why it happens:** Go does not enforce callers update when a function signature changes.

**How to avoid:** Grep all callers of `goevtx.New` in cee-exporter after changing the go-evtx signature. The only callers are `writer_evtx_notwindows.go` and tests.

**Warning signs:** `go build ./...` fails in cee-exporter after go-evtx signature change if not updated.

### Pitfall 4: Checkpoint-Write Overwrites Good File with Partial Data

**What goes wrong:** A tick fires while `WriteRecord` is in progress. The goroutine acquires `w.mu` after `WriteRecord` releases it mid-batch, writes a partial snapshot, then `WriteRecord` resumes and adds more records. The next tick writes a more complete file. On crash between ticks, the latest file on disk may miss events that were in memory.

**Why it happens:** The checkpoint-write pattern trades atomicity-per-event for bounded loss window. This is by design.

**How to avoid:** ADR-02 must document this: "checkpoint-write guarantees at most `FlushIntervalSec` seconds of data loss on crash, not zero-loss." The `flushToFile()` call inside the goroutine is safe because `w.mu` is held; `w.records` is a consistent snapshot at that moment.

### Pitfall 5: go test -race Requires CGO

**What goes wrong:** `make test` in cee-exporter uses `CGO_ENABLED=0`, which disables the race detector.

**Why it happens:** The race detector requires CGO to instrument memory accesses.

**How to avoid:** Run race tests separately: `cd ~/Projects/go-evtx && go test -race ./...`. Add a comment in the test file noting this requirement. The CLAUDE.md for cee-exporter already documents this pattern.

---

## Code Examples

Verified patterns from official sources and direct codebase inspection:

### Canonical done+WaitGroup Goroutine Lifecycle

```go
// Source: Go sync.WaitGroup documentation + github.com/golang/go/issues/68483 analysis
// This is the ONLY correct pattern for go test -race clean goroutine shutdown.

func (w *Writer) backgroundLoop() {
    defer w.wg.Done()
    ticker := time.NewTicker(time.Duration(w.cfg.FlushIntervalSec) * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            w.mu.Lock()
            if len(w.records) > 0 {
                _ = w.flushToFile()
            }
            w.mu.Unlock()
        case <-w.done:
            return
        }
    }
}

// Close: CRITICAL ordering — wait BEFORE locking
func (w *Writer) Close() error {
    close(w.done)   // 1. signal goroutine
    w.wg.Wait()     // 2. wait for goroutine exit (no lock held)
    w.mu.Lock()     // 3. now safe to acquire
    defer w.mu.Unlock()
    if len(w.records) == 0 {
        return nil
    }
    return w.flushToFile() // 4. final flush
}
```

### Zero-Value Guard for Ticker

```go
// Source: time.NewTicker documentation — "The duration d must be greater than zero"
// Panic guard is mandatory:

func New(path string, cfg RotationConfig) (*Writer, error) {
    if path == "" {
        return nil, fmt.Errorf("go_evtx: path must be non-empty")
    }
    if cfg.FlushIntervalSec < 0 {
        return nil, fmt.Errorf("go_evtx: FlushIntervalSec must be >= 0 (0 = disabled)")
    }
    // ... rest of New() ...
    if cfg.FlushIntervalSec > 0 {
        w.wg.Add(1)
        go w.backgroundLoop()
    }
    return w, nil
}
```

### Race Test for goroutine + WriteRecord concurrency

```go
// Source: go-evtx/evtx_test.go pattern (extend existing TestWriter_Concurrent)
// Run with: go test -race ./... (requires CGO=1, run outside make test)

func TestWriter_BackgroundFlush_NoRace(t *testing.T) {
    dir := t.TempDir()
    outPath := filepath.Join(dir, "race_test.evtx")

    w, err := New(outPath, RotationConfig{FlushIntervalSec: 1})
    if err != nil {
        t.Fatalf("New: %v", err)
    }

    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func(n int) {
            defer wg.Done()
            fields := map[string]string{"Computer": "testhost"}
            _ = w.WriteRecord(4663, fields)
        }(i)
    }
    wg.Wait()

    // Close must not race with background goroutine
    if err := w.Close(); err != nil {
        t.Fatalf("Close: %v", err)
    }
}
```

### Shutdown Chain (end-to-end)

```go
// Source: cee-exporter/cmd/cee-exporter/main.go (existing pattern — q.Stop() already calls writer.Close())
// SIGTERM → main signal handler → q.Stop() → writer.Close() → goroutine exits

// Existing shutdown sequence in main.go (no changes needed):
q.Stop()  // closes channel, drains workers, then calls writer.Close()
// writer.Close() → close(w.done) → w.wg.Wait() → final flush → return
```

The shutdown chain requires NO changes to `main.go`, `queue.go`, or the `Writer` interface. The goroutine lifecycle is entirely self-contained within `go-evtx`.

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `BinaryEvtxWriter` owned EVTX code in cee-exporter | `go-evtx` is the EVTX module; `BinaryEvtxWriter` is a thin adapter | Phase 8.5 (2026-03-04) | Phase 9 changes go IN go-evtx, not cee-exporter |
| Write-on-close (single `os.WriteFile` in `Close()`) | Same in v0.1.0; Phase 9 adds checkpoint-write via goroutine | Phase 9 | Bounded durability without open-handle refactor |
| No goroutine in go-evtx `Writer` | Phase 9 adds background goroutine | Phase 9 | Sets concurrency contract for Phase 10 |
| Open-handle incremental flush | Planned for Phase 10 | Phase 10 | True `f.Sync()` becomes possible |

**Deprecated/outdated:**
- `go-evtx` `New(path string)` — single-argument signature becomes `New(path string, cfg RotationConfig)` in Phase 9

---

## Open Questions

1. **`flushToFile()` idempotency under repeated calls**
   - What we know: `flushToFile()` calls `os.WriteFile` which truncates+rewrites the file each time. `w.records` grows monotonically; each checkpoint writes a superset of the previous.
   - What's unclear: If a tick fires, writes the file, then `Close()` fires immediately after, is the final flush a no-op or does it write the same data again? Currently no guard.
   - Recommendation: Add a guard in `Close()` — if `flushToFile()` was called by the goroutine within the last tick interval, skip the final flush. Alternatively, track `w.lastFlushedRecordCount` and only flush if new records exist.

2. **go-evtx module version bump**
   - What we know: go-evtx v0.1.0 is published. Phase 9 changes the `New()` signature (adds `RotationConfig`). This is a breaking API change.
   - What's unclear: Should Phase 9 publish v0.2.0, or work from a local `replace` directive until Phase 10 is also ready?
   - Recommendation: Use `replace` directive in cee-exporter's `go.mod` during development, then publish v0.2.0 when Phase 9 is complete and tested.

3. **Backward compatibility of `RotationConfig{}`**
   - What we know: The zero value `RotationConfig{}` has `FlushIntervalSec = 0`. With the `if cfg.FlushIntervalSec > 0` guard, the goroutine is not started. This is backward-compatible for `writer_native_notwindows.go` callers.
   - What's unclear: Should the go-evtx `New()` function accept `RotationConfig` as a required parameter or as a variadic `...RotationConfig`? Variadic allows existing callers to compile unchanged; required parameter makes the API explicit.
   - Recommendation: Use required parameter — the signature change is a v0.2.0 breaking change anyway; explicit is better for a library API.

4. **ADR numbering**
   - What we know: Existing ADRs go up to ADR-011. The next available numbers are ADR-012 and ADR-013.
   - What's unclear: Whether ADR-01/ADR-02 in the requirements refer to the REQUIREMENTS.md abstract names or the actual doc numbers.
   - Recommendation: Name the files `ADR-012-flush-ticker-ownership.md` and `ADR-013-write-on-close-model.md`. Reference them as ADR-01/ADR-02 in REQUIREMENTS.md (abstract IDs) and ADR-012/ADR-013 in the filesystem.

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing package (stdlib), go 1.24 |
| Config file | None — go test ./... in each module |
| Quick run command | `cd ~/Projects/go-evtx && go test ./...` |
| Full suite command | `cd ~/Projects/go-evtx && go test -race ./...` (requires CGO=1) |
| cee-exporter quick | `cd ~/Projects/cee-exporter && make test` |
| cee-exporter race | `cd ~/Projects/cee-exporter && go test -race ./...` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| FLUSH-01 | `FlushIntervalSec` triggers periodic `flushToFile()` calls | unit | `cd ~/Projects/go-evtx && go test -run TestWriter_BackgroundFlush -v ./...` | ❌ Wave 0 |
| FLUSH-01 | Goroutine does not start when `FlushIntervalSec == 0` | unit | `cd ~/Projects/go-evtx && go test -run TestWriter_NoGoroutine_WhenDisabled -v ./...` | ❌ Wave 0 |
| FLUSH-01 | Zero `FlushIntervalSec` does not panic (`time.NewTicker(0)` guard) | unit | `cd ~/Projects/go-evtx && go test -run TestWriter_ZeroInterval_NoGoroutine -v ./...` | ❌ Wave 0 |
| FLUSH-02 | `Close()` flushes remaining records before returning | unit | `cd ~/Projects/go-evtx && go test -run TestWriter_CloseFlushesRemaining -v ./...` | ❌ Wave 0 |
| FLUSH-02 | No goroutine leak after `Close()` | race | `cd ~/Projects/go-evtx && go test -race -run TestWriter_NoGoroutineLeak ./...` | ❌ Wave 0 |
| FLUSH-01+02 | Concurrent `WriteRecord` and background goroutine have no data races | race | `cd ~/Projects/go-evtx && go test -race -run TestWriter_BackgroundFlush_NoRace ./...` | ❌ Wave 0 |
| FLUSH-01 | `FlushIntervalSec` flows from cee-exporter config to `go-evtx` | integration | `cd ~/Projects/cee-exporter && go test -run TestNewBinaryEvtxWriter_Config ./pkg/evtx/` | ❌ Wave 0 |
| ADR-01 | ADR-012 file exists and documents ticker ownership decision | manual | `ls ~/Projects/cee-exporter/docs/adr/ADR-012-*.md` | ❌ Wave 0 |
| ADR-02 | ADR-013 file exists and documents write-on-close model | manual | `ls ~/Projects/cee-exporter/docs/adr/ADR-013-*.md` | ❌ Wave 0 |

### Sampling Rate

- **Per task commit:** `cd ~/Projects/go-evtx && go test ./...` (no CGO; baseline correctness)
- **Per wave merge:** `cd ~/Projects/go-evtx && go test -race ./...` + `cd ~/Projects/cee-exporter && make test`
- **Phase gate:** Both suites green + race-clean before `/gsd:verify-work`

### Wave 0 Gaps

- [ ] `~/Projects/go-evtx/goroutine_test.go` — covers FLUSH-01 background goroutine tests (TestWriter_BackgroundFlush, TestWriter_NoGoroutine_WhenDisabled, TestWriter_ZeroInterval_NoGoroutine, TestWriter_BackgroundFlush_NoRace)
- [ ] `~/Projects/go-evtx/goroutine_test.go` — covers FLUSH-02 shutdown tests (TestWriter_CloseFlushesRemaining, TestWriter_NoGoroutineLeak)
- [ ] `~/Projects/cee-exporter/pkg/evtx/writer_evtx_notwindows_test.go` — covers FLUSH-01 config passthrough (TestNewBinaryEvtxWriter_Config)
- [ ] `~/Projects/cee-exporter/docs/adr/ADR-012-flush-ticker-ownership.md` — ADR-01 deliverable
- [ ] `~/Projects/cee-exporter/docs/adr/ADR-013-write-on-close-model.md` — ADR-02 deliverable

---

## Sources

### Primary (HIGH confidence)

- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/evtx.go` — current Writer struct, `New()`, `WriteRecord()`, `WriteRaw()`, `Close()`, `flushToFile()` — confirmed write-on-close model
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/binformat.go` — `evtxChunkSize=65536`, `evtxFileHeaderSize=4096`, `evtxChunkHeaderSize=512`, CRC field offsets
- Direct source inspection: `/Users/fjacquet/Projects/go-evtx/binxml.go` — `evtxRecordsStart=512`, `evtxRecordHeaderSize=24`, `chunkFlushThreshold=60000`
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/pkg/evtx/writer_evtx_notwindows.go` — current adapter pattern; confirms single-arg `goevtx.New(evtxPath)`
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/pkg/queue/queue.go` — `Stop()` calls `writer.Close()`; shutdown chain confirmed
- Direct source inspection: `/Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/main.go` — `buildWriter()`, `OutputConfig`, `defaultConfig()`, signal handling; confirms `q.Stop()` is the shutdown trigger
- [pkg.go.dev/sync#WaitGroup](https://pkg.go.dev/sync#WaitGroup) — WaitGroup documentation
- [pkg.go.dev/time#NewTicker](https://pkg.go.dev/time#NewTicker) — "The duration d must be greater than zero; if not, NewTicker will panic"
- [pkg.go.dev/os#File.Sync](https://pkg.go.dev/os#File.Sync) — maps to `fsync(2)` / `F_FULLFSYNC`; returns error
- Go issue #68483 — ticker goroutine leak; `ticker.Stop()` does not close channel

### Secondary (MEDIUM confidence)

- [blogtitle.github.io — Go advanced concurrency patterns: timers](https://blogtitle.github.io/go-advanced-concurrency-patterns-part-2-timers/) — ticker goroutine leak patterns
- `.planning/research/SUMMARY.md` — prior milestone research documenting done+WaitGroup pattern and nine pitfalls

### Tertiary (LOW confidence)

- None — all findings for Phase 9 are grounded in direct code inspection and official stdlib documentation.

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — all stdlib; go-evtx source confirms no existing goroutine to conflict with
- Architecture: HIGH — based on direct code inspection of every relevant file; no assumptions
- Pitfalls: HIGH — concurrency pitfalls derived from Go issue tracker and direct WaitGroup/ticker docs; checkpoint-write semantics derived from `os.WriteFile` source behavior

**Research date:** 2026-03-04
**Valid until:** 2026-04-04 (stable domain — Go stdlib goroutine patterns do not change between versions)
