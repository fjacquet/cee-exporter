---
phase: 08-tls-certificate-automation-with-let-s-encrypt
plan: "02"
subsystem: infra
tags: [tls, acme, config, toml, backward-compat, migration]

# Dependency graph
requires:
  - phase: 08-01
    provides: tls.go with buildManualTLS/buildSelfSignedTLS/buildAutocertTLS/logCertInfo

provides:
  - ListenConfig struct extended with TLSMode, ACMEDomains, ACMEEmail, ACMECacheDir, ACMEChallengeAddr
  - migrateListenConfig() function for backward compat with legacy tls=true+cert_file config
  - config.toml.example documenting all four tls_mode values with explanatory comments
  - config.toml updated with explicit tls_mode="off" default

affects:
  - 08-03-PLAN.md (will wire the tls_mode switch in run() using TODO(phase08-plan03) placeholder)
  - 08-04-PLAN.md (systemd unit changes depend on tls_mode field)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - Backward compat migration pattern: migrateListenConfig converts deprecated bool+file pattern to string enum
    - Config migration called immediately after toml.DecodeFile to normalize config before any use

key-files:
  created: []
  modified:
    - cmd/cee-exporter/main.go
    - config.toml.example
    - config.toml
    - go.sum

key-decisions:
  - "TLSMode defaults to 'off' via migrateListenConfig when tls=false or not set"
  - "Legacy tls=true+cert_file migrated to TLSMode='manual' transparently with no user action"
  - "go.sum updated for golang.org/x/net/idna (needed by autocert, missing from Plan 01 execution)"
  - "buildTLS/logCertInfo removed from main.go; only tls.go definitions remain"

patterns-established:
  - "Config migration: call migrateListenConfig(&cfg.Listen) immediately after toml.DecodeFile"
  - "TLS mode check: use TLSMode != 'off' instead of TLS bool for all post-migration logic"
  - "TODO comments mark Plan 03 insertion points: // TODO(phase08-plan03): wire tls_mode switch here"

requirements-completed: [TLS-04, TLS-05]

# Metrics
duration: 8min
completed: 2026-03-03
---

# Phase 08 Plan 02: Config Layer Summary

**ListenConfig extended with four-mode TLS enum (off/manual/acme/self-signed), migrateListenConfig() backward-compat function, and full config.toml.example documentation for all modes**

## Performance

- **Duration:** 8 min
- **Started:** 2026-03-03T08:59:40Z
- **Completed:** 2026-03-03T09:07:40Z
- **Tasks:** 2
- **Files modified:** 4 (main.go, config.toml.example, config.toml, go.sum)

## Accomplishments
- Extended ListenConfig with TLSMode, ACMEDomains, ACMEEmail, ACMECacheDir, ACMEChallengeAddr fields
- Added migrateListenConfig() that converts legacy tls=true+cert_file to TLSMode="manual" transparently
- Removed buildTLS/logCertInfo duplicate from main.go (now exclusively in tls.go from Plan 01)
- Removed old TLS wiring block; inserted TODO(phase08-plan03) placeholder for Plan 03
- Updated HealthConfig to use TLSMode != "off" instead of TLS bool
- Documented all four TLS modes in config.toml.example with CEPA-specific warnings
- Updated config.toml with explicit tls_mode="off" default and commented examples for all modes

## Task Commits

Each task was committed atomically:

1. **Task 1: Extend ListenConfig and add migrateListenConfig in main.go** - `50ef9dc` (feat)
2. **Task 2: Document all TLS modes in config.toml.example and config.toml** - `7693c6f` (docs)

**Plan metadata:** committed with final state update

## Files Created/Modified
- `/Users/fjacquet/Projects/cee-exporter/cmd/cee-exporter/main.go` - ListenConfig extended, migrateListenConfig added, buildTLS/logCertInfo removed, TODO placeholder inserted
- `/Users/fjacquet/Projects/cee-exporter/config.toml.example` - [listen] section replaced with full four-mode TLS documentation
- `/Users/fjacquet/Projects/cee-exporter/config.toml` - tls_mode="off" added as explicit default with commented examples
- `/Users/fjacquet/Projects/cee-exporter/go.sum` - Missing golang.org/x/net/idna entry added for autocert dependency

## Decisions Made
- Legacy tls=true+cert_file migrated to TLSMode="manual" transparently — no user config change required
- TLSMode defaults to "off" (not "manual") when tls=false, ensuring safe default
- Plan 03 insertion point marked with TODO comment to keep main.go compilable between plans
- HealthConfig.TLSEnabled uses TLSMode != "off" — more expressive than bool negation

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Fixed missing go.sum entry for golang.org/x/net/idna**
- **Found during:** Task 1 (build verification)
- **Issue:** `make build` failed with "missing go.sum entry for module providing package golang.org/x/net/idna" — autocert (added by Plan 01) requires x/net/idna but go.sum was incomplete
- **Fix:** Ran `go get golang.org/x/crypto/acme/autocert@v0.48.0` to populate missing go.sum entries
- **Files modified:** go.sum
- **Verification:** `make build && make build-windows` both succeed
- **Committed in:** 50ef9dc (Task 1 commit)

---

**Total deviations:** 1 auto-fixed (1 blocking)
**Impact on plan:** go.sum update was essential for compilation; no scope creep.

## Issues Encountered
- Plan 01 (tls.go) had already been executed before this plan ran, so tls.go was present as expected
- go.sum was missing entries for autocert transitive dependencies introduced by Plan 01 — fixed automatically

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- ListenConfig has all TLS mode fields Plan 03 needs to wire the switch in run()
- migrateListenConfig() ensures backward compat so existing config.toml files work unchanged
- TODO(phase08-plan03) placeholder marks exact insertion point in run()
- Both Linux and Windows binaries build successfully with CGO_ENABLED=0

## Self-Check: PASSED

- [x] cmd/cee-exporter/main.go - FOUND with TLSMode, ACMEDomains, migrateListenConfig
- [x] config.toml.example - FOUND with tls_mode documented (5 occurrences)
- [x] config.toml - FOUND with tls_mode = "off" default
- [x] Commits 50ef9dc and 7693c6f - FOUND in git log
- [x] make build - PASSES
- [x] make build-windows - PASSES
- [x] make lint - PASSES

---
*Phase: 08-tls-certificate-automation-with-let-s-encrypt*
*Completed: 2026-03-03*
