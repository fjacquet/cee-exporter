# Roadmap: cee-exporter

## Milestones

- ✅ **v1.0 MVP** — Phases 1-3 (shipped 2026-03-03) — see [milestones/v1.0-ROADMAP.md](milestones/v1.0-ROADMAP.md)
- ✅ **v2.0 Operations & Output Expansion** — Phases 4-7 (shipped 2026-03-03) — see [milestones/v3.0-ROADMAP.md](milestones/v3.0-ROADMAP.md)
- ✅ **v3.0 TLS Certificate Automation** — Phase 8 (shipped 2026-03-04) — see [milestones/v3.0-ROADMAP.md](milestones/v3.0-ROADMAP.md)
- 🚧 **v4.0 Industrialisation** — Phases 9-12 (in progress)

## Phases

<details>
<summary>✅ v1.0 MVP (Phases 1-3) — SHIPPED 2026-03-03</summary>

- [x] Phase 1: Quality (3/3 plans) — completed 2026-03-02
- [x] Phase 2: Build (1/1 plan) — completed 2026-03-02
- [x] Phase 3: Documentation (2/2 plans) — completed 2026-03-03

</details>

<details>
<summary>✅ v2.0 Operations & Output Expansion (Phases 4-7) — SHIPPED 2026-03-03</summary>

- [x] Phase 4: Observability & Linux Service (3/3 plans) — completed 2026-03-03
- [x] Phase 5: Windows Service (3/3 plans) — completed 2026-03-03
- [x] Phase 6: SIEM Writers (3/3 plans) — completed 2026-03-03
- [x] Phase 7: BinaryEvtxWriter (3/3 plans) — completed 2026-03-03

</details>

<details>
<summary>✅ v3.0 TLS Certificate Automation (Phase 8) — SHIPPED 2026-03-04</summary>

- [x] Phase 8: TLS Certificate Automation with Let's Encrypt (4/4 plans) — completed 2026-03-03

</details>

### 🚧 v4.0 Industrialisation (In Progress)

**Milestone Goal:** Extract EVTX writer as an OSS Go module (`github.com/fjacquet/go-evtx`), then add durability guarantees (periodic fsync ≤15s) and file lifecycle management (size/count/time rotation) built directly into that module, wired back into cee-exporter.

- [x] **Phase 8.5: go-evtx OSS Module Extraction** — Create `github.com/fjacquet/go-evtx` as a standalone Go module with layered API (WriteRaw + WriteRecord), port existing tests, and replace cee-exporter's internal EVTX implementation with the new dependency (completed 2026-03-04)
- [ ] **Phase 9: Goroutine Scaffolding and fsync** — Establish the concurrency contract in go-evtx: background goroutine with correct shutdown, periodic f.Sync(), and ADRs documenting architectural decisions
- [ ] **Phase 10: Open-Handle Incremental Flush** — Replace os.WriteFile with a persistent *os.File held for the writer's lifetime; fix flushChunkLocked stub so no events are silently dropped
- [ ] **Phase 11: File Rotation** — Implement size-based, time-based, count-based, and SIGHUP-triggered rotation on top of the Phase 10 open-handle model
- [ ] **Phase 12: Config, Validation, Prometheus and Docs** — Wire all rotation/flush parameters into [output] TOML section, add startup validation, expose fsync gauge, update config.toml.example

## Phase Details

### Phase 8.5: go-evtx OSS Module Extraction
**Goal**: `github.com/fjacquet/go-evtx` exists as a standalone, tested, published Go module with a layered API; cee-exporter consumes it as a dependency instead of owning the EVTX implementation
**Depends on**: Phase 8 (existing BinaryEvtxWriter is the source of truth for extraction)
**Requirements**: EXT-01, EXT-02, EXT-03, EXT-04, EXT-05
**Success Criteria** (what must be TRUE):
  1. `go get github.com/fjacquet/go-evtx` works from any machine; module is published to pkg.go.dev
  2. `go-evtx` exposes `WriteRaw(chunk []byte) error` and `WriteRecord(eventID int, fields map[string]string) error`; both produce valid EVTX files confirmed by python-evtx
  3. All tests ported from `cee-exporter/pkg/evtx/` pass in the `go-evtx` CI pipeline
  4. `cee-exporter/go.mod` lists `github.com/fjacquet/go-evtx` as a dependency; `pkg/evtx/writer_evtx_notwindows.go` delegates to the module
  5. `cee-exporter` test suite (`make test`) still passes after the swap
**Plans**: 2 plans

Plans:
- [ ] 08.5-01-PLAN.md — Create go-evtx GitHub repo, implement binformat.go + binxml.go + evtx.go + tests, publish v0.1.0 (EXT-01, EXT-02, EXT-03, EXT-04)
- [ ] 08.5-02-PLAN.md — Add go-evtx dependency to cee-exporter, replace writer_evtx_notwindows.go with adapter, remove evtx_binformat.go (EXT-05)

### Phase 9: Goroutine Scaffolding and fsync
**Goal**: BinaryEvtxWriter writes events to disk within a bounded window and shuts down cleanly without losing buffered data
**Depends on**: Phase 8.5 (go-evtx v0.1.0 published; cee-exporter adapter complete)
**Requirements**: FLUSH-01, FLUSH-02, ADR-01, ADR-02
**Success Criteria** (what must be TRUE):
  1. Operator can set flush_interval_s (default 15) and BinaryEvtxWriter calls flushToFile() on that interval without data races
  2. On graceful shutdown (SIGINT/SIGTERM), all buffered events reach disk before the process exits
  3. The background goroutine exits cleanly when Close() is called (no goroutine leak detectable by go test -race)
  4. ADR-01 (flush ticker ownership in writer layer) and ADR-02 (open-handle vs write-on-close) are committed to docs/adr/
**Plans**: 2 plans

Plans:
- [ ] 09-01-PLAN.md — Add RotationConfig + backgroundLoop to go-evtx, write goroutine tests, publish v0.2.0 (FLUSH-01, FLUSH-02)
- [ ] 09-02-PLAN.md — Wire go-evtx v0.2.0 into cee-exporter, add FlushIntervalSec to OutputConfig, write ADR-012 and ADR-013 (FLUSH-01, ADR-01, ADR-02)

### Phase 10: Open-Handle Incremental Flush
**Goal**: BinaryEvtxWriter writes every event to disk regardless of session length, producing .evtx files that python-evtx parses correctly
**Depends on**: Phase 9
**Requirements**: EVTX-01
**Success Criteria** (what must be TRUE):
  1. A session producing more than 2,400 events generates a .evtx file where python-evtx reports the correct total record count (no silent drops)
  2. A two-flush session (two ticker intervals with events in each) produces a file that python-evtx parses without errors
  3. The EVTX file header fields NextRecordIdentifier and ChunkCount are correct after each flush (verified by hex dump at offsets 24-32)
  4. go test -race ./pkg/evtx/ reports zero data races on WriteEvent and Close concurrent calls
**Plans**: TBD

### Phase 11: File Rotation
**Goal**: BinaryEvtxWriter automatically manages .evtx file size and age, and responds to SIGHUP for on-demand rotation, so operators never face unbounded file growth or manual intervention
**Depends on**: Phase 10
**Requirements**: ROT-01, ROT-02, ROT-03, ROT-04
**Success Criteria** (what must be TRUE):
  1. When max_file_size_mb is set, the active .evtx file is renamed to a timestamped archive and a fresh file opened as soon as the size threshold is crossed
  2. When max_file_count is set, only the N most recent archive files remain after rotation (older files are deleted automatically)
  3. When rotation_interval_h is set, the active file is rotated on schedule regardless of its current size
  4. Sending SIGHUP to the running daemon triggers an immediate rotation without dropping events or restarting the process
  5. Rotated archive files are parseable by python-evtx (headers and CRCs are finalized before rename)
**Plans**: TBD

### Phase 12: Config, Validation, Prometheus and Docs
**Goal**: All durability and rotation parameters are operator-configurable in config.toml, invalid values are rejected at startup with clear messages, and SREs can alert on fsync health via Prometheus
**Depends on**: Phase 11
**Requirements**: FLUSH-03, CFG-01, CFG-02, CFG-03
**Success Criteria** (what must be TRUE):
  1. All four parameters (flush_interval_s, max_file_size_mb, max_file_count, rotation_interval_h) appear in [output] in config.toml.example with inline comments explaining defaults and zero-value semantics
  2. Starting the daemon with flush_interval_s = 0 produces a clear error message at startup and exits non-zero (no runtime panic)
  3. The Prometheus /metrics endpoint exposes cee_last_fsync_unix_seconds gauge that updates on each successful fsync
  4. All four [output] fields are read from config.toml and correctly mapped to BinaryEvtxWriter at construction time
**Plans**: TBD

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Quality | v1.0 | 3/3 | Complete | 2026-03-02 |
| 2. Build | v1.0 | 1/1 | Complete | 2026-03-02 |
| 3. Documentation | v1.0 | 2/2 | Complete | 2026-03-03 |
| 4. Observability & Linux Service | v2.0 | 3/3 | Complete | 2026-03-03 |
| 5. Windows Service | v2.0 | 3/3 | Complete | 2026-03-03 |
| 6. SIEM Writers | v2.0 | 3/3 | Complete | 2026-03-03 |
| 7. BinaryEvtxWriter | v2.0 | 3/3 | Complete | 2026-03-03 |
| 8. TLS Certificate Automation | v3.0 | 4/4 | Complete | 2026-03-03 |
| 8.5. go-evtx OSS Module Extraction | v4.0 | 2/2 | Complete | 2026-03-04 |
| 9. Goroutine Scaffolding and fsync | v4.0 | 0/2 | Not started | - |
| 10. Open-Handle Incremental Flush | v4.0 | 0/? | Not started | - |
| 11. File Rotation | v4.0 | 0/? | Not started | - |
| 12. Config, Validation, Prometheus and Docs | v4.0 | 0/? | Not started | - |
