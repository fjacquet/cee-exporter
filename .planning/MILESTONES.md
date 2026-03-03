# Milestones

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

