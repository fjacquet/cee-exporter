---
phase: 10
slug: open-handle-incremental-flush
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-05
---

# Phase 10 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) |
| **Config file** | none — stdlib only, no testify |
| **Quick run command** | `cd ~/Projects/go-evtx && go test ./... -count=1` |
| **Full suite command** | `cd ~/Projects/go-evtx && go test -race ./... -count=1 && cd /Users/fjacquet/Projects/cee-exporter && make test` |
| **Estimated runtime** | ~20 seconds |

---

## Sampling Rate

- **After every task commit:** Run `cd ~/Projects/go-evtx && go test ./... -count=1`
- **After every plan wave:** Run full suite (go-evtx race + cee-exporter make test)
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** ~20 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 10-01-01 | 01 | 1 | EVTX-01 | unit | `go test ./... -run TestWriter_MultiChunk -race` | ❌ W0 | ⬜ pending |
| 10-01-02 | 01 | 1 | EVTX-01 | unit | `go test ./... -run TestWriter_OpenHandle -race` | ❌ W0 | ⬜ pending |
| 10-01-03 | 01 | 1 | EVTX-01 | race | `go test -race ./... -count=1` | ✅ existing | ⬜ pending |
| 10-01-04 | 01 | 1 | EVTX-01 | integration | `make test` in cee-exporter | ✅ existing | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `~/Projects/go-evtx/openhandle_test.go` — stubs for:
  - `TestWriter_MultiChunk_EventCount` — session >2,400 events; verifies all are readable
  - `TestWriter_MultiChunk_HeaderFields` — verifies ChunkCount and NextRecordIdentifier at correct offsets after multi-chunk write
  - `TestWriter_TwoFlushSession` — two ticker intervals with events; file parses without error
  - `TestWriter_EmptyClose_NoFile` — Close() on empty writer removes the file (backward compat)
  - `TestWriter_OpenHandle_NoRace` — concurrent WriteRecord + background ticker; go test -race clean

*Wave 0 is merged into Wave 1 Task 1 (TDD: write tests RED first, then implement GREEN).*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| python-evtx parses multi-chunk file | EVTX-01 | Requires Python + evtx package installed | `python3 -c "import evtx; h=evtx.PyEvtxParser('out.evtx'); print(sum(1 for _ in h.records()))"` |
| Hex dump file header offsets correct | EVTX-01 | Structural verification beyond test assertions | `xxd out.evtx \| head -20` — check ChunkCount at bytes 42-43, NextRecordIdentifier at bytes 24-31 |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 20s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
