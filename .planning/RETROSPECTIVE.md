# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

## Milestone: v4.0 — Industrialisation

**Shipped:** 2026-03-05
**Phases:** 5 (Phases 8.5-12) | **Plans:** 10

### What Was Built

- `github.com/fjacquet/go-evtx` v0.1.0–v0.5.0 — standalone OSS Go module extracted from cee-exporter
- Open-handle incremental flush model (`f.WriteAt`) replacing write-on-close — fixes silent drop beyond 2,400 events
- Background flush goroutine with correct shutdown ordering (`close(done)→wg.Wait()→mu.Lock()→flush`)
- File rotation: size (max_file_size_mb), count (max_file_count), time (rotation_interval_h), SIGHUP
- Prometheus `cee_last_fsync_unix_seconds` gauge via `OnFsync` callback chain
- `validateOutputConfig()` startup validation with clear error messages
- ADR-012 (flush ticker ownership) and ADR-013 (write-on-close model) in docs/adr/

### What Worked

- TDD (RED first, GREEN after) for every go-evtx feature — goroutine tests caught the ticker-before-lock deadlock pattern before it shipped
- Incremental version bumping (v0.1.0→v0.5.0) with `go mod replace` kept iteration fast despite GOPROXY indexing delay
- Race detector (`go test -race`) clean across all 5 phases — goroutine lifecycle design held
- `_notwindows.go` + `//go:build !windows` pattern continues to prevent platform naming mistakes
- Integration checker agent surfaced the v0.4.0/v0.5.0 require/replace mismatch as release debt
- nil-channel select idiom for optional rotation ticker is clean and zero-overhead

### What Was Inefficient

- GOPROXY indexing lag required `go mod replace` workaround for every version bump — must push v0.5.0 tag and remove replace before next release
- Pre-append vs post-append capacity check bug (Phase 10): silent 2965-event drop discovered only after implementation — should have been caught in Wave 0 test design
- VALIDATION.md `nyquist_compliant` flags never flipped to `true` after test execution — paperwork debt carried to audit
- RTK `git log | head` caused Rust binary panics (broken pipe) — worked around with separate command invocations

### Patterns Established

- **go mod replace for OSS submodule iteration**: use `replace github.com/X => ../X` locally; push semver tag + update require for each published version
- **OnFsync callback pattern**: inject observability hooks into library via `RotationConfig` struct fields — decouples library from Prometheus/metrics layer
- **Type assertion for optional interface** (`w.(interface{ Rotate() error })`): SIGHUP handler works for any writer with Rotate() without polluting the Writer interface
- **Private mutex contract**: `rotate()` requires caller holds `w.mu`; `Rotate()` acquires it — prevents double-lock with single mutex

### Key Lessons

1. When extracting an OSS module, plan for GOPROXY indexing lag — the `replace` directive is mandatory for the full extraction cycle
2. Write the capacity check BEFORE append, not after — `flushChunkLocked()` being a stub silently dropped events; the test should have caught this in Phase 10 Wave 0
3. The goroutine shutdown ordering is non-negotiable: `close(done)` → `wg.Wait()` (no lock held) → `mu.Lock()` → flush. Any other order creates deadlock potential
4. Flip `nyquist_compliant: true` immediately after confirming all VALIDATION.md tests are green — don't defer to milestone audit

### Cost Observations

- Model mix: ~50% sonnet, ~40% sonnet (subagents), ~10% haiku
- 5 phases executed in 1 day (2026-03-05)
- Notable: Parallel wave execution (integration checker + phase executor in parallel) saved ~30% of total session time

---

## Milestone: v3.0 — TLS Certificate Automation

**Shipped:** 2026-03-04
**Phases:** 1 (Phase 8) | **Plans:** 4

### What Was Built

- Four-mode TLS switch (off/manual/acme/self-signed) in `cmd/cee-exporter/tls.go`
- Automatic Let's Encrypt cert via autocert.Manager with TLS-ALPN-01 challenge
- Runtime self-signed ECDSA P-256 cert (zero files, zero network)
- Backward-compatible config migration (tls=true → tls_mode="manual")
- 9 unit tests (6 migration + 3 self-signed)
- systemd AmbientCapabilities for privileged ACME port 443

### What Worked

- Template-based BinXML fix for Phase 7 completed during v3.0 cycle — python-evtx now parses all records
- Pure stdlib approach for self-signed certs keeps CGO_ENABLED=0 constraint intact
- migrateListenConfig pattern provides zero-effort upgrade path for existing operators
- autocert.Manager handles cert renewal automatically with no operator intervention

### What Was Inefficient

- Phase 7 BinXML debugging required multiple iterations to identify the missing attribute_list_size field
- The TemplateNode header size confusion (24 vs 28 bytes) cost a full cycle of build/test/debug
- REQUIREMENTS.md traceability table not updated during execution — caught only at milestone audit

### Patterns Established

- **Config migration function**: migrateListenConfig pattern for backward-compatible config evolution
- **nil sentinel for TLS**: `*tls.Config == nil` means plain HTTP; non-nil means TLS mode
- **Three-source cross-reference**: VERIFICATION + SUMMARY + REQUIREMENTS for audit completeness

### Key Lessons

1. When debugging binary format parsers, always read the parser source (python-evtx Nodes.py) before guessing at byte layouts
2. BinXML token flags determine field presence — the `flags() & 0x04` check for attribute_list_size was the root cause of the OverrunBufferException
3. Update REQUIREMENTS.md traceability during phase execution, not just at milestone completion

### Cost Observations

- Model mix: ~60% opus, ~30% sonnet, ~10% haiku
- Notable: Integration checker agent (sonnet) completed 7 checks in ~3 min — good parallelization ROI

---

## Milestone: v2.0 — Operations & Output Expansion

**Shipped:** 2026-03-03
**Phases:** 4 (Phases 4-7) | **Plans:** 12

### What Was Built

- Prometheus /metrics endpoint on port 9228 (5 cee_* metrics)
- Hardened systemd unit file with ProtectSystem=strict
- Windows Service SCM wrapper (install/uninstall/recovery)
- SyslogWriter (RFC 5424 over UDP/TCP with octet-counting framing)
- BeatsWriter (Lumberjack v2 with TLS via SyncDialWith)
- Pure-Go BinaryEvtxWriter with template-based BinXML

### What Worked

- Wave-based parallelization of independent phases (5+6 parallel, 7 serial)
- Private Prometheus registry keeps scrape output clean
- crewjam/rfc5424 avoids stdlib log/syslog Windows build exclusion
- CRC32 deferred-patch pattern simplifies EVTX header construction

### What Was Inefficient

- 0xrawsec/golang-evtx oracle exclusion meant BinXML correctness relied on structural tests only
- Phase 7 BinXML debugging extended over multiple sessions

### Patterns Established

- `_notwindows.go` suffix pattern (not `_linux.go`) for cross-platform Go files
- reconnect-once resilience pattern for network writers (GELF, Syslog, Beats)

### Key Lessons

1. Always validate BinXML output with an actual parser (python-evtx) — structural tests alone are insufficient
2. Private Prometheus registries prevent metric namespace pollution

---

## Milestone: v1.0 — MVP

**Shipped:** 2026-03-03
**Phases:** 3 (Phases 1-3) | **Plans:** 6

### What Was Built

- CEPA HTTP handler with RegisterRequest handshake
- CEE XML parser (single + VCAPS batch)
- CEPA→Windows EventID mapper (6 event types)
- Async queue with worker goroutines
- GELF 1.1 JSON writer (UDP/TCP)
- Win32 EventLog writer (Windows only)
- Complete operator README and documentation suite

### What Worked

- Table-driven tests with t.Run for all multi-case tests
- White-box testing (same-package) for access to unexported symbols
- CGO_ENABLED=0 from day one ensures consistent cross-platform builds

### Key Lessons

1. CEPA RegisterRequest MUST return empty body — any XML causes fatal Dell-side parse error
2. ACK HTTP requests before queuing work (CEPA 3s timeout)

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Phases | Plans | Key Change |
|-----------|--------|-------|------------|
| v1.0 | 3 | 6 | Established testing patterns and build infrastructure |
| v2.0 | 4 | 12 | Added 4 writer backends; wave-based parallelization |
| v3.0 | 1 | 4 | Config migration pattern; milestone audit workflow |
| v4.0 | 5 | 10 | OSS module extraction; TDD goroutine lifecycle; OnFsync callback pattern |

### Cumulative Quality

| Milestone | Tests | New Dependencies |
|-----------|-------|-----------------|
| v1.0 | 35 | toml, x/sys/windows |
| v2.0 | 74 | prometheus, kardianos/service, crewjam/rfc5424, go-lumber |
| v3.0 | 86 | golang.org/x/crypto |
| v4.0 | 100+ (combined cee-exporter + go-evtx) | github.com/fjacquet/go-evtx (own OSS module) |

### Top Lessons (Verified Across Milestones)

1. CGO_ENABLED=0 constraint shapes every dependency choice — validate before adding
2. White-box stdlib-only tests scale well across 9 packages with zero test framework debt
3. Always validate binary format output with the actual target parser, not just structural checks
4. OSS module extraction requires `go mod replace` during active development; plan for GOPROXY lag before publishing
5. Goroutine lifecycle patterns (done chan + WaitGroup + correct Close() ordering) are non-negotiable for race-clean code
