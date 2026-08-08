# Milestones

## v4.0 Industrialisation (Shipped: 2026-03-05)

**Phases completed:** 5 phases, 10 plans, 5 tasks

**Key accomplishments:**
- (none recorded)

---

## v3.0 TLS Certificate Automation (Shipped: 2026-03-04)

**Phases:** 8 (Phase 8 new + Phases 1-7 dependency) | **Plans:** 4 (Phase 8) | **LOC:** 4,623 Go
**Timeline:** 2026-03-03 (1 day)

**Key accomplishments:**

1. Four-mode TLS switch (off/manual/acme/self-signed) wired into run() with config migration
2. ACME/Let's Encrypt auto-cert via autocert.Manager with TLS-ALPN-01 challenge on port 443
3. Runtime self-signed ECDSA P-256 cert generation — zero files, zero network, zero config
4. Backward-compatible config migration: legacy tls=true+cert_file auto-upgrades to tls_mode="manual"
5. 9 unit tests covering migrateListenConfig (6 cases) and buildSelfSignedTLS (3 cases)
6. systemd unit updated with AmbientCapabilities=CAP_NET_BIND_SERVICE for privileged port binding

**Also completed during v3.0 cycle:**

7. Phase 7 BinXML fix: template-based BinXML with attribute_list_size field — python-evtx parses all records
8. Phase 7 UAT Test 6 passed: forensics tool (python-evtx) confirms correct EventIDs, timestamps, and audit fields

---

## v1.0 MVP (Shipped: 2026-03-03)

**Phases:** 3 (Quality, Build, Documentation) | **Plans:** 6 | **LOC:** ~2,138 Go
**Timeline:** 2026-03-02 → 2026-03-03 (1 day)

**Key accomplishments:**

1. Fixed `readBody` nil ResponseWriter panic; 35 unit tests across 5 packages pass with 0 data races
2. Table-driven unit tests for parser, mapper, queue, and GELF writer — stdlib only, no testify
3. Makefile with build/build-windows/test/lint/clean; static Linux ELF and cross-compiled Windows PE32+ binaries verified
4. Complete operator README: 7-step quickstart, 16-field config table, SAN TLS cert guide, 10-step PowerStore CEPA registration
5. CHANGELOG.md, PRD with personas, and five Nygard-format ADRs documenting key architecture decisions
6. Multi-stage Dockerfile (scratch), GitHub Actions CI/release/docs workflows, mkdocs-material site

**Known tech debt:**

- `make test` omits `-race` (incompatible with CGO_ENABLED=0 build posture)
- Win32 EventID 4663/4660/4670 may need message DLL for correct Event Viewer display

---
