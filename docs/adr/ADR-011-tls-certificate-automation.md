# ADR-011: Three-mode TLS certificate management (off / manual / acme / self-signed)

**Status:** accepted

## Context

Phase 8 introduces automated TLS certificate management. The research (2026-03-03)
uncovered two findings that shape the design:

### Finding 1 — CEPA uses HTTP only (critical protocol constraint)

The Dell PowerStore CEPA client sends events over **plain HTTP only**. All CEPA
integration documentation specifies `http://` endpoint URLs; no HTTPS variant exists.
Dell's own documentation states: "events are forwarded to the CEPA server over an
HTTP connection."

**Consequence:** TLS on the cee-exporter CEPA listener (port 12228) does **not**
encrypt the PowerStore-to-exporter event channel. TLS on this port is useful only
when a TLS-capable reverse proxy terminates CEPA traffic, or if a future PowerStore
firmware version adds HTTPS receiver support. The existing `ListenConfig.TLS`
infrastructure is kept for these cases, but operators must understand the limitation.

### Finding 2 — Manual certificate management is operationally brittle

The v1 TLS model requires operators to hand-manage certificate files and renewals.
For deployments that _do_ use TLS (e.g., reverse proxy scenarios or future CEPA HTTPS
support), automated renewal via ACME is the industry standard.

### Options evaluated

| Option | Summary | Verdict |
|--------|---------|---------|
| Manual cert files only (v1 approach) | Operator manages rotation | KEPT as `tls_mode="manual"` for backward compat |
| `golang.org/x/crypto/acme/autocert` | In-process ACME, HTTP-01 or TLS-ALPN-01 | **SELECTED** for `tls_mode="acme"` |
| `go-acme/lego` with DNS-01 | Supports air-gapped/private networks | DEFERRED — adds 160+ DNS provider deps |
| External reverse proxy (Caddy/Traefik) | Proxy handles ACME automatically | Valid operational pattern; not built into daemon |
| Runtime self-signed generation | stdlib `crypto/ecdsa` + `crypto/x509` | **SELECTED** for `tls_mode="self-signed"` |

**Rejected: DNS-01 via lego** — the dependency weight (~180 DNS provider adapters) is
disproportionate. Air-gapped deployments are better served by `self-signed` mode or an
operator-managed CA.

## Decision

Extend `ListenConfig` with a `tls_mode` field controlling four modes:

| `tls_mode` | Description |
|------------|-------------|
| `"off"` | No TLS (default). Suitable for HTTP-only CEPA deployments. |
| `"manual"` | Operator supplies `cert_file` + `key_file`. Preserves v1 behaviour. |
| `"acme"` | `autocert.Manager` with `DirCache`, `HostWhitelist`, and a separate port-443 ACME challenge listener. Requires `acme_domains` and internet access to Let's Encrypt. |
| `"self-signed"` | Runtime ECDSA-P256 certificate generated via stdlib. No network needed. Valid for air-gapped or testing. |

Backward compatibility: existing configs with `tls = true` are migrated to
`tls_mode = "manual"` automatically at startup.

**Library:** `golang.org/x/crypto/acme/autocert` — already an indirect dependency via
`x/sys`. Promoting to direct dep. CGO-free. No new C toolchain requirement.

**Self-signed generation:** stdlib only — `crypto/ecdsa`, `crypto/x509`, `crypto/rand`,
`encoding/pem`. No external tools or `openssl` subprocess.

**ACME challenge listener:** TLS-ALPN-01 via a dedicated `net.Listen(":443")` goroutine,
separate from the CEPA listener on 12228. Port 443 binding requires `AmbientCapabilities=
CAP_NET_BIND_SERVICE` in the systemd unit.

**Cache:** `autocert.DirCache("/var/cache/cee-exporter/acme")` — operators using Docker
must mount this path as a persistent volume to avoid hitting Let's Encrypt rate limits
(5 certs / same identifier set / 7 days) on container restarts.

## Consequences

- `golang.org/x/crypto` is promoted from indirect to direct dep in `go.mod`.
- `tls_mode = "off"` is the new default — existing deployments that relied on the
  implicit no-TLS behaviour are unaffected.
- Operators who relied on the v1 `tls = true` flag must migrate to `tls_mode = "manual"`;
  a deprecation warning is logged when the old field is detected.
- ACME mode requires port 443 to be reachable from Let's Encrypt servers. The systemd
  unit must include `AmbientCapabilities=CAP_NET_BIND_SERVICE`.
- The Let's Encrypt staging URL must be used during development (`acme_staging = true`
  config option) to avoid the 5-cert-per-7-days rate limit.
- Self-signed mode produces browser-untrusted certificates (expected) — suitable for
  machine-to-machine scenarios where the CA can be added to the trust store.
- TLS on port 12228 with CEPA source: clearly documented that this does NOT encrypt
  the PowerStore → cee-exporter path with current CEPA protocol versions.
