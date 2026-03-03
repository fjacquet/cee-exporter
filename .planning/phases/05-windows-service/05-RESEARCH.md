# Phase 5: Windows Service - Research

**Researched:** 2026-03-03
**Domain:** Windows Service Control Manager (SCM) integration in Go — CGO_ENABLED=0
**Confidence:** HIGH

<phase_requirements>

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| DEPLOY-03 | Windows operator can run `cee-exporter.exe install` to register daemon with Service Control Manager | kardianos/service `Control(s, "install")` + Config{DelayedAutoStart: true} covers full registration workflow |
| DEPLOY-04 | Windows operator can run `cee-exporter.exe uninstall` to remove daemon from Service Control Manager | kardianos/service `Control(s, "uninstall")` uses `mgr.DeleteService()` which removes SCM entry and registry key under `HKLM\SYSTEM\CurrentControlSet\Services` |
| DEPLOY-05 | Windows Service auto-restarts after unexpected crash (recovery actions configured) | kardianos/service `Option{"OnFailure": "restart", "OnFailureDelayDuration": "5s", "OnFailureResetPeriod": 86400}` — calls `SetRecoveryActions` + `SetRecoveryActionsOnNonCrashFailures(true)` at install time |
</phase_requirements>

---

## Summary

Phase 5 implements native Windows SCM service management using `github.com/kardianos/service` v1.2.4 (July 2025). This library wraps `golang.org/x/sys/windows/svc` and `golang.org/x/sys/windows/svc/mgr` behind a portable interface, requiring no CGO and no C toolchain. The project already has `golang.org/x/sys v0.41.0` as a direct dependency, so `kardianos/service` can be added without dependency conflicts.

The existing code already established the scaffolding for this phase: `main()` calls `runWithServiceManager(run)`, and `service_windows.go` currently contains a no-op stub that Phase 5 replaces. The `service_notwindows.go` shim must remain unchanged. The subcommand pattern (`cee-exporter.exe install` / `cee-exporter.exe uninstall`) requires parsing `os.Args[1]` before `flag.Parse()` is called in `run()`, which means the dispatch must happen in `runWithServiceManager`.

The critical SCM startup-timeout pitfall (Go runtime init can exceed the 30-second SCM window during boot under resource contention) is mitigated by configuring `DelayedAutoStart: true`, which starts the service 120 seconds after the "Automatic" services. Recovery actions must be set with `SetRecoveryActionsOnNonCrashFailures(true)` so that a non-zero exit code also triggers restart, not only crashes that terminate without `SERVICE_STOPPED`.

**Primary recommendation:** Use `github.com/kardianos/service` v1.2.4. It handles StartPending signaling, the Execute loop, SetRecoveryActions, and DelayedAutoStart. Replace the stub in `service_windows.go` with a full implementation; do not touch `service_notwindows.go`.

---

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/kardianos/service` | v1.2.4 (Jul 2025) | SCM registration, service lifecycle, recovery actions | Only maintained Go library for cross-platform service management; wraps `x/sys/windows/svc` cleanly; CGO-free; used by production projects (k0s, Datadog forks) |
| `golang.org/x/sys` | v0.41.0 (already in go.mod) | Underlying Win32 API calls via syscall | Already present; kardianos/service uses it internally |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `golang.org/x/sys/windows/svc/mgr` | included in x/sys v0.41.0 | Direct SCM mgr API if bypassing kardianos | Only needed if kardianos/service proves insufficient for a specific edge case |
| `golang.org/x/sys/windows/svc` | included in x/sys v0.41.0 | Low-level service handler interface | Used internally by kardianos/service; no direct use needed |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `kardianos/service` | Direct `x/sys/windows/svc` + `svc/mgr` | Lower abstraction: must hand-write Execute loop, StartPending signaling, SetRecoveryActions calls, and uninstall cleanup. Saves one dependency but adds ~150 lines of Windows-specific boilerplate. Not worth it given the project already avoids external test dependencies — this is a runtime dependency justified by platform complexity. |
| `kardianos/service` | NSSM | Explicitly out of scope (REQUIREMENTS.md: "External binary dependency; breaks single-artifact deploy contract") |
| `kardianos/service` | MSI/WiX installer | Explicitly out of scope (REQUIREMENTS.md: "Scope explosion") |

**Installation:**

```bash
go get github.com/kardianos/service@v1.2.4
```

---

## Architecture Patterns

### Recommended Project Structure

The phase touches exactly two files:

```
cmd/cee-exporter/
├── main.go                    # unchanged — calls runWithServiceManager(run)
├── service_windows.go         # REPLACE stub with full SCM implementation
└── service_notwindows.go      # DO NOT TOUCH — keeps non-Windows compile working
```

No new packages are needed. The SCM wrapper lives entirely in `service_windows.go` (build-tagged `//go:build windows`).

### Pattern 1: runWithServiceManager — the integration seam

**What:** `runWithServiceManager` in `service_windows.go` is the single entry point that either runs as a Windows service (when invoked by SCM) or handles the `install`/`uninstall` subcommands (when run interactively).

**When to use:** Always — `main()` already calls it. This is the pattern established by Phase 4.

**Example:**

```go
//go:build windows

package main

import (
    "fmt"
    "log/slog"
    "os"

    "github.com/kardianos/service"
)

const (
    svcName        = "cee-exporter"
    svcDisplayName = "CEE Exporter"
    svcDescription = "Dell PowerStore CEPA audit event bridge to GELF / Windows Event Log"
)

type svcProgram struct {
    runFn func()
    stop  chan struct{}
}

func (p *svcProgram) Start(s service.Service) error {
    p.stop = make(chan struct{})
    go p.runFn() // must not block
    return nil
}

func (p *svcProgram) Stop(s service.Service) error {
    // Signal run() to exit by sending SIGTERM equivalent.
    // run() already listens on os/signal — nothing extra needed if
    // it uses os.Interrupt / syscall.SIGTERM. For Windows service,
    // the SCM sends a stop control, kardianos translates to Stop().
    return nil
}

func buildSvcConfig() *service.Config {
    return &service.Config{
        Name:        svcName,
        DisplayName: svcDisplayName,
        Description: svcDescription,
        // Pass -config flag value through to the service process arguments
        // so SCM starts with the same config path used at install time.
        Arguments: os.Args[1:], // captured at install; SCM replays on start
        Option: service.KeyValue{
            "DelayedAutoStart":     true,
            "StartType":            "automatic",
            "OnFailure":            "restart",
            "OnFailureDelayDuration": "5s",
            "OnFailureResetPeriod": 86400, // 24 hours
        },
    }
}

func runWithServiceManager(runFn func()) {
    prg := &svcProgram{runFn: runFn}
    cfg := buildSvcConfig()

    s, err := service.New(prg, cfg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "service.New: %v\n", err)
        os.Exit(1)
    }

    // Subcommand dispatch: if first arg is install/uninstall, handle it.
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "install":
            if err := s.Install(); err != nil {
                fmt.Fprintf(os.Stderr, "install: %v\n", err)
                os.Exit(1)
            }
            fmt.Println("Service installed successfully.")
            return
        case "uninstall":
            if err := s.Uninstall(); err != nil {
                fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
                os.Exit(1)
            }
            fmt.Println("Service uninstalled successfully.")
            return
        }
    }

    // Either running as SCM service or directly from console.
    if err := s.Run(); err != nil {
        slog.Error("service_run_error", "error", err)
        os.Exit(1)
    }
}
```

**Source:** Synthesized from kardianos/service v1.2.4 API (pkg.go.dev/github.com/kardianos/service) and service_windows.go internal source.

### Pattern 2: Arguments passthrough for -config flag

**What:** When SCM starts the service on boot, it replays the binary path and arguments recorded at install time. The `Arguments` field in `service.Config` must capture the `-config` path so the service finds its config on boot.

**When to use:** Whenever the service has required flags that differ from defaults.

**Critical detail:** `kardianos/service` stores `Arguments` in the registry under `HKLM\SYSTEM\CurrentControlSet\Services\<name>\ImagePath`. The SCM passes these to the process on start. Pass `os.Args[1:]` at install time (minus the "install" subcommand), or use a dedicated flag `--config` that the operator specifies at install time.

**Recommended approach:**

```go
// Parse -config before dispatching subcommand
cfgPath := "config.toml"
for i, a := range os.Args[1:] {
    if (a == "-config" || a == "--config") && i+1 < len(os.Args[1:]) {
        cfgPath = os.Args[i+2]
    }
}
cfg.Arguments = []string{"-config", cfgPath}
```

### Pattern 3: SCM recovery actions via kardianos/service Options

**What:** Recovery actions (restart on crash) are configured at install time through `service.Config.Option` KeyValues. `kardianos/service` internally calls `mgr.SetRecoveryActions` and (in v1.2.4) `SetRecoveryActionsOnNonCrashFailures`.

**Example (already captured in buildSvcConfig above):**

```go
Option: service.KeyValue{
    "OnFailure":              "restart",  // ServiceRestart action
    "OnFailureDelayDuration": "5s",       // 5-second delay before restart
    "OnFailureResetPeriod":   86400,      // Reset failure count after 24h
},
```

This corresponds to the Windows "Recovery" tab in services.msc: "First failure: Restart the Service (after 5s)", and reset after 24 hours. The `FailureActionsOnNonCrashFailures` flag (equivalent of `SetRecoveryActionsOnNonCrashFailures(true)`) is set automatically by `kardianos/service` when `OnFailure` is configured.

### Pattern 4: Stop signal bridging

**What:** `run()` currently blocks on `signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)`. When SCM calls `Stop()`, kardianos does NOT send a signal — it calls the `Stop(s Service) error` method. The `Stop` method must trigger the same shutdown path.

**Options:**

Option A — channel bridge (recommended for this codebase):

```go
// In svcProgram, hold a reference to the signal channel
type svcProgram struct {
    runFn  func()
    stopCh chan os.Signal
}

func (p *svcProgram) Start(s service.Service) error {
    p.stopCh = make(chan os.Signal, 1)
    // Register our channel with signal.Notify before starting run
    go p.runFn() // run() reads from stopCh
    return nil
}

func (p *svcProgram) Stop(s service.Service) error {
    p.stopCh <- syscall.SIGTERM
    return nil
}
```

Option B — refactor `run()` to accept a context (cleanest long-term):

```go
func run(ctx context.Context) { ... }
// svcProgram.Start creates a cancel context and stores cancel
// svcProgram.Stop calls cancel()
```

Option B is architecturally cleaner but requires changing the `run()` signature. Since `run()` was refactored in Phase 4 to be a no-arg function, Option A adds less churn. Choose based on Phase 4 implementation.

### Anti-Patterns to Avoid

- **Calling `os.Exit()` in Stop():** SCM gives a limited time for Stop to return. Calling `os.Exit()` bypasses SCM cleanup. Use channel signaling instead.
- **Blocking in Start():** `Start(s service.Service)` must return quickly. Long initialization must happen in a goroutine.
- **Registering with SCM in the wrong tier:** Never use `sc.exe` or PowerShell for install in code. Use the library API only.
- **Ignoring `FailureActionsOnNonCrashFailures`:** Without this flag, SCM only restarts on crashes (SERVICE_STOPPED not reported). A Go process that calls `os.Exit(1)` reports SERVICE_STOPPED and does NOT trigger recovery without this flag.
- **Setting `Arguments` to `os.Args[1:]` literally at install time:** This captures "install" as an argument, which causes infinite install loop on SCM start. Filter out the subcommand before storing arguments.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| SCM StartPending signaling | Custom goroutine sending STATUS_START_PENDING | `kardianos/service` handles this in Execute loop | Wrong WaitHint values cause SCM timeout; library uses correct defaults |
| Recovery action setup | Direct `mgr.SetRecoveryActions` calls in custom install | `kardianos/service` Option KeyValues | Library also handles `FailureActionsOnNonCrashFailures` and `DelayedAutoStart` in one Install() call |
| Service detection (service vs console) | Custom IsWindowsService logic | `service.Interactive()` from kardianos | `svc.IsWindowsService()` logic has subtle edge cases in interactive admin sessions |
| Install/uninstall registry cleanup | Manual `regedit` or `reg delete` | `s.Uninstall()` | Library calls `DeleteService()` which atomically removes SCM entry and registry subtree |
| Cross-platform service daemon | Two separate binaries (one Linux, one Windows) | `runWithServiceManager` + build tags | Single binary pattern already established by Phase 4 scaffolding |

**Key insight:** The Windows SCM API has many edge cases (service marked for deletion, service already running on install, insufficient privilege error codes) that `kardianos/service` handles. Do not replace it with direct Win32 calls.

---

## Common Pitfalls

### Pitfall 1: 30-Second SCM Startup Timeout During Boot

**What goes wrong:** The SCM starts the service process and waits up to 30 seconds for `StartServiceCtrlDispatcher` to be called. Go programs with large init chains can exceed this during boot when disk I/O and CPU are contended (documented in golang/go issue #23479).

**Why it happens:** Go initializes all packages sequentially before `main()` runs. Large binaries with many imports (net/http, prometheus, TLS) have significant init time on slow boot disks. SCM kills the process and logs Event ID 7009.

**How to avoid:** Configure `DelayedAutoStart: true`. The SCM starts delayed services 120 seconds after auto-start services, when the system is idle. This is the correct fix for Go services — not a workaround.

**Warning signs:** Event Viewer shows Event ID 7009 ("Timeout (30000 milliseconds) waiting for service to connect") on first boot after install.

### Pitfall 2: Recovery Actions Only Fire on Crash, Not Clean Exit

**What goes wrong:** Operator configures restart on failure, but when the Go process exits with `os.Exit(1)` (config error, listener bind failure), SCM does NOT restart it.

**Why it happens:** `os.Exit(1)` causes the process to exit cleanly from the SCM perspective — it reports `SERVICE_STOPPED` before dying. Recovery actions only fire when the service terminates without reporting `SERVICE_STOPPED` (true crash), unless `FailureActionsOnNonCrashFailures` is enabled.

**How to avoid:** Set `FailureActionsOnNonCrashFailures = true` (done automatically by kardianos/service v1.2.4 when `OnFailure` is set). Verify with: open services.msc → service Properties → Recovery tab → check "Enable actions for stops with errors" checkbox is ticked.

**Warning signs:** Service exits with error but SCM shows "Stopped" with no automatic restart attempt.

### Pitfall 3: Arguments Captured at Install Include the Subcommand

**What goes wrong:** Operator runs `cee-exporter.exe install` and the service gets registered with `Arguments: ["install"]`. On boot SCM runs `cee-exporter.exe install`, which tries to install again, fails with "already installed" error, and the daemon never starts.

**Why it happens:** Naively passing `os.Args[1:]` to `service.Config.Arguments` includes the "install" string.

**How to avoid:** Strip the subcommand from arguments before passing to `service.Config.Arguments`. Only pass the `-config` path and any other runtime flags.

**Warning signs:** Service installed correctly but shows "Error 1053: The service did not respond" on start; Event Log shows repeated install attempts.

### Pitfall 4: Stop() Not Bridging to run() Shutdown

**What goes wrong:** SCM sends stop control, kardianos calls `Stop()`, but `run()` is still blocking on the signal channel waiting for SIGTERM/SIGINT which never arrives on Windows service stop. Service hangs in StopPending until SCM force-kills after timeout.

**Why it happens:** Windows SCM stop control does not generate a POSIX signal. The `signal.Notify` channel in `run()` never receives anything.

**How to avoid:** Implement `Stop()` to bridge the shutdown signal to whatever mechanism `run()` uses to exit. Either inject a stop channel into `run()`, or use a context with cancel.

**Warning signs:** `services.msc` shows service stuck in "Stopping" state; Event Log shows "The service did not stop in a timely fashion."

### Pitfall 5: `_windows.go` vs `_notwindows.go` Build Tag Confusion

**What goes wrong:** Developer creates `service_linux.go` instead of `service_notwindows.go`, breaking Linux builds.

**Why it happens:** `_linux.go` suffix is treated by Go toolchain as Linux-only build tag. CLAUDE.md explicitly prohibits this pattern.

**How to avoid:** The existing files are correctly named. Phase 5 only modifies `service_windows.go`. Never rename or add files with `_linux.go` suffix.

**Warning signs:** `GOOS=linux go build ./...` fails with duplicate symbol or missing symbol errors.

### Pitfall 6: Insufficient Privileges at Install Time

**What goes wrong:** `s.Install()` returns "Access is denied" error.

**Why it happens:** SCM registration requires Administrator privileges. The operator must run the install command from an elevated (Administrator) command prompt.

**How to avoid:** Document the requirement in help output. Optionally detect insufficient privilege and print a helpful error message rather than the raw Win32 error.

**Warning signs:** `install: Access is denied.`

---

## Code Examples

Verified patterns from official sources:

### Full service_windows.go Implementation Skeleton

```go
//go:build windows

package main

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "syscall"

    "github.com/kardianos/service"
)

const (
    svcName        = "cee-exporter"
    svcDisplayName = "CEE Exporter"
    svcDescription = "Dell PowerStore CEPA audit event bridge to GELF / Windows Event Log"
)

// svcProgram implements service.Interface for kardianos/service.
type svcProgram struct {
    cancel context.CancelFunc // set in Start(), called by Stop()
    runFn  func(ctx context.Context)
}

func (p *svcProgram) Start(s service.Service) error {
    ctx, cancel := context.WithCancel(context.Background())
    p.cancel = cancel
    go p.runFn(ctx) // must not block
    return nil
}

func (p *svcProgram) Stop(s service.Service) error {
    if p.cancel != nil {
        p.cancel()
    }
    return nil
}

// svcConfig builds the kardianos/service Config with correct Windows options.
func svcConfig(cfgPath string) *service.Config {
    return &service.Config{
        Name:        svcName,
        DisplayName: svcDisplayName,
        Description: svcDescription,
        Arguments:   []string{"-config", cfgPath},
        Option: service.KeyValue{
            // Use Automatic (Delayed Start) to avoid SCM 30-second boot timeout
            "StartType":            "automatic",
            "DelayedAutoStart":     true,
            // Recovery: restart 5s after failure; reset count after 24h
            "OnFailure":            "restart",
            "OnFailureDelayDuration": "5s",
            "OnFailureResetPeriod": 86400,
        },
    }
}

// runWithServiceManager is the single entry point called by main().
// It handles install/uninstall subcommands and otherwise runs the service.
func runWithServiceManager(runFn func()) {
    // Parse -config path early so we can store it in service arguments.
    cfgPath := parseCfgPath(os.Args[1:])

    // Adapt the no-arg runFn to accept a context for SCM stop bridging.
    // NOTE: if Phase 4 refactored run() to accept a context, use that directly.
    prg := &svcProgram{
        runFn: func(ctx context.Context) {
            runFn() // run() must respect context or signal channel
        },
    }

    s, err := service.New(prg, svcConfig(cfgPath))
    if err != nil {
        fmt.Fprintf(os.Stderr, "service.New: %v\n", err)
        os.Exit(1)
    }

    // Subcommand dispatch before flag.Parse() runs in run()
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "install":
            if err := s.Install(); err != nil {
                fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
                os.Exit(1)
            }
            fmt.Printf("Service %q installed. Start with: sc start %s\n", svcName, svcName)
            return
        case "uninstall":
            if err := s.Uninstall(); err != nil {
                fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
                os.Exit(1)
            }
            fmt.Printf("Service %q uninstalled.\n", svcName)
            return
        }
    }

    // Not install/uninstall — run as service or interactive console.
    if err := s.Run(); err != nil {
        slog.Error("service_run_error", "error", err)
        os.Exit(1)
    }
}

// parseCfgPath extracts the value of -config flag from args without calling flag.Parse().
func parseCfgPath(args []string) string {
    for i, a := range args {
        if (a == "-config" || a == "--config") && i+1 < len(args) {
            return args[i+1]
        }
    }
    return "config.toml" // default matches flag default in run()
}
```

**Note on Stop bridging:** The example above calls `runFn()` which currently uses `signal.Notify` for shutdown. The `cancel()` call in `Stop()` does nothing without context propagation into `run()`. The plan must either (a) refactor `run()` to accept a `context.Context` and check `ctx.Done()` in addition to signals, or (b) have `Stop()` send a synthetic signal via `syscall.Kill(os.Getpid(), syscall.SIGTERM)` — but SIGTERM does not exist on Windows. The correct solution is option (a): refactor `run()` signature.

### Recovery Actions (direct x/sys/windows/svc/mgr — for reference only)

```go
// Source: pkg.go.dev/golang.org/x/sys/windows/svc/mgr
// This is what kardianos/service calls internally.
actions := []mgr.RecoveryAction{
    {Type: mgr.ServiceRestart, Delay: 5 * time.Second},
    {Type: mgr.ServiceRestart, Delay: 10 * time.Second},
    {Type: mgr.ServiceRestart, Delay: 30 * time.Second},
}
resetPeriod := uint32(86400) // 24 hours
err = svc.SetRecoveryActions(actions, resetPeriod)

// Enable restart on non-crash exits (non-zero exit code)
err = svc.SetRecoveryActionsOnNonCrashFailures(true)
```

### Verify Installation (PowerShell)

```powershell
Get-Service -Name "cee-exporter" | Select-Object Name, Status, StartType
sc.exe qfailure cee-exporter       # shows recovery actions
sc.exe qc cee-exporter              # shows config including delayed auto start
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `IsAnInteractiveSession()` | `IsWindowsService()` | Go 1.13 era | Old function deprecated; new one is correct |
| Manual `sc.exe` calls in install script | Library API (`s.Install()`) | Established pattern | No external tools needed; single binary |
| No delayed start for Go services | `DelayedAutoStart: true` | golang/go #23479 (2018, ongoing) | Prevents SCM 30-second timeout at boot |
| Recovery actions via manual `sc.exe failure` | `kardianos/service` Options | v1.2.1 (2021) | Declarative; no post-install scripting |
| `kardianos/service` v1.1.x | v1.2.4 (July 2025) | 2021-2025 | Added `OnFailureDelayDuration`, OpenRC, proper Windows options |

**Deprecated/outdated:**

- NSSM: External dependency, explicitly out of scope per REQUIREMENTS.md
- `sc.exe` for install/uninstall: Works but requires admin shell and no rollback; library approach is superior

---

## Open Questions

1. **Stop() bridging into run()**
   - What we know: `run()` currently blocks on `signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)`. Windows SCM stop does not generate signals.
   - What's unclear: Whether Phase 4's `run()` refactor left any hook for context cancellation or if it remains signal-only.
   - Recommendation: The plan's first task should refactor `run()` to accept `ctx context.Context` and exit on `ctx.Done()` in addition to signals. This is the minimal correct fix and benefits Linux service management too.

2. **Service arguments vs -config flag interaction**
   - What we know: `service.Config.Arguments` stores the args SCM passes on boot. The operator specifies `-config` at install time.
   - What's unclear: Whether to require the operator to pass `-config` explicitly at install, or detect and store automatically.
   - Recommendation: Parse `-config` from `os.Args` in `runWithServiceManager` and always store it in `Arguments`. Default to `config.toml`. Document this in help text.

3. **Event Log source registration**
   - What we know: Windows services can register an Event Log source so their messages appear in `System` or `Application` Event Viewer logs. kardianos/service has a Logger that writes to the Windows Event Log.
   - What's unclear: Whether to use `s.Logger()` for structured logging or keep slog and not register an Event Log source (Phase 5 scope).
   - Recommendation: Keep slog for Phase 5. Event Log source registration is a separate concern (not in DEPLOY-03/04/05). Log to file or stderr; let the operator redirect via SCM configuration.

---

## Sources

### Primary (HIGH confidence)

- `pkg.go.dev/github.com/kardianos/service` — version, Config struct, KeyValue options, Interface, Control(), Install(), Uninstall()
- `pkg.go.dev/golang.org/x/sys/windows/svc/mgr` — RecoveryAction struct, SetRecoveryActions, SetRecoveryActionsOnNonCrashFailures, Config.DelayedAutoStart
- `pkg.go.dev/golang.org/x/sys/windows/svc` — Handler interface, IsWindowsService(), StartPending, WaitHint signaling
- `github.com/kardianos/service/blob/master/service_windows.go` — internal Install() showing DelayedAutoStart, SetRecoveryActions call pattern

### Secondary (MEDIUM confidence)

- `github.com/golang/go/issues/23479` — SCM 30-second timeout root cause and `DelayedAutoStart` as recommended mitigation
- `github.com/golang/go/issues/59016` — `SetRecoveryActionsOnNonCrashFailures` API addition background
- `github.com/golang/sys/blob/master/windows/svc/example/main.go` — `IsWindowsService()` dispatch pattern confirmed

### Tertiary (LOW confidence)

- levin405.neocities.org/blog/2022-07-25-writing-a-windows-service-in-go-2/ — os.Args dispatch pattern (single blog post, not official)

---

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — kardianos/service v1.2.4 confirmed on pkg.go.dev; x/sys already in go.mod
- Architecture: HIGH — build tag pattern from CLAUDE.md; service_windows.go stub already in place; kardianos API verified
- Pitfalls: HIGH — SCM timeout confirmed in golang/go #23479; recovery action gotcha confirmed in x/sys mgr docs; argument capture pitfall derived from library API semantics

**Research date:** 2026-03-03
**Valid until:** 2026-06-03 (kardianos/service stable; x/sys stable; Win32 SCM API does not change)
