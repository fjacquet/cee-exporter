# State: cee-exporter

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-02)

**Core value:** Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.
**Current focus:** Milestone v1.0 initialization

## Current Position

Phase: Not started (defining requirements)
Plan: —
Status: Defining requirements
Last activity: 2026-03-02 — Milestone v1.0 started

## Key Decisions Made

- GELF selected as primary Linux output (replaces BinXML v1 plan) — simpler, no agent, direct Graylog integration
- BinaryEvtxWriter deferred to v2 — GELF covers the Graylog use case
- MultiWriter interface added — fan-out to multiple backends
- Core pipeline already implemented outside GSD structure (parser, mapper, queue, writers, server, main)

## Blockers / Concerns

- `readBody` nil ResponseWriter bug — will panic on >64 MiB payload (QUAL-05)
- Win32 EventID registration: `InstallAsEventCreate` only covers IDs 1-1000; IDs 4663/4660/4670 need proper message DLL for Event Viewer display

## Accumulated Context

### What Was Built (2026-03-02, pre-roadmap)

All core pipeline code was implemented in a single session:
- `pkg/parser` — CEE XML → `[]CEPAEvent` (single + VCAPS)
- `pkg/mapper` — CEPA type → WindowsEvent (6 event types)
- `pkg/queue` — buffered channel + worker pool
- `pkg/evtx` — Writer interface + GELFWriter + Win32Writer + MultiWriter + BinaryEvtxWriter stub
- `pkg/server` — CEPA HTTP handler + GET /health
- `pkg/log`, `pkg/metrics` — slog + atomic counters
- `cmd/cee-exporter/main.go` — TOML config wiring, graceful shutdown
- `config.toml.example`

Binary builds clean: `go build ./...` ✓, `go vet ./...` ✓

### What Remains for v1.0

- Tests (QUAL-01 through QUAL-05)
- Makefile (BUILD-01, BUILD-02)
- README + docs (DOC-01 through DOC-04)
- Fix readBody bug (QUAL-05)
- Validate Win32 EventID registration approach (WIN-01, WIN-02)

## Pending Todos

(None captured yet)
