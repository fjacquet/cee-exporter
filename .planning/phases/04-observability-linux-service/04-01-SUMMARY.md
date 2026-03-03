---
phase: 04-observability-linux-service
plan: 01
subsystem: observability
tags: [prometheus, metrics, http, go, atomics, telemetry]

# Dependency graph
requires:
  - phase: pkg/metrics
    provides: atomic counters (EventsReceivedTotal, EventsDroppedTotal, WriterErrorsTotal, EventsWrittenTotal, QueueDepth) that the Prometheus handler reads

provides:
  - pkg/prometheus/handler.go — NewMetricsHandler() and Serve(addr) with private registry and 5 cee_* metrics
  - pkg/prometheus/handler_test.go — unit test verifying all 5 metric values in scrape output

affects:
  - 04-02 (systemd/linux service — may expose /metrics via Serve() in main)
  - 05-windows-service (may wire Serve() into service lifecycle)
  - cmd/cee-exporter/main.go (future: start metrics server goroutine)

# Tech tracking
tech-stack:
  added:
    - github.com/prometheus/client_golang v1.23.2
    - github.com/prometheus/common v0.66.1 (transitive)
    - github.com/prometheus/procfs v0.16.1 (transitive)
    - google.golang.org/protobuf v1.36.8 (transitive)
  patterns:
    - Private prometheus.Registry (no Go runtime metrics in output)
    - CounterFunc/GaugeFunc callbacks reading only from pkg/metrics.M atomics (no I/O in scrape path)
    - Dedicated http.ServeMux for metrics (not DefaultServeMux)

key-files:
  created:
    - pkg/prometheus/handler.go
    - pkg/prometheus/handler_test.go
  modified:
    - go.mod (prometheus/client_golang v1.23.2 added as direct dep, transitive deps added)
    - go.sum (hashes for all new deps)

key-decisions:
  - "Used prometheus.NewRegistry() (private) not DefaultRegisterer — keeps scrape output clean (cee_* only, no go_gc_/go_goroutines_)"
  - "Package named ceeprometheus to avoid import collision with github.com/prometheus/client_golang/prometheus"
  - "Serve() uses dedicated http.ServeMux — metrics endpoint isolated from CEPA handler on port 12228"
  - "Added cee_events_written_total as bonus metric — enables success-rate PromQL queries at zero extra cost"

patterns-established:
  - "Metrics scrape callbacks: call .Load() or .QueueDepth() ONLY — no I/O, no mutex, no network in callback"
  - "Private Prometheus registry pattern for application-specific metrics endpoints"

requirements-completed: [OBS-01, OBS-02, OBS-03, OBS-04, OBS-05]

# Metrics
duration: 2min
completed: 2026-03-03
---

# Phase 4 Plan 01: Prometheus /metrics Handler Summary

**Private prometheus.Registry exposing 5 cee_* counters/gauges via NewMetricsHandler() and Serve(), backed by pkg/metrics atomics with no I/O in scrape callbacks**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-03T14:37:02Z
- **Completed:** 2026-03-03T14:39:00Z
- **Tasks:** 3
- **Files modified:** 4

## Accomplishments
- Added prometheus/client_golang v1.23.2 as direct dependency (CGO_ENABLED=0 compatible; v1.23.2 avoids older transitive deps that broke cross-compilation)
- Implemented pkg/prometheus/handler.go with private registry, 4 required metrics + 1 bonus (cee_events_written_total)
- All callbacks read exclusively from pkg/metrics.M atomics (zero I/O in scrape goroutine)
- Unit test verifies all 5 metric values and absence of Go runtime metrics — passes with PASS

## Task Commits

Each task was committed atomically:

1. **Task 1: Add prometheus/client_golang dependency** - `266d23b` (chore)
2. **Task 2: Implement pkg/prometheus/handler.go** - `75ba14d` (feat)
3. **Task 3: Write handler unit test** - `6bc9b7c` (test)

## Files Created/Modified
- `pkg/prometheus/handler.go` - NewMetricsHandler() (private registry, 5 metrics) and Serve(addr string) (dedicated mux)
- `pkg/prometheus/handler_test.go` - TestMetricsHandler_AllRequiredMetrics: seeds atomics, scrapes, verifies values and no go_gc_ metrics
- `go.mod` - prometheus/client_golang v1.23.2 added as direct dep; transitive deps (common, procfs, protobuf) added
- `go.sum` - hashes for all new dependencies

## Decisions Made
- Used prometheus.NewRegistry() (private) instead of DefaultRegisterer: keeps scrape output to cee_* metrics only, no Go runtime noise
- Named package `ceeprometheus` to avoid import collision with `github.com/prometheus/client_golang/prometheus`
- Serve() uses its own http.ServeMux to isolate the /metrics endpoint from the CEPA HTTP handler
- Added cee_events_written_total as a bonus CounterFunc: enables success-rate PromQL queries (received - written) at zero extra implementation cost

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Ran go mod tidy after writing handler.go to fetch transitive deps**
- **Found during:** Task 2 (handler.go compilation)
- **Issue:** Build failed with "missing go.sum entry" for 7 transitive dependencies of prometheus/client_golang (common, procfs, protobuf, etc.)
- **Fix:** Ran `go mod tidy` to resolve and add all transitive dependency hashes to go.sum
- **Files modified:** go.mod, go.sum
- **Verification:** `CGO_ENABLED=0 go build ./...` succeeds
- **Committed in:** 75ba14d (Task 2 commit, go.mod/go.sum included)

---

**Total deviations:** 1 auto-fixed (1 blocking — missing transitive dep hashes)
**Impact on plan:** Required fix to unblock compilation. No scope creep. All planned work delivered exactly as specified.

## Issues Encountered
- go mod tidy removed prometheus/client_golang from go.mod during Task 1 (no code imported it yet); re-added via `go get` then became permanent once handler.go was written and `go mod tidy` ran again with actual imports.

## User Setup Required
None - no external service configuration required. The /metrics endpoint will be wired into main.go in a subsequent plan.

## Next Phase Readiness
- pkg/prometheus is ready to be imported from cmd/cee-exporter/main.go
- Serve() needs to be called as a goroutine in main() on port 9228 (per ADR decision)
- Phase 04-02 (systemd/linux service) may add the goroutine startup; Phase 05 (Windows service) will do the same on Windows
- All 5 requirements (OBS-01 through OBS-05) fulfilled by this plan

---
*Phase: 04-observability-linux-service*
*Completed: 2026-03-03*
