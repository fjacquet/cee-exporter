---
phase: 08-tls-certificate-automation-with-let-s-encrypt
plan: "03"
subsystem: cmd/cee-exporter
tags: [tls, acme, lets-encrypt, systemd, integration]
dependency_graph:
  requires:
    - 08-01  # TLS builder functions (buildManualTLS, buildSelfSignedTLS, buildAutocertTLS, startACMEChallengeListener)
    - 08-02  # ListenConfig TLS fields (TLSMode, ACMEDomains, ACMEEmail, ACMECacheDir, ACMEChallengeAddr)
  provides:
    - TLS-01  # Four-mode TLS switch wired into run()
    - TLS-02  # ACME challenge listener goroutine started for acme mode
    - TLS-03  # systemd unit with AmbientCapabilities for port 443 binding
  affects:
    - cmd/cee-exporter/main.go
    - deploy/systemd/cee-exporter.service
tech_stack:
  added:
    - crypto/tls (stdlib — TLS config type in main.go)
    - golang.org/x/crypto/acme/autocert (imported in main.go for *autocert.Manager type)
  patterns:
    - Switch dispatch on string enum (tls_mode)
    - Nil *tls.Config signals plain HTTP; non-nil signals TLS mode
    - ServeTLS with empty cert/key strings for in-memory certs (self-signed/acme)
    - ServeTLS with file paths for manual mode
key_files:
  modified:
    - cmd/cee-exporter/main.go   # four-mode TLS switch + updated httpServer + serve goroutine
    - deploy/systemd/cee-exporter.service  # AmbientCapabilities + StateDirectory
decisions:
  - "nil *tls.Config used as sentinel: plain httpServer.Serve() for off, ServeTLS() for all TLS modes"
  - "Manual mode passes cert/key file paths to ServeTLS(); self-signed/acme pass empty strings (certs already in TLSConfig)"
  - "err variable reused from buildWriter declaration — no extra var err error needed"
  - "StateDirectory=cee-exporter lets systemd create /var/lib/cee-exporter owned by cee-exporter user automatically"
metrics:
  duration: "5 min"
  completed: "2026-03-03"
  tasks_completed: 2
  files_modified: 2
---

# Phase 8 Plan 03: TLS Integration into run() Summary

Four-mode TLS switch wired into main.go run() using builder functions from Plan 01, TLSMode config from Plan 02; systemd unit updated with port 443 capabilities and state directory for ACME cert caching.

## Objective

Wire the tls_mode switch into run() in main.go, replacing the TODO placeholder from Plan 02. Add the ACME challenge listener goroutine startup. Update the systemd unit for port 443 binding capability.

## Tasks Completed

| Task | Name | Commit | Files |
|------|------|--------|-------|
| 1 | Wire tls_mode switch into run() | 3f847b2 | cmd/cee-exporter/main.go |
| 2 | Add AmbientCapabilities to systemd unit | e9ee125 | deploy/systemd/cee-exporter.service |

## What Was Built

### Task 1: TLS Mode Switch in main.go

Replaced the `// TODO(phase08-plan03)` placeholder with a four-arm switch block:

- **`"off"` / `""`**: no-op; `tlsCfg` stays nil; `httpServer.Serve(ln)` used
- **`"manual"`**: calls `buildManualTLS(certFile, keyFile)` from tls.go; logs cert file via `logCertInfo()`; `httpServer.ServeTLS(ln, certFile, keyFile)` used
- **`"self-signed"`**: calls `buildSelfSignedTLS(acmeDomains)` from tls.go; in-memory cert; `httpServer.ServeTLS(ln, "", "")` used
- **`"acme"`**: calls `buildAutocertTLS()` → gets `*autocert.Manager` + `*tls.Config`; starts ACME challenge listener goroutine via `startACMEChallengeListener()`; `httpServer.ServeTLS(ln, "", "")` used

The `httpServer` struct now carries `TLSConfig: tlsCfg` (nil = no TLS). The serve goroutine branches on `tlsCfg != nil` and additionally on `cfg.Listen.TLSMode == "manual"` to decide whether to pass file paths or empty strings to `ServeTLS`.

Imports added: `"crypto/tls"` and `"golang.org/x/crypto/acme/autocert"`.

### Task 2: systemd Unit Hardening for ACME

Added to `[Service]` section after `NoNewPrivileges=true`:

```ini
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
StateDirectory=cee-exporter
StateDirectoryMode=0750
```

Updated `ReadWritePaths` to include `/var/lib/cee-exporter` alongside `/var/log/cee-exporter`. The `StateDirectory` directive makes systemd create and chown `/var/lib/cee-exporter/` to the `cee-exporter` user automatically on service start — the ACME cache directory can be set to `/var/lib/cee-exporter/acme` in config.

## Verification Results

All six success criteria satisfied:

1. `make build` — succeeded (Linux/amd64)
2. `make build-windows` — succeeded (Windows/amd64, CGO_ENABLED=0)
3. `make test` — all packages pass
4. Four switch cases present in main.go — confirmed
5. `grep "AmbientCapabilities" deploy/systemd/cee-exporter.service` — present
6. `make lint` (`go vet ./...`) — no errors

## Deviations from Plan

None — plan executed exactly as written.

## Self-Check: PASSED

- [x] cmd/cee-exporter/main.go exists and contains tls_mode switch
- [x] deploy/systemd/cee-exporter.service contains AmbientCapabilities=CAP_NET_BIND_SERVICE
- [x] Commit 3f847b2 exists (Task 1)
- [x] Commit e9ee125 exists (Task 2)
- [x] Both Linux and Windows binaries build cleanly
- [x] All tests pass
