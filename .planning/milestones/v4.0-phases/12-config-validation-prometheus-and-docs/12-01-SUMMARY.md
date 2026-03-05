---
phase: 12-config-validation-prometheus-and-docs
plan: "01"
subsystem: observability, config-validation, go-evtx
tags:
  - prometheus
  - fsync
  - config-validation
  - go-evtx
  - metrics
dependency_graph:
  requires:
    - go-evtx v0.4.0 (RotationConfig, flushChunkLocked, tickFlushLocked)
    - pkg/metrics (atomic Store pattern)
    - pkg/prometheus (GaugeFunc pattern)
    - cmd/cee-exporter/main.go (buildWriter, run)
  provides:
    - go-evtx v0.5.0 (OnFsync callback)
    - pkg/metrics.RecordFsyncAt / LastFsyncUnix
    - cee_last_fsync_unix_seconds Prometheus gauge
    - validateOutputConfig() startup validation
    - config.toml EVTX rotation parameters
  affects:
    - FLUSH-03, CFG-01, CFG-02, CFG-03 requirements
tech_stack:
  added:
    - go-evtx v0.5.0 OnFsync callback field
  patterns:
    - atomic.Int64 for lock-free fsync timestamp
    - GaugeFunc closure pattern for Prometheus
    - nil-guard callback pattern for optional observability hooks
key_files:
  created:
    - /Users/fjacquet/Projects/cee-exporter/pkg/metrics/metrics_test.go
    - /Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/validate_test.go
  modified:
    - /Users/fjacquet/Projects/go-evtx/evtx.go
    - /Users/fjacquet/Projects/go-evtx/CHANGELOG.md
    - /Users/fjacquet/Projects/cee-exporter/pkg/metrics/metrics.go
    - /Users/fjacquet/Projects/cee-exporter/pkg/prometheus/handler.go
    - /Users/fjacquet/Projects/cee-exporter/pkg/prometheus/handler_test.go
    - /Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/main.go
    - /Users/fjacquet/Projects/cee-exporter/config.toml
    - /Users/fjacquet/Projects/cee-exporter/config.toml.example
decisions:
  - "OnFsync nil-guard pattern: callback fires only after successful f.Sync(), never on error"
  - "lastFsyncAt stores Unix seconds (not nanoseconds) to match Prometheus float64 convention"
  - "validateOutputConfig rejects FlushIntervalSec=0 for type=evtx with descriptive error message"
  - "validateOutputConfig is evtx-only — gelf/syslog/beats have no flush validation requirements"
metrics:
  duration_seconds: 399
  completed_date: "2026-03-05"
  tasks_completed: 3
  tasks_total: 3
  files_modified: 10
  files_created: 2
---

# Phase 12 Plan 01: Fsync Observability Gauge, Config Validation, and EVTX Config Docs Summary

**One-liner:** OnFsync callback in go-evtx v0.5.0 feeds atomic lastFsyncAt in metrics.Store, exposed as cee_last_fsync_unix_seconds Prometheus gauge, with validateOutputConfig() guarding startup correctness.

## What Was Built

### Task 1: go-evtx v0.5.0 — OnFsync callback (commit 8771ba0 in go-evtx)

Added `OnFsync func(time.Time)` field to `RotationConfig`. Both `flushChunkLocked()` and `tickFlushLocked()` call `w.cfg.OnFsync(time.Now())` after each successful `f.Sync()`, guarded by a nil check. The nil default ensures backward compatibility with v0.4.0. Tagged v0.5.0 with race detector confirmation.

### Task 2: cee-exporter — fsync gauge, validation, OnFsync wiring (commit 6637e1a)

- `pkg/metrics/metrics.go`: `lastFsyncAt atomic.Int64`, `RecordFsyncAt(time.Time)`, `LastFsyncUnix() int64`, and `LastFsyncUnix` in `Snapshot`
- `pkg/metrics/metrics_test.go`: `TestStore_LastFsyncUnix` with zero-value, round-trip, and overwrite cases
- `pkg/prometheus/handler.go`: `cee_last_fsync_unix_seconds` GaugeFunc registered in `NewMetricsHandler()`
- `pkg/prometheus/handler_test.go`: asserts `cee_last_fsync_unix_seconds` appears in scrape output
- `cmd/cee-exporter/main.go`: `validateOutputConfig()` with 6 guard rules for evtx type; called in `run()` after `migrateListenConfig`, before `buildWriter()`; `OnFsync: metrics.M.RecordFsyncAt` wired in evtx case
- `cmd/cee-exporter/validate_test.go`: `TestValidateOutputConfig` with 8 table-driven cases

### Task 3: Config files — EVTX rotation parameters (commit 34b98e8)

- `config.toml`: added active `flush_interval_s=15`, `max_file_size_mb=0`, `max_file_count=0`, `rotation_interval_h=0` to `[output]` section
- `config.toml.example`: added all four as commented-out defaults with full inline documentation explaining zero-value semantics and SIGHUP rotation trigger

## Decisions Made

| Decision | Rationale |
|---|---|
| OnFsync nil-guard in both flush paths | Backward compatible — no callback = v0.4.0 behavior |
| Unix seconds (not nanoseconds) for lastFsyncAt | Prometheus float64 convention; seconds is sufficient precision for fsync alerting |
| validateOutputConfig evtx-only | gelf/syslog/beats don't use FlushIntervalSec; validation would be misleading |
| FlushIntervalSec=0 treated as configuration error for evtx | 0 disables fsync entirely — high risk of data loss on crash; default config uses 15 |

## Verification Results

```
make test:    86 tests pass across 9 packages
make build:   Linux/amd64 binary builds successfully (CGO_ENABLED=0, static)
make build-windows: Windows/amd64 binary builds successfully
go-evtx race: go test -race ./... passes (zero races)
```

## Deviations from Plan

None — plan executed exactly as written.

## Requirements Closed

- FLUSH-03: cee_last_fsync_unix_seconds Prometheus gauge updated atomically after each successful fsync
- CFG-01: config.toml [output] section contains all four active EVTX parameters
- CFG-02: validateOutputConfig() rejects invalid evtx configs at startup
- CFG-03: config.toml.example documents all four EVTX parameters with inline comments

## Self-Check: PASSED

All files created/modified confirmed present. All commits verified in both repositories.
