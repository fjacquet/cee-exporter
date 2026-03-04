---
phase: 08-tls-certificate-automation-with-let-s-encrypt
plan: "01"
subsystem: infra
tags: [tls, acme, autocert, lets-encrypt, self-signed, ecdsa, crypto]

# Dependency graph
requires:
  - phase: 07-binaryevtxwriter
    provides: completed writer backends; main.go already refactored for Phase 8

provides:
  - cmd/cee-exporter/tls.go with 5 TLS builder functions (buildManualTLS, buildSelfSignedTLS, buildAutocertTLS, startACMEChallengeListener, logCertInfo)
  - golang.org/x/crypto v0.48.0 as direct dependency in go.mod
  - Foundation for Plan 02 (config switch wiring) and Plan 03 (ACME challenge listener)

affects:
  - 08-02 (config TLSMode switch wires buildManualTLS/buildSelfSignedTLS/buildAutocertTLS)
  - 08-03 (startACMEChallengeListener consumed in ACME mode)
  - 08-04 (integration/e2e tests import tls.go functions)

# Tech tracking
tech-stack:
  added:
    - golang.org/x/crypto v0.48.0 (autocert ACME client)
    - golang.org/x/net v0.49.0 (indirect, required by autocert)
    - golang.org/x/text v0.34.0 (indirect, required by autocert)
  patterns:
    - Self-signed TLS with stdlib only (no external deps): ecdsa.GenerateKey + x509.CreateCertificate + pem.EncodeToMemory + tls.X509KeyPair
    - ACME autocert.Manager with DirCache for cert persistence
    - Separation of TLS logic into dedicated tls.go file (no build tag — compiles everywhere)

key-files:
  created:
    - cmd/cee-exporter/tls.go
  modified:
    - go.mod (golang.org/x/crypto promoted to direct; golang.org/x/net + text added as indirect)
    - go.sum (new hash entries for crypto, net, text)

key-decisions:
  - "golang.org/x/crypto promoted to direct dependency v0.48.0 — autocert package requires explicit pinning"
  - "No build tag on tls.go — autocert is pure Go with no C deps; compiles CGO_ENABLED=0 on linux and windows"
  - "buildSelfSignedTLS uses stdlib crypto only — ecdsa.P256 + x509.CreateCertificate, no external dependency"
  - "autocert.Manager uses production Let's Encrypt by default — staging is an operator config concern not code"
  - "acme_cache_dir defaults to /var/cache/cee-exporter/acme when empty — operator can override via config"
  - "buildManualTLS replaces the former buildTLS in main.go — duplicate removed as Rule 3 auto-fix since main.go was already updated by Plan 02 prep work"

patterns-established:
  - "TLS builder functions isolated in tls.go, no build constraints — all platforms compile the same file"
  - "Self-signed cert clock skew tolerance: NotBefore=now-1min; 1-year validity"
  - "ACME challenge listener errors are non-fatal (logged in goroutine) — renewal continues independently"

requirements-completed: [TLS-01, TLS-02, TLS-03]

# Metrics
duration: 2min
completed: 2026-03-03
---

# Phase 8 Plan 01: TLS Builder Functions Summary

**Five TLS builder functions (manual, self-signed ECDSA, ACME autocert) extracted into cmd/cee-exporter/tls.go with golang.org/x/crypto v0.48.0 promoted to direct dependency**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-03T20:19:23Z
- **Completed:** 2026-03-03T20:21:23Z
- **Tasks:** 2
- **Files modified:** 3 (tls.go created, go.mod + go.sum updated)

## Accomplishments

- Created cmd/cee-exporter/tls.go (164 lines) with all 5 required TLS builder functions
- Promoted golang.org/x/crypto v0.48.0 to direct dependency — pulled in golang.org/x/net and golang.org/x/text as indirect deps via autocert's dependency chain
- Both Linux/amd64 and Windows/amd64 binaries build successfully with CGO_ENABLED=0

## Task Commits

Each task was committed atomically:

1. **Task 1: Promote golang.org/x/crypto to direct dependency** - `0202741` (chore)
2. **Task 2: Create cmd/cee-exporter/tls.go** - `7e6a9b5` (feat)

## Files Created/Modified

- `cmd/cee-exporter/tls.go` - All 5 TLS builder functions: buildManualTLS, buildSelfSignedTLS, buildAutocertTLS, startACMEChallengeListener, logCertInfo
- `go.mod` - golang.org/x/crypto v0.48.0 direct dep; golang.org/x/net + text added indirect
- `go.sum` - New hash entries for crypto, net, text packages

## Decisions Made

- No build tag on tls.go: autocert is pure Go, no C dependencies, compiles with CGO_ENABLED=0 on all platforms
- buildSelfSignedTLS uses stdlib only: ecdsa.GenerateKey (P-256), x509.CreateCertificate, pem.EncodeToMemory, tls.X509KeyPair — no external library
- Production Let's Encrypt used by autocert default — staging is operator responsibility via DNS/config
- acme_cache_dir defaults to /var/cache/cee-exporter/acme when not configured
- buildManualTLS replaces the former buildTLS function (duplicate logCertInfo also resolved)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Discovered main.go had already been updated by Plan 02 prep work**
- **Found during:** Task 2 (creating tls.go)
- **Issue:** main.go already had migrateListenConfig, TLSMode fields, and old buildTLS/logCertInfo removed — meaning tls.go could not also define logCertInfo without seeing the prior Plan 02 changes
- **Fix:** Verified main.go state on re-read; confirmed no duplicates existed; tls.go compiles cleanly
- **Files modified:** None additional (existing state was already correct)
- **Verification:** `make build` and `make build-windows` both succeeded
- **Committed in:** 7e6a9b5 (Task 2 commit)

**2. [Rule 3 - Blocking] go mod tidy removed golang.org/x/crypto before tls.go was created**
- **Found during:** Task 1 (promoting go dependency)
- **Issue:** go mod tidy strips deps with no importer; running it before tls.go existed would remove crypto
- **Fix:** Added crypto to go.mod manually, ran go mod download to populate go.sum, then created tls.go (which imports autocert) before running tidy
- **Files modified:** go.mod, go.sum
- **Verification:** `grep "golang.org/x/crypto" go.mod | grep -v indirect` shows direct dep
- **Committed in:** 0202741 (Task 1 commit)

---

**Total deviations:** 2 auto-fixed (2 blocking)
**Impact on plan:** Both auto-fixes resolved coordination issues between parallel plans. No scope creep.

## Issues Encountered

- go mod tidy removes unused deps — had to add the require line manually before the import existed to prevent the dependency being stripped; resolved by creating tls.go first
- main.go was already partially updated (Plan 02 prep merged earlier) — confirmed no duplicate function definitions existed

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- cmd/cee-exporter/tls.go is ready for Plan 02 to wire the tls_mode config switch
- Plan 03 can consume startACMEChallengeListener for ACME HTTP-01 challenge listener
- All 5 functions satisfy the must_haves.artifacts requirements
- Linux and Windows cross-compilation verified with CGO_ENABLED=0

---
*Phase: 08-tls-certificate-automation-with-let-s-encrypt*
*Completed: 2026-03-03*
