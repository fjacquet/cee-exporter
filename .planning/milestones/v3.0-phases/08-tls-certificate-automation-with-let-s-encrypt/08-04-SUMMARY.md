---
plan: 08-04
phase: 08-tls-certificate-automation-with-let-s-encrypt
status: complete
completed: 2026-03-03
tasks_total: 1
tasks_complete: 1
---

# Plan 08-04 Summary: TDD — migrateListenConfig + buildSelfSignedTLS

## What was built

**`cmd/cee-exporter/tls_test.go`** — 9 unit tests covering the two pure, testable
functions introduced in Phase 8.

### TestMigrateListenConfig (6 table-driven cases)

| Case | Input | Expected TLSMode |
|------|-------|-----------------|
| already_set_manual | TLSMode="manual" | "manual" (no change) |
| already_set_acme | TLSMode="acme" + TLS=true | "acme" (no change) |
| legacy_tls_true_with_cert | TLS=true + CertFile set | "manual" (migration) |
| legacy_tls_false_no_cert | TLS=false | "off" |
| tls_true_no_cert_no_mode | TLS=true, no CertFile | "off" |
| zero_value | empty ListenConfig | "off" |

### TestBuildSelfSignedTLS (3 cases)

- **with_hosts** — verifies ECDSA key type, SAN contains requested hostname, cert valid for 1 year
- **empty_hosts_fallback** — empty slice falls back to "localhost"
- **nil_hosts_fallback** — nil slice falls back to "localhost"

## Verification

```
go test ./cmd/cee-exporter/ -run "TestMigrateListenConfig|TestBuildSelfSignedTLS" -v
```
All 11 sub-tests pass (6 migration + 3 self-signed + 2 extra validation assertions).

## Key decisions

- White-box package `main` per CLAUDE.md convention
- stdlib only: `crypto/tls`, `crypto/x509`, `testing`, `time` — no external test deps
- Tests pass GREEN immediately since functions were implemented in Plans 01/02 — TDD
  retrofit catches regressions on the config migration critical path
- Requirements covered: TLS-04 (migration backward compat)

## key-files

### created
- `cmd/cee-exporter/tls_test.go` — 9 test functions, stdlib only, package main
