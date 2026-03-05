---
phase: 11
slug: file-rotation
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-05
---

# Phase 11 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) |
| **Config file** | none — stdlib only, no testify |
| **Quick run command** | `cd ~/Projects/go-evtx && go test ./... -count=1` |
| **Full suite command** | `cd ~/Projects/go-evtx && go test -race ./... -count=1 && cd /Users/fjacquet/Projects/cee-exporter && make test` |
| **Estimated runtime** | ~25 seconds |

---

## Sampling Rate

- **After every task commit:** Run `cd ~/Projects/go-evtx && go test ./... -count=1`
- **After every plan wave:** Run full suite (go-evtx race + cee-exporter make test)
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** ~25 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 11-01-01 | 01 | 1 | ROT-01 | unit | `go test ./... -run TestWriter_SizeRotation -count=1` | ❌ W0 | ⬜ pending |
| 11-01-02 | 01 | 1 | ROT-02 | unit | `go test ./... -run TestWriter_FileCountPrune -count=1` | ❌ W0 | ⬜ pending |
| 11-01-03 | 01 | 1 | ROT-03 | unit | `go test ./... -run TestWriter_TimeRotation -count=1` | ❌ W0 | ⬜ pending |
| 11-01-04 | 01 | 1 | ROT-04 | unit | `go test ./... -run TestWriter_Rotate -count=1` | ❌ W0 | ⬜ pending |
| 11-01-05 | 01 | 1 | ROT-01..04 | race | `go test -race ./... -count=1` | ✅ existing | ⬜ pending |
| 11-02-01 | 02 | 2 | ROT-04 | integration | `go test ./... -run TestSIGHUP -count=1` in cee-exporter | ❌ W0 | ⬜ pending |
| 11-02-02 | 02 | 2 | ROT-01..04 | integration | `make test` in cee-exporter | ✅ existing | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `~/Projects/go-evtx/rotation_test.go` — stubs for:
  - `TestWriter_SizeRotation` — writes past MaxFileSizeMB; verifies archive created, fresh file opened
  - `TestWriter_FileCountPrune` — creates N+1 archives; verifies only N remain
  - `TestWriter_TimeRotation` — advances time mock; verifies rotation occurred
  - `TestWriter_Rotate_PublicMethod` — calls `w.Rotate()` directly; verifies archive parseable
  - `TestWriter_RotatedArchiveParseable` — Reader round-trip on rotated archive
- [ ] `~/Projects/go-evtx/rotation_test.go` — race test `TestWriter_Rotate_NoRace`
- [ ] `/Users/fjacquet/Projects/cee-exporter/pkg/evtx/sighup_notwindows.go` — SIGHUP signal handler wiring
- [ ] `/Users/fjacquet/Projects/cee-exporter/pkg/evtx/sighup_windows.go` — no-op stub for Windows

*Wave 0 tests are written RED first, then implementation brings them GREEN.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| SIGHUP triggers rotation in running daemon | ROT-04 | Requires live process + signal delivery | `./cee-exporter -config config.toml & sleep 2 && kill -HUP $! && ls *.evtx` |
| Rotated archive parseable by python-evtx | ROT-01 | python-evtx not in automated suite | `python3 -c "import evtx; h=evtx.PyEvtxParser('archive-*.evtx'); print(sum(1 for _ in h.records()))"` |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 25s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
