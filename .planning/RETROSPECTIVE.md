# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

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

### Cumulative Quality

| Milestone | Tests | New Dependencies |
|-----------|-------|-----------------|
| v1.0 | 35 | toml, x/sys/windows |
| v2.0 | 74 | prometheus, kardianos/service, crewjam/rfc5424, go-lumber |
| v3.0 | 86 | golang.org/x/crypto |

### Top Lessons (Verified Across Milestones)

1. CGO_ENABLED=0 constraint shapes every dependency choice — validate before adding
2. White-box stdlib-only tests scale well across 9 packages with zero test framework debt
3. Always validate binary format output with the actual target parser, not just structural checks
