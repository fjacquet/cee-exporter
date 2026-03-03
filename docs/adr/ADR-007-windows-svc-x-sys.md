# ADR-007: Use golang.org/x/sys/windows/svc for Windows Service registration

**Status:** superseded by [ADR-010](ADR-010-kardianos-service-windows-scm.md)

## Context

v2.0 adds native Windows Service support so operators can register cee-exporter with
the Windows Service Control Manager (SCM) instead of using NSSM or manual startup.

Three implementation approaches were evaluated:

1. **`golang.org/x/sys/windows/svc` + `svc/mgr`** — the official Go extended stdlib
   packages for Windows service lifecycle and SCM management.
2. **`github.com/kardianos/service`** — a cross-platform wrapper that delegates to
   `x/sys/windows/svc` on Windows and to systemd/launchd on other platforms.
3. **NSSM (Non-Sucking Service Manager)** — an external executable that wraps any
   binary as a Windows Service.

Arguments against option 2 (`kardianos/service`):

- `kardianos/service` is a thin wrapper around `golang.org/x/sys/windows/svc`. It
  adds no functionality that the underlying package does not already provide.
- `x/sys` is **already in `go.mod`** (v0.31.0) as a dependency of the existing
  Win32 EventLog writer. Using `kardianos/service` adds a new transitive dependency
  for zero functional gain.
- The cross-platform abstraction (Linux/macOS service support) is not a goal for
  cee-exporter: systemd integration on Linux is handled by a static unit file
  (ADR-implicit), and macOS is not a supported platform.

Arguments against option 3 (NSSM):

- Breaks the single-artifact deployment contract: the binary is no longer
  self-sufficient; operators must install NSSM separately.
- Not reproducible in CI/CD pipelines that build and package the daemon.

## Decision

Use `golang.org/x/sys/windows/svc` and `golang.org/x/sys/windows/svc/mgr` directly.

The `main` binary gains `install`, `uninstall`, `start`, `stop`, and `status`
subcommands implemented in a `cmd/cee-exporter/service_windows.go` build-tag file.
At startup, `svc.IsWindowsService()` detects SCM context and delegates to
`svc.Run("cee-exporter", handler)`.

## Consequences

- No new external dependencies added to `go.mod`.
- All SCM code is confined to `_windows.go` build-tag files; Linux build is unaffected.
- `golang.org/x/sys` is an official Go project; breaking changes follow the Go
  compatibility promise.
- Service installation requires Administrator privileges (`mgr.Connect()` needs them);
  this must be documented clearly.
- Recovery actions (auto-restart on failure) are set via `mgr.RecoveryActions` with a
  5-second delay — equivalent to `Restart=on-failure, RestartSec=5s` in systemd.
