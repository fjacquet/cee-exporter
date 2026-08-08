---
phase: 04-observability-linux-service
plan: 02
subsystem: infra
tags: [systemd, linux, service, deployment, hardening]

# Dependency graph
requires: []
provides:
  - Hardened systemd unit file at deploy/systemd/cee-exporter.service (Type=simple, network-online.target, Restart=on-failure, ProtectSystem=strict)
  - Makefile install-systemd target for operator deployment workflow
affects: [05-windows-service, 07-binary-evtx]

# Tech tracking
tech-stack:
  added: []
  patterns: [systemd Type=simple for Go daemons, ProtectSystem=strict with ReadWritePaths for filesystem hardening]

key-files:
  created:
    - deploy/systemd/cee-exporter.service
  modified:
    - Makefile

key-decisions:
  - "Type=simple chosen over Type=notify (sd_notify integration deferred to OBS-F02)"
  - "After=network-online.target with Wants= (not Requires=) to avoid hard boot failure if networkd absent"
  - "EnvironmentFile=-/etc/cee-exporter/env uses leading dash so missing file is not an error"
  - "install-systemd placed in Makefile after clean target; requires sudo; recipe uses tab indentation"

patterns-established:
  - "Go daemon systemd unit: Type=simple, Restart=on-failure+RestartSec=5s, ProtectSystem=strict"
  - "Makefile deployment targets use SYSTEMD_UNIT_SRC/DST variables for path configuration"

requirements-completed: [DEPLOY-01, DEPLOY-02]

# Metrics
duration: 1min
completed: 2026-03-03
---

# Phase 4 Plan 02: Systemd Unit File and Makefile Deployment Target Summary

**Hardened systemd unit (Type=simple, network-online.target, Restart=on-failure+5s, ProtectSystem=strict) and Makefile install-systemd target for Linux operator deployment**

## Performance

- **Duration:** 1 min
- **Started:** 2026-03-03T14:37:00Z
- **Completed:** 2026-03-03T14:38:13Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Created deploy/systemd/cee-exporter.service with full filesystem hardening (ProtectSystem=strict, NoNewPrivileges=true, PrivateTmp=true)
- Auto-restart on crash with Restart=on-failure + RestartSec=5s to prevent tight restart loops
- Correct network dependency ordering via After=network-online.target (not network.target) with Wants= soft dependency
- Added install-systemd Makefile target with tab-indented recipe, SYSTEMD_UNIT_SRC/DST variables, and operator guidance

## Task Commits

Each task was committed atomically:

1. **Task 1: Create hardened systemd unit file** - `720da59` (feat)
2. **Task 2: Add install-systemd Makefile target** - `838575f` (feat)

## Files Created/Modified
- `deploy/systemd/cee-exporter.service` - Hardened systemd unit file for production Linux deployment
- `Makefile` - Added SYSTEMD_UNIT_SRC/DST variables, install-systemd to .PHONY, and install-systemd target

## Decisions Made
- Type=simple chosen over Type=notify because sd_notify integration is deferred to OBS-F02
- Wants=network-online.target (not Requires=) to avoid hard boot failure on systems without networkd
- EnvironmentFile uses leading dash prefix so a missing /etc/cee-exporter/env file is silently ignored
- Makefile target documented as requiring root (sudo make install-systemd) via comment

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

Before enabling the service, operators must:
1. Create the system user: `useradd --system --no-create-home --shell /usr/sbin/nologin cee-exporter`
2. Install the binary: `sudo install -m 755 cee-exporter /usr/local/bin/cee-exporter`
3. Create config dir: `sudo mkdir -p /etc/cee-exporter && sudo cp config.toml /etc/cee-exporter/`
4. Create log dir: `sudo mkdir -p /var/log/cee-exporter && sudo chown cee-exporter:cee-exporter /var/log/cee-exporter`
5. Install and enable: `sudo make install-systemd && sudo systemctl enable --now cee-exporter`

## Next Phase Readiness

- systemd unit file provides the Linux service foundation for Phase 5 (Windows Service) context
- deploy/systemd/ directory established for future deploy artifacts
- No blockers for remaining Phase 4 plans

## Self-Check: PASSED

All artifacts verified:
- deploy/systemd/cee-exporter.service: FOUND
- Makefile: FOUND
- 04-02-SUMMARY.md: FOUND
- Commit 720da59 (Task 1): FOUND
- Commit 838575f (Task 2): FOUND

---
*Phase: 04-observability-linux-service*
*Completed: 2026-03-03*
