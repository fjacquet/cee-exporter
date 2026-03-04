---
phase: 9
slug: goroutine-scaffolding-and-fsync
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-04
---

# Phase 9 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) |
| **Config file** | none — stdlib only, no testify |
| **Quick run command** | `cd ~/Projects/go-evtx && go test ./... -count=1` |
| **Full suite command** | `cd ~/Projects/go-evtx && go test -race ./... && cd /Users/fjacquet/Projects/cee-exporter && make test` |
| **Estimated runtime** | ~15 seconds |

---

## Sampling Rate

- **After every task commit:** Run `cd ~/Projects/go-evtx && go test ./... -count=1`
- **After every plan wave:** Run full suite (go-evtx race + cee-exporter make test)
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** ~15 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 09-01-01 | 01 | 1 | FLUSH-01 | unit | `go test ./... -run TestWriter_FlushTicker -race` | ❌ W0 | ⬜ pending |
| 09-01-02 | 01 | 1 | FLUSH-02 | unit | `go test ./... -run TestWriter_GracefulShutdown -race` | ❌ W0 | ⬜ pending |
| 09-01-03 | 01 | 1 | FLUSH-01/02 | race | `go test -race ./... -count=1` | ✅ existing | ⬜ pending |
| 09-02-01 | 02 | 2 | ADR-01 | manual | `ls ~/Projects/cee-exporter/docs/adr/ADR-012*` | ❌ W0 | ⬜ pending |
| 09-02-02 | 02 | 2 | ADR-02 | manual | `ls ~/Projects/cee-exporter/docs/adr/ADR-013*` | ❌ W0 | ⬜ pending |
| 09-02-03 | 02 | 2 | FLUSH-01 | integration | `make test` in cee-exporter | ✅ existing | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `~/Projects/go-evtx/evtx_goroutine_test.go` — stubs for `TestWriter_FlushTicker` and `TestWriter_GracefulShutdown` (RED before implementation)
- [ ] `~/Projects/cee-exporter/docs/adr/` directory must exist (already present from Phase 8.5)

*Wave 0 is merged into Wave 1 Task 1 (TDD: write tests first, then implement).*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| ADR-01 committed to docs/adr/ | ADR-01 | Document artifact, not runtime behavior | `ls ~/Projects/cee-exporter/docs/adr/ADR-012*` returns a file |
| ADR-02 committed to docs/adr/ | ADR-02 | Document artifact, not runtime behavior | `ls ~/Projects/cee-exporter/docs/adr/ADR-013*` returns a file |
| No goroutine leak under race detector | FLUSH-01 | Requires CGO=1 (separate from make test) | `cd ~/Projects/go-evtx && go test -race ./... -count=1 -v` |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 15s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
