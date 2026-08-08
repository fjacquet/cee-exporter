---
phase: 05-windows-service
verified: 2026-03-03T17:00:00Z
status: human_needed
score: 4/4 must-haves verified (automated); 4 items require Windows hardware
re_verification: false
human_verification:
  - test: "Run `cee-exporter.exe install` as Administrator on a Windows machine"
    expected: "Service 'CEE Exporter' appears in services.msc with Startup Type 'Automatic (Delayed Start)'"
    why_human: "Cannot invoke Windows SCM from Linux CI; requires real Windows environment with Administrator privileges"
  - test: "Run `cee-exporter.exe uninstall` after install on a Windows machine"
    expected: "Service disappears from services.msc; `reg query HKLM\\SYSTEM\\CurrentControlSet\\Services\\cee-exporter` returns 'ERROR: The system was unable to find the specified registry key or value.'"
    why_human: "Registry cleanup and SCM deregistration require live Windows SCM; not verifiable cross-platform"
  - test: "Start service via SCM and observe startup timing"
    expected: "Service transitions from StartPending to Running within the SCM 30-second window; no 'Error 1053' timeout event in Windows Event Viewer"
    why_human: "Delayed start and SCM handshake timing require Windows runtime; goroutine launch in Start() is the critical mechanism (verified in code)"
  - test: "Verify recovery actions in services.msc after install"
    expected: "Properties > Recovery tab shows: 'First failure: Restart the Service', delay 5 seconds; 'Enable actions for stops with errors' checkbox is ticked"
    why_human: "Recovery action registration uses Win32 SetRecoveryActions API called by kardianos/service; requires Windows to confirm SCM applied the settings correctly"
---

# Phase 5: Windows Service Verification Report

**Phase Goal:** Windows operators can register, start, and recover cee-exporter as a native SCM-managed service without external tools
**Verified:** 2026-03-03T17:00:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

All automated checks pass. The implementation is substantive, fully wired, and cross-compiles cleanly. Four success criteria require Windows hardware for final confirmation because they involve live Windows SCM/registry interactions that cannot be emulated cross-platform.

### Observable Truths (from Phase Success Criteria)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | `cee-exporter.exe install` registers service in services.msc with Automatic Delayed Start | ? HUMAN | Code verified: `s.Install()` called with `DelayedAutoStart: true`, `StartType: "automatic"` in `svcConfig()`; SCM outcome requires Windows hardware |
| 2 | `cee-exporter.exe uninstall` removes service with no leftover registry entries | ? HUMAN | Code verified: `s.Uninstall()` called on SCM handle; registry cleanup requires Windows hardware |
| 3 | Service starts within SCM 30-second window (StartPending before Go runtime completes) | ? HUMAN | Code verified: `Start()` launches `go p.runFn(ctx)` in goroutine and returns `nil` immediately — correct non-blocking pattern; SCM timeout requires Windows runtime to confirm |
| 4 | Crash triggers SCM auto-restart per configured recovery actions | ? HUMAN | Code verified: `OnFailure: "restart"`, `OnFailureDelayDuration: "5s"`, `OnFailureResetPeriod: 86400` all present; actual SCM restart requires Windows hardware |

**Score:** 4/4 truths are code-verified; all 4 require human confirmation on Windows hardware

### Supporting Truth Coverage (from Plan must_haves)

| Plan | Truth | Status | Evidence |
|------|-------|--------|----------|
| 05-01 | kardianos/service v1.2.4 in go.mod as direct dependency | VERIFIED | `go.mod` line 7: `github.com/kardianos/service v1.2.4` (direct block, not indirect) |
| 05-01 | `run()` accepts `context.Context` parameter | VERIFIED | `cmd/cee-exporter/main.go:119`: `func run(ctx context.Context)` |
| 05-01 | `run()` exits on `ctx.Done()` in addition to SIGTERM/SIGINT | VERIFIED | `main.go:235`: `case <-ctx.Done():` inside shutdown select |
| 05-01 | `service_notwindows.go` passes `context.Background()` into `run()` | VERIFIED | File line 11: `runFn(context.Background())` |
| 05-01 | CGO_ENABLED=0 builds succeed for linux/amd64 and windows/amd64 | VERIFIED | Both cross-compilation commands exit 0 |
| 05-01 | `go test ./...` passes | VERIFIED | 44/44 tests pass, 9 packages |
| 05-02 | `parseCfgPath` exists with correct pure-Go implementation, no build tag | VERIFIED | `service_helpers.go`: no `//go:build` tag; correct loop-based parsing |
| 05-02 | `TestParseCfgPath` covers 7 table-driven cases | VERIFIED | `service_helpers_test.go`: 7 sub-tests, all pass |
| 05-03 | `service_windows.go` implements full SCM wrapper (not stub) | VERIFIED | 110 lines; `svcProgram`, `svcConfig`, `runWithServiceManager` all present |
| 05-03 | `Start()` launches `runFn` in goroutine (non-blocking) | VERIFIED | `service_windows.go:29`: `go p.runFn(ctx)` |
| 05-03 | `Stop()` calls `cancel()` to bridge context cancellation | VERIFIED | `service_windows.go:37`: `p.cancel()` |
| 05-03 | `parseCfgPath` called before subcommand dispatch | VERIFIED | `service_windows.go:72`: `cfgPath := parseCfgPath(os.Args[1:])` |
| 05-03 | Administrator privilege error message present | VERIFIED | Lines 88 and 96: privilege reminder printed on install/uninstall error |

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `go.mod` | kardianos/service v1.2.4 direct dependency | VERIFIED | Present in direct `require` block |
| `cmd/cee-exporter/main.go` | `func run(ctx context.Context)` with ctx.Done() select | VERIFIED | Signature at line 119; ctx.Done() at line 235 |
| `cmd/cee-exporter/service_notwindows.go` | Non-Windows shim passing context.Background() | VERIFIED | 12 lines, correct; `//go:build !windows` |
| `cmd/cee-exporter/service_helpers.go` | parseCfgPath pure function, no build tag | VERIFIED | 14 lines; no build constraint; correct implementation |
| `cmd/cee-exporter/service_helpers_test.go` | 7-case table-driven test, package main | VERIFIED | 7 cases; `package main` declared; all 8 sub-tests pass |
| `cmd/cee-exporter/service_windows.go` | Full SCM wrapper, min 80 lines, imports kardianos/service | VERIFIED | 110 lines; `//go:build windows`; imports `github.com/kardianos/service` |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `service_notwindows.go` | `main.go` `run()` | `runFn(context.Background())` | WIRED | Line 11: `runFn(context.Background())` — correct function call |
| `main.go` `run()` | `context.Context` | `case <-ctx.Done()` in select | WIRED | Line 235: shutdown select handles both signals and ctx cancellation |
| `service_windows.go` `Start()` | `run()` | `go p.runFn(ctx)` goroutine | WIRED | Line 29: non-blocking goroutine launch |
| `service_windows.go` `Stop()` | `run()` ctx cancel | `p.cancel()` | WIRED | Line 37: cancel function stored in Start(), called in Stop() |
| `service_windows.go` | `service_helpers.go` | `parseCfgPath(os.Args[1:])` | WIRED | Line 72: called before subcommand dispatch |
| `svcConfig` options | Windows SCM recovery | `OnFailure: "restart"`, `OnFailureDelayDuration: "5s"` | WIRED (code-level) | Lines 59-61: correct option keys/values per kardianos/service v1.2.4 API |
| `main()` | `runWithServiceManager` | `runWithServiceManager(run)` | WIRED | `main.go:116`: direct function reference, no closure wrapper |

### Requirements Coverage

| Requirement | Phase Plan(s) | Description | Status | Evidence |
|-------------|---------------|-------------|--------|----------|
| DEPLOY-03 | 05-01, 05-02, 05-03 | Windows operator runs `cee-exporter.exe install` to register with SCM | CODE-VERIFIED / HUMAN-NEEDED | `s.Install()` dispatched in `runWithServiceManager`; SCM registration needs Windows hardware |
| DEPLOY-04 | 05-01, 05-03 | Windows operator runs `cee-exporter.exe uninstall` to remove from SCM | CODE-VERIFIED / HUMAN-NEEDED | `s.Uninstall()` dispatched in `runWithServiceManager`; registry cleanup needs Windows hardware |
| DEPLOY-05 | 05-01, 05-03 | Windows Service auto-restarts after unexpected crash | CODE-VERIFIED / HUMAN-NEEDED | `OnFailure: "restart"` with 5s delay and 24h reset period configured; actual SCM restart needs Windows hardware |

All three requirement IDs declared across plans are accounted for. No REQUIREMENTS.md phase-5 IDs are orphaned.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `service_windows.go` | 30, 39 | `return nil` | INFO | Both are legitimate: `Start()` returns nil after launching goroutine; `Stop()` returns nil after calling cancel(). Not stubs. |

No blockers or warnings. The `return nil` instances are correct implementation of the `service.Interface` error-returning contract.

### Human Verification Required

#### 1. SCM Registration with Automatic Delayed Start (DEPLOY-03)

**Test:** On a Windows machine with Administrator privileges, run `.\cee-exporter.exe install` and then open `services.msc`
**Expected:** Service named "CEE Exporter" appears in the list; Startup Type column shows "Automatic (Delayed Start)"; Description reads "Dell PowerStore CEPA audit event bridge to GELF / Windows Event Log"
**Why human:** Live Windows SCM and registry interaction; cross-platform compilation from Linux confirms the code is correct but not that SCM accepted and stored the configuration

#### 2. SCM Deregistration with No Leftover Registry (DEPLOY-04)

**Test:** After install, run `.\cee-exporter.exe uninstall`, then execute in PowerShell: `Get-Item "HKLM:\SYSTEM\CurrentControlSet\Services\cee-exporter" -ErrorAction SilentlyContinue`
**Expected:** Command returns nothing (key does not exist); service is absent from `services.msc`
**Why human:** Registry cleanup is performed by SCM via kardianos/service's `Uninstall()` call; requires Windows registry access

#### 3. Service Starts Within SCM 30-Second Window (Success Criterion 3)

**Test:** After install, start the service via `sc start cee-exporter` or `services.msc`; observe the service status and Windows Event Viewer > System log
**Expected:** Service reaches "Running" status without triggering a timeout event (Event ID 7009 "A timeout was reached waiting for the cee-exporter service to connect"); status transitions from StartPending to Running within 30 seconds
**Why human:** Goroutine-based non-blocking Start() is verified in code, but SCM handshake timing depends on Windows runtime initialization

#### 4. Recovery Actions Applied at Install Time (DEPLOY-05)

**Test:** After install, open `services.msc`, right-click "CEE Exporter" > Properties > Recovery tab
**Expected:** First failure: "Restart the Service"; Reset fail count after: 1440 minutes (24h); Restart service after: 0 minutes (which maps to the "5s" delay set programmatically via SetRecoveryActions); "Enable actions for stops with errors" checkbox is ticked
**Why human:** Win32 `SetRecoveryActions` and `SetRecoveryActionsOnNonCrashFailures` are called internally by kardianos/service; the actual UI reflection requires Windows SCM to confirm translation of the KeyValue options

### Gaps Summary

No automated gaps found. All code-verifiable aspects of the phase pass at all three levels (exists, substantive, wired).

The only open items are the four Windows-hardware-dependent success criteria that cannot be verified from a Linux host. The implementation follows all documented kardianos/service v1.2.4 patterns correctly:

- `Start()` is non-blocking (goroutine launch) — correct SCM contract
- `Stop()` calls `cancel()` not `os.Exit()` — correct SCM contract
- `Arguments` stores only `["-config", cfgPath]` — prevents infinite install loop
- `OnFailureDelayDuration` is string `"5s"` — correct KeyValue API type
- `OnFailureResetPeriod` is int `86400` — correct KeyValue API type
- Both install and uninstall print privilege reminders on error

---

_Verified: 2026-03-03T17:00:00Z_
_Verifier: Claude (gsd-verifier)_
