---
phase: 06-siem-writers
plan: "01"
subsystem: output
tags: [syslog, rfc5424, rfc6587, udp, tcp, crewjam, evtx, writer]

# Dependency graph
requires:
  - phase: 04-observability-linux-service
    provides: writer interface and WindowsEvent struct
  - phase: 02-build
    provides: CGO_ENABLED=0 cross-platform build constraint
provides:
  - SyslogWriter struct implementing evtx.Writer interface
  - buildSyslog5424 helper producing RFC 5424 bytes with audit@32473 SD-element
  - RFC 6587 octet-counting TCP framing in send() method
  - NewSyslogWriter constructor with connect() and reconnect-once resilience
  - crewjam/rfc5424 v0.1.0 as direct go.mod dependency
affects: [06-siem-writers-02, 07-evtx-writer, main-config-wiring]

# Tech tracking
tech-stack:
  added:
    - github.com/crewjam/rfc5424 v0.1.0 (cross-platform RFC 5424 message builder)
  patterns:
    - TDD Red-Green-Refactor cycle applied to writer implementation
    - Mirrors GELFWriter struct layout and reconnect-once pattern exactly
    - RFC 6587 §3.4.1 octet-counting: fmt.Fprintf(conn, "%d ", len(payload)) then Write(payload)
    - audit@32473 SD-ID for structured audit fields (EventID, User, Domain, Object, AccessMask, ClientAddr, CEPAType)
    - ProcessID uses NILVALUE "-" when WindowsEvent.ProcessID == 0

key-files:
  created:
    - pkg/evtx/writer_syslog.go
    - pkg/evtx/writer_syslog_test.go
  modified:
    - go.mod (added crewjam/rfc5424 v0.1.0 as direct dependency)
    - go.sum

key-decisions:
  - "Use crewjam/rfc5424 instead of stdlib log/syslog — log/syslog is excluded from Windows builds"
  - "audit@32473 SD-ID uses IANA example PEN (RFC 5612) for structured audit data element"
  - "ProcessID NILVALUE: emit '-' when ProcessID==0 to comply with RFC 5424 PROCID grammar"
  - "TCP framing: fmt.Fprintf then Write (two syscalls) keeps code readable vs single sprintf"
  - "SyslogConfig.AppName defaults to cee-exporter in constructor, matching GELF appname pattern"

patterns-established:
  - "SyslogWriter mirrors GELFWriter: cfg struct, mu sync.Mutex, conn net.Conn, connect/send/reconnect pattern"
  - "No build tags on writer_syslog.go: net and crewjam/rfc5424 are cross-platform"
  - "White-box test uses net.Pipe() for in-memory TCP framing test without real network"

requirements-completed: [OUT-03, OUT-04]

# Metrics
duration: 5min
completed: 2026-03-03
---

# Phase 06 Plan 01: SyslogWriter Summary

**RFC 5424 syslog writer over UDP/TCP using crewjam/rfc5424, with octet-counting TCP framing and audit@32473 structured-data element for CEPA audit events**

## Performance

- **Duration:** ~5 min
- **Started:** 2026-03-03T19:31:00Z
- **Completed:** 2026-03-03T19:33:00Z
- **Tasks:** 3 (RED + GREEN + REFACTOR)
- **Files modified:** 4

## Accomplishments

- Implemented SyslogWriter with RFC 5424 message construction via crewjam/rfc5424 library
- TCP transport uses RFC 6587 §3.4.1 octet-counting framing (length prefix before payload)
- UDP transport sends single datagram with no framing
- buildSyslog5424 emits audit@32473 structured-data with 7 audit fields per event
- WriteEvent reconnects once on failure, mirrors GELFWriter resilience pattern exactly
- All 59 tests pass across full test suite, make build and make build-windows both succeed

## Task Commits

Each task was committed atomically:

1. **Task 1: RED — Failing tests for buildSyslog5424 + TCP framing** - `436cc16` (test)
2. **Task 2: GREEN — SyslogWriter implementation** - `adc9dc3` (feat)
3. **Task 3: REFACTOR — go mod tidy, promote crewjam to direct dependency** - `879e1d7` (refactor)

_Note: TDD tasks have three commits (test → feat → refactor)_

## Files Created/Modified

- `pkg/evtx/writer_syslog.go` - SyslogWriter struct, NewSyslogWriter, connect, send, WriteEvent, Close, buildSyslog5424
- `pkg/evtx/writer_syslog_test.go` - TestBuildSyslog5424 and TestSyslogTCPFraming table-driven tests
- `go.mod` - Added github.com/crewjam/rfc5424 v0.1.0 as direct dependency
- `go.sum` - Updated checksums

## Decisions Made

- **crewjam/rfc5424 vs stdlib log/syslog:** stdlib log/syslog is excluded from Windows builds (`//go:build !windows` constraint) — crewjam/rfc5424 uses pure Go and works cross-platform. This matches the architectural decision recorded in STATE.md.
- **audit@32473 SD-ID:** Uses IANA example Private Enterprise Number (32473) per RFC 5612. This allows structured data without registering a real PEN for a reference implementation.
- **ProcessID NILVALUE:** RFC 5424 grammar requires PROCID be printable US-ASCII (33-126). When ProcessID==0 (zero value), emit "-" (NILVALUE) to avoid invalid "0" being rejected by strict parsers.
- **Two-write TCP framing:** `fmt.Fprintf(conn, "%d ", len(payload))` then `conn.Write(payload)` — two syscalls but keeps code clear. Alternatively could use a single formatted write but the pattern is more readable.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] go mod tidy after go get added spurious go-lumber indirect entry**

- **Found during:** Task 3 (REFACTOR)
- **Issue:** Running `go get github.com/crewjam/rfc5424@v0.1.0` initially added crewjam as `// indirect` instead of direct. Also left `elastic/go-lumber` as an indirect entry from cache.
- **Fix:** Ran `go mod tidy` which promoted crewjam to direct dependency and removed the spurious go-lumber entry. Note: go-lumber was subsequently re-added by the 06-02 beats commit that occurred concurrently.
- **Files modified:** go.mod, go.sum
- **Verification:** `grep "crewjam/rfc5424" go.mod` shows direct dependency without `// indirect` marker
- **Committed in:** `879e1d7` (refactor commit)

---

**Total deviations:** 1 auto-fixed (1 blocking/cleanup)
**Impact on plan:** Minor cleanup to go.mod — no scope creep.

## Issues Encountered

- During Task 3 verification, `go test ./pkg/evtx/` failed because 06-02's RED test (`writer_beats_test.go`) was committed concurrently with undefined `buildBeatsEvent`, `BeatsConfig`, and `BeatsWriter` symbols. This was the expected RED state for 06-02 (not a bug in 06-01). By the time verification completed, 06-02 had been fully implemented (`ca55154`) and all 59 tests pass.

## User Setup Required

None — no external service configuration required. SyslogWriter requires syslog receiver configuration in config.toml (host, port, protocol).

## Next Phase Readiness

- SyslogWriter implements evtx.Writer interface — ready for multi-writer fan-out wiring in main.go config
- 06-02 (BeatsWriter) already complete; both writers available for Phase 6 integration
- Config wiring (adding SyslogConfig to the TOML config struct and factory in main.go) is the remaining integration step

---
_Phase: 06-siem-writers_
_Completed: 2026-03-03_

## Self-Check: PASSED

- FOUND: pkg/evtx/writer_syslog.go
- FOUND: pkg/evtx/writer_syslog_test.go
- FOUND: .planning/phases/06-siem-writers/06-01-SUMMARY.md
- FOUND: commit 436cc16 (test RED)
- FOUND: commit adc9dc3 (feat GREEN)
- FOUND: commit 879e1d7 (refactor REFACTOR)
- FOUND: commit fc5eb7d (docs metadata)
- All 59 tests pass: go test ./... reports ok
- make build: PASS (CGO_ENABLED=0 linux/amd64)
- make build-windows: PASS (CGO_ENABLED=0 windows/amd64)
- make lint (go vet): exit code 0
