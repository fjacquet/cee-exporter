# Requirements: cee-exporter

**Defined:** 2026-03-04
**Milestone:** v4.0 Industrialisation
**Core Value:** Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## v4.0 Requirements

Requirements for the Industrialisation milestone. Scope: BinaryEvtxWriter durability, file rotation, and supporting config/docs.

### Durability (FLUSH)

- [ ] **FLUSH-01**: Operator can set `flush_interval_s` (default 15) so BinaryEvtxWriter calls `f.Sync()` every N seconds, bounding potential data loss to at most N seconds on power failure
- [ ] **FLUSH-02**: BinaryEvtxWriter flushes and fsyncs all buffered events to disk before the process exits on graceful shutdown
- [ ] **FLUSH-03**: Prometheus `/metrics` endpoint exposes a `cee_last_fsync_unix_seconds` gauge so SREs can alert when fsync has not occurred within the expected interval

### EVTX Correctness (EVTX)

- [ ] **EVTX-01**: BinaryEvtxWriter writes all events to disk regardless of session length (fix `flushChunkLocked()` stub that currently silently drops events beyond ~2,400 per session)

### File Rotation (ROT)

- [ ] **ROT-01**: Operator can set `max_file_size_mb` so the active `.evtx` file is rotated when it reaches that size (0 = unlimited; rotation produces a timestamped archive file)
- [ ] **ROT-02**: Operator can set `max_file_count` so only the N most recent archive files are kept and older ones are deleted automatically (0 = unlimited)
- [ ] **ROT-03**: Operator can set `rotation_interval_h` so the active `.evtx` file is rotated on a fixed schedule regardless of size (0 = disabled)
- [ ] **ROT-04**: Operator can send SIGHUP to the process to trigger an immediate `.evtx` file rotation without restarting the daemon

### Configuration (CFG)

- [ ] **CFG-01**: All flush and rotation parameters (`flush_interval_s`, `max_file_size_mb`, `max_file_count`, `rotation_interval_h`) are configurable in the `[output]` section of `config.toml` with documented zero-value semantics
- [ ] **CFG-02**: cee-exporter rejects invalid configuration (e.g., `flush_interval_s = 0`) at startup with a clear error message rather than panicking at runtime
- [ ] **CFG-03**: `config.toml.example` is updated to document all four new `[output]` fields with inline comments explaining default values and zero-value semantics

### Architecture & Documentation (ADR)

- [ ] **ADR-01**: Architecture Decision Record documents the decision to own the flush ticker inside `BinaryEvtxWriter` (not in the queue layer), explaining why `Flush()` was not added to the `Writer` interface
- [ ] **ADR-02**: Architecture Decision Record documents the decision to switch from write-on-close (`os.WriteFile`) to open-handle incremental flush, covering EVTX crash tolerance and fsync semantics

## v5 Requirements

Deferred to future release.

### Observability

- **OBS-01**: Prometheus counter `cee_rotation_total` for tracking rotation events over time

### EVTX

- **EVTX-02**: Multi-chunk EVTX files (full multi-chunk support beyond single-chunk-per-session)
- **EVTX-03**: Startup repair pass for partial-chunk files left by a crash (invalid CRC recovery)

### Rotation

- **ROT-F01**: Compression of rotated `.evtx` files (blocked on forensics tool support)

## Out of Scope

| Feature | Reason |
|---------|--------|
| Rotation for GELF, Syslog, Beats, Win32 writers | Network-based backends; file rotation not applicable |
| Log rotation for application logs (stdout/syslog) | Handled by systemd/logrotate at the OS level |
| DNS-01 ACME challenge | Already deferred in v3.0 (TLS-F01) |
| sd_notify READY=1 | Already deferred in v3.0 (OBS-F02) |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| FLUSH-01 | Phase 9 | Pending |
| FLUSH-02 | Phase 9 | Pending |
| ADR-01 | Phase 9 | Pending |
| ADR-02 | Phase 9 | Pending |
| EVTX-01 | Phase 10 | Pending |
| ROT-01 | Phase 11 | Pending |
| ROT-02 | Phase 11 | Pending |
| ROT-03 | Phase 11 | Pending |
| ROT-04 | Phase 11 | Pending |
| FLUSH-03 | Phase 12 | Pending |
| CFG-01 | Phase 12 | Pending |
| CFG-02 | Phase 12 | Pending |
| CFG-03 | Phase 12 | Pending |

**Coverage:**
- v4.0 requirements: 13 total
- Mapped to phases: 13
- Unmapped: 0 ✓

---
*Requirements defined: 2026-03-04*
*Last updated: 2026-03-04 — traceability filled after roadmap creation*
