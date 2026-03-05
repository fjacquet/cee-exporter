# Phase 12: Config, Validation, Prometheus and Docs - Research

**Researched:** 2026-03-05
**Domain:** Go configuration validation, Prometheus gauge exposition, TOML documentation
**Confidence:** HIGH

---

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| FLUSH-03 | Prometheus `/metrics` endpoint exposes a `cee_last_fsync_unix_seconds` gauge so SREs can alert when fsync has not occurred within the expected interval | Prometheus `GaugeFunc` pattern already used in handler.go; `atomic.Int64` in metrics.Store is the correct propagation path; fsync happens inside `flushChunkLocked()` and `tickFlushLocked()` in go-evtx |
| CFG-01 | All flush and rotation parameters (`flush_interval_s`, `max_file_size_mb`, `max_file_count`, `rotation_interval_h`) are configurable in the `[output]` section of config.toml with documented zero-value semantics | All four fields already exist in `OutputConfig` with correct `toml:` tags and in-code comments; config.toml file currently lacks these four lines in the `[output]` block |
| CFG-02 | cee-exporter rejects invalid configuration (e.g., `flush_interval_s = 0`) at startup with a clear error message rather than panicking at runtime | go-evtx `New()` already rejects `FlushIntervalSec < 0`; CFG-02 per spec says `flush_interval_s = 0` is the invalid case — but existing comments say `0 = disabled`; resolution documented below |
| CFG-03 | `config.toml.example` is updated to document all four new `[output]` fields with inline comments explaining default values and zero-value semantics | `config.toml.example` exists and lacks the four EVTX rotation/flush fields; must add them in commented form with documentation |
</phase_requirements>

---

## Summary

Phase 12 is the final wire-up and documentation phase for the v4.0 milestone. Three previous phases (9, 10, 11) built all the runtime machinery into go-evtx. Phase 12's job is entirely within cee-exporter: expose the fsync timestamp via Prometheus, add startup validation for the EVTX output configuration, and document the four new `[output]` fields in both `config.toml` and `config.toml.example`.

The codebase is in a very advanced state. All four `OutputConfig` fields (`FlushIntervalSec`, `MaxFileSizeMB`, `MaxFileCount`, `RotationIntervalH`) are already defined, already have `toml:` tags, and already flow through `buildWriter()` to `goevtx.RotationConfig`. The Prometheus handler already uses the `prometheus/client_golang` library with a private registry and the `GaugeFunc`/`CounterFunc` pattern. The `metrics.Store` singleton already stores gauge values as `atomic.Int64`.

The primary design work for this phase is answering: where does the fsync timestamp get recorded, and how does it flow from go-evtx (where `f.Sync()` is called) up to the Prometheus handler?

**Primary recommendation:** Record the fsync timestamp in `metrics.Store` (add a `lastFsyncAt atomic.Int64` field). Update go-evtx to accept an optional callback (`OnFsync func(time.Time)`) in `RotationConfig`, or record directly via a package-level hook. Expose it in the Prometheus handler as a `GaugeFunc` returning `float64(unix seconds)`. Add startup validation in `run()` after config is parsed, before `buildWriter()` is called. Update both config files.

---

## Standard Stack

### Core (already in place — no new dependencies)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/prometheus/client_golang` | v1.23.2 | Prometheus metrics exposition | Already in go.mod; private registry pattern already in use in `pkg/prometheus/handler.go` |
| `github.com/BurntSushi/toml` | v1.6.0 | TOML config parsing | Already in use; `toml:` tags on all config structs |
| `github.com/fjacquet/go-evtx` | v0.4.0 (local replace) | EVTX writer with RotationConfig | All flush/rotation machinery lives here; fsync called in `flushChunkLocked()` and `tickFlushLocked()` |
| `sync/atomic` (stdlib) | Go 1.24 | Thread-safe counter/gauge storage | Already the pattern in `metrics.Store`; `atomic.Int64` for Unix nanosecond timestamps |

### No New Dependencies Required

This phase adds zero new `go.mod` entries. The Prometheus client library is already imported. The atomic metrics store already holds gauge values. All work is connecting existing pieces.

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `atomic.Int64` in metrics.Store for fsync time | Channel/callback from go-evtx | Callback adds coupling; atomic pull avoids go-evtx needing to import cee-exporter's metrics package |
| `OnFsync func(time.Time)` callback in RotationConfig | Global hook in go-evtx | Callback is cleaner API design; global hook is simpler but pollutes library semantics |
| `GaugeFunc` reading atomic | Push-style `Gauge.Set()` | `GaugeFunc` is pull-based, consistent with existing `CounterFunc`/`GaugeFunc` pattern in handler.go |

---

## Architecture Patterns

### Current Prometheus Handler Pattern (handler.go)

All existing metrics use pull-based `GaugeFunc`/`CounterFunc` that read from `metrics.M`:

```go
// Source: pkg/prometheus/handler.go (lines 44-50)
prometheus.NewGaugeFunc(
    prometheus.GaugeOpts{
        Name: "cee_queue_depth",
        Help: "Current number of events waiting in the async queue.",
    },
    func() float64 { return float64(metrics.M.QueueDepth()) },
),
```

The `cee_last_fsync_unix_seconds` gauge MUST follow this same pattern.

### Existing metrics.Store Pattern

```go
// Source: pkg/metrics/metrics.go (lines 23-26)
// Current gauges use atomic.Int64 with accessor methods:
lastEventAt atomic.Int64 // Unix nanoseconds

func (s *Store) RecordEventAt() {
    s.lastEventAt.Store(time.Now().UnixNano())
}
func (s *Store) LastEventAt() time.Time { ... }
```

The `lastFsyncAt` field follows exactly this pattern.

### Fsync Timestamp Propagation Options

**Option A (RECOMMENDED): OnFsync callback in RotationConfig**

Add `OnFsync func(time.Time)` to `goevtx.RotationConfig`. After each successful `f.Sync()` call in both `flushChunkLocked()` and `tickFlushLocked()`, call `w.cfg.OnFsync(time.Now())` if non-nil. In `buildWriter()`, pass `metrics.M.RecordFsyncAt` as the callback.

Advantages:
- go-evtx remains a library with no knowledge of cee-exporter's metrics package
- Zero overhead if callback is nil (GELF/other writers)
- Testable: tests can pass their own callback

**Option B: Direct metrics.Store update from BinaryEvtxWriter**

`BinaryEvtxWriter` in cee-exporter wraps the go-evtx Writer. Since the adapter controls the call, it could intercept... but it cannot — `WriteEvent` delegates to `b.w.WriteRecord()` and the fsync happens inside go-evtx, invisible to the adapter.

**Option C: Polling approach**

Expose `LastFsync() time.Time` on `goevtx.Writer`. Have the Prometheus handler call `w.LastFsync()` at scrape time (type-assert the evtx.Writer to check for this interface). This avoids modifying RotationConfig but requires the Prometheus handler to know about the writer.

**Verdict:** Option A (callback) is cleanest. Option C (interface polling at scrape time) is acceptable if modifying go-evtx is undesirable. Both are workable; the planner should choose based on whether a go-evtx v0.5.0 bump is acceptable.

### Startup Validation Pattern

Validation should live in a `validateOutputConfig(cfg OutputConfig) error` function called from `run()` immediately after config is parsed, before `buildWriter()`:

```go
// Location: cmd/cee-exporter/main.go, called after migrateListenConfig()
if err := validateOutputConfig(cfg.Output); err != nil {
    fmt.Fprintf(os.Stderr, "config error: %v\n", err)
    os.Exit(1)
}
```

**What is "invalid" for CFG-02?**

The requirement says: `flush_interval_s = 0` is the example of an invalid config. However, the existing code comment says `0 = disabled`. These are in direct conflict.

**Resolution based on code evidence:**
- `go-evtx New()` at line 86-88 only rejects `FlushIntervalSec < 0`; it explicitly allows 0 (disables the goroutine)
- `defaultConfig()` sets `FlushIntervalSec: 15` — meaning 0 means "operator explicitly disabled it"
- The REQUIREMENTS.md CFG-02 entry says `flush_interval_s = 0` is the example invalid case

**Recommended interpretation:** CFG-02's example is illustrative, not definitive. Validate that:
- When `type = "evtx"`, `flush_interval_s` must be > 0 (disabling the flush goroutine on an EVTX writer is operationally dangerous — data loss up to full session on crash)
- `max_file_size_mb`, `max_file_count`, `RotationIntervalH` being 0 are explicitly valid (unlimited/disabled semantics)
- Negative values for any of the four fields are invalid (go-evtx already rejects them, but fail early with a better message)
- `evtx_path` must be non-empty when `type = "evtx"` (go-evtx rejects empty path; validate earlier with a better message)

This interpretation satisfies CFG-02's intent ("rejects invalid configuration at startup with clear error") without contradicting the existing `0 = disabled` documentation.

### Recommended Project Structure Changes

```
pkg/metrics/metrics.go           # Add lastFsyncAt atomic.Int64 + RecordFsyncAt() + LastFsyncUnix()
pkg/prometheus/handler.go        # Add cee_last_fsync_unix_seconds GaugeFunc
cmd/cee-exporter/main.go         # Add validateOutputConfig(), pass OnFsync callback to buildWriter()
config.toml                      # Add four [output] lines (flush_interval_s, max_file_size_mb, etc.)
config.toml.example              # Add four [output] commented lines with documentation
~/Projects/go-evtx/evtx.go      # Add OnFsync func(time.Time) to RotationConfig; call after Sync()
```

### Anti-Patterns to Avoid

- **Do NOT add `Flush()` to the `evtx.Writer` interface.** ADR-01 explicitly documents this decision; the Writer interface has only `WriteEvent` and `Close`.
- **Do NOT use prometheus.DefaultRegisterer.** The handler uses a private registry; adding the new gauge must use the same `reg` variable inside `NewMetricsHandler()`.
- **Do NOT import cee-exporter packages from go-evtx.** That would create a circular dependency.
- **Do NOT validate configuration with `log.Fatal` or `panic`.** Use `fmt.Fprintf(os.Stderr, ...)` + `os.Exit(1)` consistently with existing patterns in `run()`.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Prometheus text format | Custom `/metrics` formatter | `promhttp.HandlerFor` with private registry | Already in use; Prometheus text format has edge cases (type headers, help text, escaping) |
| Thread-safe timestamp storage | Mutex-guarded `time.Time` | `atomic.Int64` (Unix nanoseconds) | Already the `lastEventAt` pattern in metrics.Store; zero allocation, no lock contention |
| TOML parsing | Custom string splitter | `github.com/BurntSushi/toml` with struct tags | Already in use; zero-value semantics work automatically |

---

## Common Pitfalls

### Pitfall 1: Prometheus Counter vs Gauge for Unix Timestamp

**What goes wrong:** Using a `CounterFunc` for the fsync timestamp — counters can only increase, so a wrap-around or reset would be invisible/confusing.

**How to avoid:** Use `GaugeFunc`. A Unix timestamp in seconds as a Gauge is the standard Prometheus pattern for "last occurrence" metrics. Alert rule: `time() - cee_last_fsync_unix_seconds > 60` means "no fsync in 60 seconds."

**Warning sign:** If the Prometheus help text says `# TYPE cee_last_fsync_unix_seconds counter`, the wrong type was used.

### Pitfall 2: Nanoseconds vs Seconds in the Gauge

**What goes wrong:** Storing Unix nanoseconds in the `atomic.Int64` (correct for internal storage) but exposing nanoseconds to Prometheus (wrong — Prometheus convention for Unix timestamps is seconds).

**How to avoid:** `LastFsyncUnix()` accessor returns `float64(ns / 1e9)` (integer division is acceptable; subsecond precision not needed for alert purposes). Alternatively store as Unix seconds directly in the atomic.

**Warning sign:** Alert rule `time() - cee_last_fsync_unix_seconds > 60` evaluating to a number in the billions range.

### Pitfall 3: fsync Timestamp Never Updated (Zero Value)

**What goes wrong:** If no events are written, or if `type != "evtx"`, `lastFsyncAt` stays 0. An SRE alert on `cee_last_fsync_unix_seconds` would immediately fire even on a freshly-started daemon with no events.

**How to avoid:** Document the zero-value semantics in metric HELP text: "Unix timestamp of the last successful fsync. 0 = no fsync has occurred yet." SRE alert should be conditioned on `cee_last_fsync_unix_seconds > 0 and time() - cee_last_fsync_unix_seconds > N`.

### Pitfall 4: Validation Before Writer Construction

**What goes wrong:** Returning a runtime error from `goevtx.New()` (e.g., negative FlushIntervalSec) rather than catching it at startup.

**How to avoid:** `validateOutputConfig()` runs before `buildWriter()`. Validate all four fields with clear messages. `buildWriter()` error path remains as a safety net but should not be the first line of defense.

### Pitfall 5: config.toml vs config.toml.example Drift

**What goes wrong:** Updating `config.toml.example` but not `config.toml` (or vice versa), leaving operators with mismatched documentation.

**How to avoid:** Both files must be updated in the same plan wave. The four fields in `config.toml` should appear as active (not commented) values for the EVTX section since `config.toml` is the live operational config, while `config.toml.example` uses commented-out defaults.

---

## Code Examples

Verified patterns from existing codebase:

### Adding a Gauge to the Prometheus Handler

```go
// Source: pkg/prometheus/handler.go (existing pattern, lines 44-50)
// Add inside NewMetricsHandler() alongside existing registrations:
reg.MustRegister(
    prometheus.NewGaugeFunc(
        prometheus.GaugeOpts{
            Name: "cee_last_fsync_unix_seconds",
            Help: "Unix timestamp of the last successful fsync to the EVTX file. " +
                "0 = no fsync has occurred yet. Alert when time()-this > flush_interval_s*2.",
        },
        func() float64 { return float64(metrics.M.LastFsyncUnix()) },
    ),
)
```

### Adding Atomic Gauge to metrics.Store

```go
// Source: pkg/metrics/metrics.go (follow existing lastEventAt pattern, lines 23-50)
// Field addition:
lastFsyncAt atomic.Int64 // Unix seconds (not nanoseconds — Prometheus convention)

// Setter (called from go-evtx OnFsync callback):
func (s *Store) RecordFsyncAt(t time.Time) {
    s.lastFsyncAt.Store(t.Unix()) // seconds, not nanoseconds
}

// Getter for Prometheus GaugeFunc:
func (s *Store) LastFsyncUnix() int64 {
    return s.lastFsyncAt.Load()
}
```

### RotationConfig OnFsync Callback (go-evtx change)

```go
// Source: go-evtx/evtx.go — extend RotationConfig struct
type RotationConfig struct {
    FlushIntervalSec  int
    MaxFileSizeMB     int
    MaxFileCount      int
    RotationIntervalH int
    // OnFsync is called after each successful f.Sync() with the time of the sync.
    // nil = no callback. Only applies when FlushIntervalSec > 0.
    OnFsync func(time.Time)
}

// In flushChunkLocked(), after the f.Sync() call:
if err := w.f.Sync(); err != nil {
    return fmt.Errorf("go_evtx: sync: %w", err)
}
if w.cfg.OnFsync != nil {
    w.cfg.OnFsync(time.Now())
}

// In tickFlushLocked(), same pattern after its f.Sync() call.
```

### Wiring OnFsync in buildWriter()

```go
// Source: cmd/cee-exporter/main.go buildWriter() case "evtx"
case "evtx":
    w, err := evtx.NewNativeEvtxWriter(cfg.EVTXPath, goevtx.RotationConfig{
        FlushIntervalSec:  cfg.FlushIntervalSec,
        MaxFileSizeMB:     cfg.MaxFileSizeMB,
        MaxFileCount:      cfg.MaxFileCount,
        RotationIntervalH: cfg.RotationIntervalH,
        OnFsync:           metrics.M.RecordFsyncAt, // NEW: wire fsync timestamp
    })
    return w, cfg.EVTXPath, err
```

### Startup Validation Function

```go
// cmd/cee-exporter/main.go — new function, called in run() after migrateListenConfig()
func validateOutputConfig(cfg OutputConfig) error {
    if cfg.Type == "evtx" {
        if cfg.EVTXPath == "" {
            return fmt.Errorf("[output] evtx_path must be set when type = \"evtx\"")
        }
        if cfg.FlushIntervalSec == 0 {
            return fmt.Errorf("[output] flush_interval_s = 0 disables periodic fsync; " +
                "set to a positive value (default: 15) when type = \"evtx\" to bound data loss on crash")
        }
        if cfg.FlushIntervalSec < 0 {
            return fmt.Errorf("[output] flush_interval_s must be > 0, got %d", cfg.FlushIntervalSec)
        }
        if cfg.MaxFileSizeMB < 0 {
            return fmt.Errorf("[output] max_file_size_mb must be >= 0, got %d", cfg.MaxFileSizeMB)
        }
        if cfg.MaxFileCount < 0 {
            return fmt.Errorf("[output] max_file_count must be >= 0, got %d", cfg.MaxFileCount)
        }
        if cfg.RotationIntervalH < 0 {
            return fmt.Errorf("[output] rotation_interval_h must be >= 0, got %d", cfg.RotationIntervalH)
        }
    }
    return nil
}
```

### config.toml.example EVTX Section (new content to add)

```toml
# EVTX durability and rotation (applies only when type = "evtx")
#
# flush_interval_s — Interval in seconds between periodic checkpoint writes (fsync).
#   Must be > 0 when type = "evtx" (0 disables fsync and risks data loss on crash).
#   Default: 15  (bound data loss to at most 15 seconds on power failure)
# flush_interval_s = 15
#
# max_file_size_mb — Rotate the active .evtx file when it reaches this size in MiB.
#   0 = unlimited (file grows without bound).
#   Default: 0
# max_file_size_mb = 0
#
# max_file_count — Keep only the N most recent archive .evtx files; delete older ones.
#   0 = unlimited (all archives are kept).
#   Default: 0
# max_file_count = 0
#
# rotation_interval_h — Rotate the active .evtx file every N hours regardless of size.
#   0 = disabled (no time-based rotation).
#   Default: 0
#   Send SIGHUP to trigger an immediate rotation at any time.
# rotation_interval_h = 0
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| No Prometheus metrics | Private registry with CounterFunc/GaugeFunc | Phase 4 | Private registry excludes Go runtime noise from scrape |
| Write-on-Close EVTX | Open-handle incremental flush with periodic fsync | Phase 10 | Enables crash recovery; makes fsync timestamp meaningful |
| No rotation | Size/time/count/SIGHUP rotation via go-evtx | Phase 11 | Bounded file growth; archiving |
| FlushIntervalSec only | Full RotationConfig (4 fields) | Phase 11 | All rotation parameters flow end-to-end |

**Key design decision already made (ADR-01):** Flush ticker is owned inside `BinaryEvtxWriter` (go-evtx layer), not in the queue layer. The `Writer` interface does NOT have a `Flush()` method. This is locked — do not revisit.

---

## Open Questions

1. **go-evtx version bump required?**
   - What we know: Adding `OnFsync` to `RotationConfig` requires modifying go-evtx, which would be tagged v0.5.0
   - What's unclear: Whether the planner wants to use Option A (callback in go-evtx v0.5.0) or Option C (interface polling in cee-exporter without touching go-evtx)
   - Recommendation: Option A is architecturally cleaner; plan for a go-evtx v0.5.0 bump in Plan 12-01, cee-exporter wiring in Plan 12-02

2. **CFG-02 zero value semantics conflict**
   - What we know: REQUIREMENTS.md says `flush_interval_s = 0` is "invalid"; code comments say `0 = disabled`
   - What's unclear: Whether the product intent is to allow operators to disable fsync (dangerous but valid) or to reject it
   - Recommendation: Treat `flush_interval_s = 0` as invalid when `type = "evtx"` (validation is conditional on output type); GELF operators setting `flush_interval_s = 0` are not affected since the field is documented as "only applies when type = evtx"

3. **Should `config.toml` (live config) show the four fields as active or commented?**
   - What we know: `config.toml` currently uses `type = "evtx"` and `evtx_path = "/tmp/audit.evtx"` but no rotation fields
   - Recommendation: Add the four fields as active (uncommented) values in `config.toml` with their defaults, so operators can see and edit them immediately; keep them commented in `config.toml.example` to show they are optional

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` package (no testify) |
| Config file | none (project uses `make test` = `go test ./...`) |
| Quick run command | `go test ./pkg/prometheus/ ./pkg/metrics/ ./cmd/cee-exporter/ -count=1` |
| Full suite command | `make test` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| FLUSH-03 | `cee_last_fsync_unix_seconds` appears in /metrics scrape output | unit | `go test ./pkg/prometheus/ -run TestMetricsHandler_AllRequiredMetrics -count=1` | ✅ (handler_test.go exists; test must be extended) |
| FLUSH-03 | Gauge value updates after `RecordFsyncAt()` is called | unit | `go test ./pkg/metrics/ -run TestStore_LastFsyncUnix -count=1` | ❌ Wave 0 |
| CFG-02 | `validateOutputConfig` returns error for `flush_interval_s = 0` with `type = "evtx"` | unit | `go test ./cmd/cee-exporter/ -run TestValidateOutputConfig -count=1` | ❌ Wave 0 |
| CFG-02 | `validateOutputConfig` returns error for negative field values | unit | `go test ./cmd/cee-exporter/ -run TestValidateOutputConfig -count=1` | ❌ Wave 0 |
| CFG-02 | `validateOutputConfig` returns nil for valid EVTX config | unit | `go test ./cmd/cee-exporter/ -run TestValidateOutputConfig -count=1` | ❌ Wave 0 |
| CFG-01 | All four OutputConfig fields have correct toml tags and default values | unit | `go test ./cmd/cee-exporter/ -run TestDefaultConfig -count=1` | ❌ Wave 0 (or manual review) |
| CFG-03 | config.toml.example contains all four field names | manual-only | N/A — doc file review | N/A |

### Sampling Rate

- **Per task commit:** `go test ./pkg/prometheus/ ./pkg/metrics/ ./cmd/cee-exporter/ -count=1`
- **Per wave merge:** `make test` (all 9 packages)
- **Phase gate:** Full suite green before `/gsd:verify-work`

### Wave 0 Gaps

- [ ] `pkg/metrics/metrics_test.go` (if not already present) — add `TestStore_LastFsyncUnix` covering REQ FLUSH-03
- [ ] `cmd/cee-exporter/validate_test.go` — add `TestValidateOutputConfig` covering REQ CFG-02
- [ ] Extend `pkg/prometheus/handler_test.go` `TestMetricsHandler_AllRequiredMetrics` to assert `cee_last_fsync_unix_seconds` appears in scrape output

*(If gap check confirms `pkg/metrics/metrics_test.go` exists, check whether it covers the new field.)*

---

## Sources

### Primary (HIGH confidence)

- Direct code reading — `pkg/prometheus/handler.go` (all 79 lines), confirmed pattern for adding gauges
- Direct code reading — `pkg/metrics/metrics.go` (all 73 lines), confirmed `atomic.Int64` + accessor pattern
- Direct code reading — `cmd/cee-exporter/main.go` (all 403 lines), confirmed `OutputConfig` struct, `defaultConfig()`, `buildWriter()`, `run()` flow
- Direct code reading — `/Users/fjacquet/Projects/go-evtx/evtx.go` (all 540 lines), confirmed `f.Sync()` location in `flushChunkLocked()` (line 414) and `tickFlushLocked()` (line 472), `RotationConfig` struct (lines 52-57)
- Direct code reading — `go.mod` — confirmed `prometheus/client_golang v1.23.2` is already a dependency; no new dependencies needed
- Direct code reading — `config.toml.example` — confirmed the four EVTX fields are absent; file exists

### Secondary (MEDIUM confidence)

- Prometheus naming convention for Unix timestamp gauges: `_unix_seconds` suffix (consistent with `node_time_seconds`, `process_start_time_seconds` in the Prometheus ecosystem)
- Prometheus alert pattern for "last occurrence": `time() - metric_name > threshold_seconds`

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — all libraries already in go.mod; confirmed via file reads
- Architecture: HIGH — all patterns verified directly from source code; no guessing
- Pitfalls: HIGH — derived from direct code analysis (e.g., nanoseconds vs seconds issue, zero-value gauge problem)
- CFG-02 interpretation: MEDIUM — conflict between REQUIREMENTS.md example and code comments; resolution is a judgment call documented above

**Research date:** 2026-03-05
**Valid until:** 2026-04-05 (stable — all dependencies pinned, go-evtx local replace)
