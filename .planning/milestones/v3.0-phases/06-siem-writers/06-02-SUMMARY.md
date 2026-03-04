---
phase: 06-siem-writers
plan: "02"
subsystem: evtx-writers
tags: [beats, lumberjack, tls, tdd, go-lumber, writer]
dependency_graph:
  requires: []
  provides: [BeatsWriter, buildBeatsEvent, BeatsConfig, beats-transport]
  affects: [pkg/evtx, go.mod]
tech_stack:
  added: [github.com/elastic/go-lumber v0.1.1]
  patterns: [TDD-red-green-refactor, SyncDialWith-TLS-injection, sync.Mutex-thread-safety, reconnect-retry-once]
key_files:
  created:
    - pkg/evtx/writer_beats.go
    - pkg/evtx/writer_beats_test.go
  modified:
    - go.mod
    - go.sum
decisions:
  - "go-lumber has no TLS option — inject tls.Dialer via SyncDialWith custom dial function"
  - "sync.Mutex required because SyncClient is not thread-safe"
  - "SyncClient must be closed and recreated on error — cannot reuse after failure"
  - "Default port 5044 matches standard Beats Input convention"
metrics:
  duration: "3 min"
  completed: "2026-03-03"
  tasks_completed: 3
  files_created: 2
  files_modified: 2
---

# Phase 6 Plan 02: BeatsWriter — Lumberjack v2 Transport Summary

**One-liner:** Lumberjack v2 BeatsWriter with SyncDialWith TLS injection and mutex-serialized SyncClient reconnection.

## What Was Built

A `BeatsWriter` struct in `pkg/evtx/writer_beats.go` that implements the `evtx.Writer` interface and forwards CEPA audit events to Logstash or Graylog Beats Input using the Lumberjack v2 protocol.

Key design decisions driven by go-lumber's API constraints:

- **TLS injection:** go-lumber has no TLS option — TLS is implemented by passing a `tls.Dialer`-backed function to `SyncDialWith`
- **Thread safety:** `SyncClient` is documented as not thread-safe — a `sync.Mutex` serializes every `Send` call
- **Reconnect:** `SyncClient` cannot recover from errors — on send failure, `Close()` + `dial()` + retry once mirrors the `GELFWriter` pattern

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | RED — failing tests for BeatsWriter | 441a0a7 | pkg/evtx/writer_beats_test.go |
| 2 | GREEN — implement BeatsWriter | ca55154 | pkg/evtx/writer_beats.go, go.mod, go.sum |
| 3 | REFACTOR — full suite green, lint clean | (no changes needed) | — |

## Artifacts Produced

### pkg/evtx/writer_beats.go (130 lines)

- `BeatsConfig` struct: Host, Port (default 5044), TLS
- `BeatsWriter` struct: cfg, sync.Mutex, *lumberv2.SyncClient
- `NewBeatsWriter(cfg BeatsConfig) (*BeatsWriter, error)`: dials and logs ready
- `dial() error`: plain TCP via `SyncDial`, TLS via `SyncDialWith` + `tls.Dialer`
- `WriteEvent(ctx context.Context, e WindowsEvent) error`: mutex-protected Send with reconnect-retry
- `Close() error`: mutex-protected client close
- `buildBeatsEvent(e WindowsEvent) map[string]interface{}`: maps all 15 audit fields

### pkg/evtx/writer_beats_test.go (153 lines)

- `TestBuildBeatsEvent`: 8 sub-tests validating @timestamp (RFC3339Nano), message, event_id, user, object_name, cepa_event_type, client_address
- `TestBeatsWriterDialerInjection`: plain TCP and TLS dial paths both return errors on unreachable address (no panic, correct code path)

## Verification Results

All plan verification checks passed:

1. `go test ./pkg/evtx/ -run "TestBuildBeatsEvent|TestBeatsWriterDialerInjection" -v` — PASS (10 sub-tests)
2. `go test ./...` — 59 tests across 9 packages, all pass
3. `make build` — Linux amd64 binary built (CGO_ENABLED=0)
4. `make build-windows` — Windows amd64 binary built (CGO_ENABLED=0)
5. `make lint` — go vet: no issues
6. `grep "elastic/go-lumber" go.mod` — v0.1.1 present
7. `grep "tls.Dialer" writer_beats.go` — TLS injection present
8. `grep "SyncDialWith" writer_beats.go` — TLS path uses SyncDialWith
9. `grep "w.mu.Lock" writer_beats.go` — mutex protecting Send present

## Deviations from Plan

None — plan executed exactly as written.

## Decisions Made

1. **go-lumber TLS injection via SyncDialWith:** go-lumber v0.1.1 has no TLS Option — the only way to enable TLS is to inject a custom dial function via `SyncDialWith`. Used `tls.Dialer` with `MinVersion: tls.VersionTLS12` per plan specification.

2. **sync.Mutex wrapping every Send:** SyncClient documentation states it is not thread-safe. `w.mu.Lock()` wraps every `Send` call to serialize concurrent `WriteEvent` calls from queue worker goroutines.

3. **Reconnect pattern:** `SyncClient` cannot recover after error (connection state is invalid). On failure: `w.client.Close()` → `w.dial()` → retry `Send` once. Mirrors `GELFWriter.connect()` pattern for consistency.

## Requirements Satisfied

- **OUT-01:** Beats/Lumberjack v2 transport to Logstash or Graylog Beats Input
- **OUT-02:** TLS for Beats transport (MinVersion TLS 1.2)
