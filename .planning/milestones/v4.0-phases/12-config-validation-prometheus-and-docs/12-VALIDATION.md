---
phase: 12
slug: config-validation-prometheus-and-docs
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-05
---

# Phase 12 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib) |
| **Config file** | none — stdlib only, no testify |
| **Quick run command** | `cd /Users/fjacquet/Projects/cee-exporter && make test` |
| **Full suite command** | `cd /Users/fjacquet/Projects/cee-exporter && make test && make build && make build-windows` |
| **Estimated runtime** | ~20 seconds |

---

## Sampling Rate

- **After every task commit:** Run `cd /Users/fjacquet/Projects/cee-exporter && make test`
- **After every plan wave:** Run full suite (make test + make build + make build-windows)
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** ~20 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 12-01-01 | 01 | 1 | FLUSH-03 | unit | `go test ./pkg/metrics/ -run TestFsyncGauge -count=1` | ❌ W0 | ⬜ pending |
| 12-01-02 | 01 | 1 | FLUSH-03 | unit | `go test ./pkg/prometheus/ -run TestPrometheus_FsyncGauge -count=1` | ❌ W0 | ⬜ pending |
| 12-01-03 | 01 | 1 | CFG-02 | unit | `go test ./cmd/cee-exporter/ -run TestValidateConfig -count=1` | ❌ W0 | ⬜ pending |
| 12-01-04 | 01 | 1 | CFG-01/03 | manual | `grep flush_interval_s config.toml.example` | ✅ file exists | ⬜ pending |
| 12-02-01 | 02 | 2 | FLUSH-03 | integration | `make test` (all packages) | ✅ existing | ⬜ pending |
| 12-02-02 | 02 | 2 | CFG-01..03 | integration | `make build && make build-windows` | ✅ existing | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `pkg/metrics/metrics.go` — add `LastFsyncUnix int64` atomic field to `Metrics` struct
- [ ] `pkg/metrics/metrics_test.go` — stub `TestFsyncGauge` (atomic read/write)
- [ ] `pkg/prometheus/prometheus_test.go` — stub `TestPrometheus_FsyncGauge` (gauge appears in /metrics output)
- [ ] `cmd/cee-exporter/config_test.go` (or similar) — stub `TestValidateConfig` (flush_interval_s=0 → error; negative values → error; valid config → nil)

*Wave 0 is merged into Wave 1 Task 1 (TDD: write tests RED first, implement GREEN).*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| config.toml.example has all four fields with comments | CFG-03 | Document artifact, not runtime behavior | `grep -A2 flush_interval_s config.toml.example` |
| flush_interval_s = 0 exits non-zero at startup | CFG-02 | Requires running the binary | `echo '[output]\nflush_interval_s = 0\ntype = "evtx"' > /tmp/bad.toml && ./cee-exporter -config /tmp/bad.toml; echo "exit: $?"` |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 20s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
