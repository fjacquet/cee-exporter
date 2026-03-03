# ADR-010: Use kardianos/service v1.2.4 for Windows SCM integration

**Status:** accepted
**Supersedes:** [ADR-007](ADR-007-windows-svc-x-sys.md)

## Context

ADR-007 chose `golang.org/x/sys/windows/svc` and `svc/mgr` directly for Windows
Service Control Manager (SCM) integration on the premise that `kardianos/service` is a
thin wrapper that adds no functionality. Phase 5 research (2026-03-03) revised this
assessment after detailed examination of the library internals and the Windows SCM API
edge cases.

The research identified three significant gaps when using `x/sys` directly versus
`kardianos/service` v1.2.4:

1. **`SetRecoveryActionsOnNonCrashFailures`** — `kardianos/service` calls this
   automatically when `OnFailure` is configured. Without it, SCM only restarts on
   crashes (unexpected termination), not on clean-exit errors like `os.Exit(1)`. A
   Go process that exits with an error via `os.Exit(1)` reports `SERVICE_STOPPED`
   and does **not** trigger recovery unless this flag is set.

2. **`DelayedAutoStart`** — `kardianos/service` exposes this as a `KeyValue` option
   in `service.Config`. Without delayed start, Go services with large init chains
   (net/http, TLS, Prometheus) can exceed the SCM 30-second startup timeout during
   boot under resource contention (documented in golang/go #23479).

3. **Edge cases in `Install()`/`Uninstall()`** — `kardianos/service` handles SCM
   error codes for "service already exists", "service marked for deletion", and
   "insufficient privilege" gracefully. Hand-written code using `svc/mgr` directly
   must replicate this error handling.

The concern in ADR-007 about adding an unnecessary dependency was valid at the time,
but `kardianos/service` is now at v1.2.4 (July 2025) — actively maintained, used by
production projects (k0s, Datadog agent forks) — and the implementation savings
outweigh the dependency cost.

## Decision

Use `github.com/kardianos/service` v1.2.4 for Windows SCM integration.

Key configuration (in `service.Config.Option` `KeyValue`):

```go
service.KeyValue{
    "StartType":               "automatic",
    "DelayedAutoStart":        true,            // Avoids SCM 30s boot timeout
    "OnFailure":               "restart",
    "OnFailureDelayDuration":  "5s",
    "OnFailureResetPeriod":    86400,           // Reset failure count after 24h
}
```

The `runWithServiceManager` function in `cmd/cee-exporter/service_windows.go` handles:
- `install` subcommand → `s.Install()`
- `uninstall` subcommand → `s.Uninstall()`
- SCM context → `s.Run()` (calls `Start`/`Stop` methods)

The `Stop()` method cancels a `context.Context` passed into `run()` — this requires
`run()` to accept a `context.Context` argument (minor refactor from the Phase 4 stub).

## Consequences

- `github.com/kardianos/service v1.2.4` is added to `go.mod`. The library is pure Go,
  CGO-free, and has no transitive dependencies beyond `golang.org/x/sys` (already
  present).
- `SetRecoveryActionsOnNonCrashFailures(true)` is set automatically — service restarts
  on any failure including clean `os.Exit(1)` terminations.
- `DelayedAutoStart: true` prevents Event ID 7009 (SCM startup timeout) during boot.
- The `install` subcommand must strip itself from `Arguments` stored in the SCM
  registry to avoid an infinite install loop on boot.
- Service installation requires Administrator privileges; this must be documented
  and detected at install time with a helpful error message.
- The `service_notwindows.go` shim is unchanged — no impact on Linux or Docker builds.
