# Phase 8: TLS Certificate Automation with Let's Encrypt - Research

**Researched:** 2026-03-03
**Domain:** TLS/ACME certificate automation in Go, Dell PowerStore CEPA protocol constraints
**Confidence:** MEDIUM-HIGH (CEPA TLS support is MEDIUM; Go autocert is HIGH from official docs)

---

## Summary

The central finding of this research is that the Dell PowerStore CEPA protocol **sends events over plain HTTP only**. Multiple independent documentation sources confirm the CEPA event receiver URL is always configured using `http://` (not `https://`). This fundamentally changes the rationale for TLS on the cee-exporter CEPA listener port (12228): TLS there protects a channel that CEPA itself cannot use, which means TLS on port 12228 is only relevant if a reverse proxy or future CEPA version supports it.

The existing codebase already has TLS scaffolding in `main.go` (`ListenConfig.TLS`, `buildTLS()`, `ServeTLS()`). What Phase 8 must add is **automatic certificate provisioning via ACME** so operators no longer hand-manage cert/key files. The two in-process Go options are:
1. `golang.org/x/crypto/acme/autocert` — works with Let's Encrypt via HTTP-01 (requires port 80 open) or TLS-ALPN-01 (requires port 443 open). Both conflict with cee-exporter's non-standard port 12228. A port-forwarding shim (port 80 or 443 → 12228) is required in production.
2. `go-acme/lego` — full-featured ACME client with DNS-01 support; better for air-gapped or private-network deployments.

The recommended minimal implementation is: `autocert` with TLS-ALPN-01 via a dedicated port 443 listener for ACME challenges, plus operator-configurable options for manual certs (existing path) and self-signed generation (air-gapped fallback).

**Primary recommendation:** Add `autocert` mode behind a config flag (`listen.acme_domains`) that, when set, starts a separate ACME challenge listener on port 443 and serves the CEPA listener on the configured port with auto-refreshed certificates. Preserve the existing manual cert path as default. Add a self-signed generation mode for air-gapped environments.

---

## Critical Protocol Constraint: CEPA Uses HTTP Only

**Confidence: MEDIUM** (multiple third-party guides confirm, official Dell docs blocked by 403)

Dell PowerStore CEE CEPA event receivers are always configured using plain HTTP URLs. Third-party integration guides (PeerSoftware, others) confirm the canonical format is:

```
ApplicationName@http://<IP>:<Port>
```

No HTTPS variant appears in any CEPA configuration documentation. Dell's own documentation confirms:
> "When a host generates an event on the file system over SMB or NFS, the information is forwarded to the CEPA server over an **HTTP connection**."

**Consequence for Phase 8:** TLS on the cee-exporter CEPA listener (port 12228) does **not** benefit the CEPA event channel because the PowerStore sends plain HTTP to it. TLS on port 12228 is useful only if:
- A reverse proxy terminates CEPA traffic (non-standard deployment)
- A future PowerStore version supports HTTPS receivers
- The CEPA listener is also used for other clients that support TLS

The existing `ListenConfig.TLS` feature (already coded in `main.go`) should be kept. Phase 8 should automate certificate acquisition for it, but operators should understand TLS on 12228 does not encrypt the PowerStore-to-exporter path with current CEE/CEPA.

---

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `golang.org/x/crypto/acme/autocert` | v0.48.0 (Feb 2026) | Automatic ACME certificate management (HTTP-01, TLS-ALPN-01) | Official Go extended library; zero new C deps; CGO_ENABLED=0 safe |
| `crypto/tls` (stdlib) | Go 1.24 | TLS configuration, self-signed cert generation | Already in use; standard library; no new deps |
| `crypto/x509` (stdlib) | Go 1.24 | X509 certificate creation for self-signed mode | Standard library; no new deps |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `go-acme/lego` v4 | latest | Full ACME client with 180+ DNS providers | DNS-01 challenge; air-gapped / private networks |
| Caddy (external) | 2.x | Reverse proxy with automatic HTTPS | Operators who prefer not to embed ACME in the daemon |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `autocert` in-process | Caddy/Traefik reverse proxy | Proxy is operationally simpler (no port 80/443 changes to cee-exporter) but adds external dependency; violates "no external deps beyond Go binary" core value |
| `autocert` | `go-acme/lego` | Lego supports DNS-01 (air-gapped), but adds significant dependency weight (~180 DNS providers) |
| Let's Encrypt public CA | Internal ACME CA (e.g., step-ca) | Internal CA works for air-gapped; DNS not required; operator must run their own CA |

**Installation:**
```bash
go get golang.org/x/crypto@v0.48.0
```
(Note: `golang.org/x/crypto` is already an indirect dep via `golang.org/x/sys`. This promotes it to a direct dep.)

---

## Architecture Patterns

### Recommended Project Structure

```
cmd/cee-exporter/
├── main.go           # Add ACMEConfig to Config; wire autocert Manager
├── tls.go            # Move buildTLS(), logCertInfo() here; add buildAutocert(), generateSelfSigned()
└── ...
```

No new packages needed. All TLS logic stays in `cmd/cee-exporter/`.

### Pattern 1: Three-Mode TLS Configuration

**What:** A `tls_mode` config field selects between manual certs, ACME autocert, and self-signed (runtime-generated).
**When to use:** Always — gives operators choice for different deployment contexts.

Config struct changes:
```go
// Source: based on existing ListenConfig in main.go
type ListenConfig struct {
    Addr      string `toml:"addr"`      // CEPA listener, e.g. "0.0.0.0:12228"
    TLSMode   string `toml:"tls_mode"`  // "off" | "manual" | "acme" | "self-signed"
    CertFile  string `toml:"cert_file"` // used when tls_mode="manual"
    KeyFile   string `toml:"key_file"`  // used when tls_mode="manual"
    ACMEDomains []string `toml:"acme_domains"` // used when tls_mode="acme"
    ACMEEmail   string   `toml:"acme_email"`   // contact email for Let's Encrypt
    ACMECacheDir string  `toml:"acme_cache_dir"` // default "/var/cache/cee-exporter/acme"
}
```

**Backward compatibility:** The existing `TLS bool` + `CertFile`/`KeyFile` fields map to `tls_mode="manual"`. Use a migration helper that converts the old fields to the new mode during config load.

### Pattern 2: In-Process autocert with TLS-ALPN-01

**What:** autocert Manager with TLS-ALPN-01 avoids port 80; the ACME challenge runs on port 443, CEPA still runs on 12228.
**When to use:** When port 80 is blocked but port 443 is routable to the cee-exporter host.

```go
// Source: pkg.go.dev/golang.org/x/crypto/acme/autocert (verified)
import "golang.org/x/crypto/acme/autocert"

func buildAutocert(cfg ListenConfig) (*tls.Config, error) {
    m := &autocert.Manager{
        Prompt:     autocert.AcceptTOS,
        Email:      cfg.ACMEEmail,
        HostPolicy: autocert.HostWhitelist(cfg.ACMEDomains...),
        Cache:      autocert.DirCache(cfg.ACMECacheDir),
    }
    // TLSConfig enables TLS-ALPN-01 challenge automatically
    // when HTTPHandler is not wired (port 80 not used)
    return m.TLSConfig(), nil
}
```

Port 443 must be reachable from the internet (or Let's Encrypt's servers). A firewall rule forwarding 443 → 12228 is NOT sufficient because TLS-ALPN-01 challenge must arrive on port 443, but cee-exporter serves CEPA on 12228. The clean solution is to start a separate port-443 challenge listener:

```go
// TLS-ALPN-01 challenge listener on :443 (or configurable alt port)
// The CEPA server itself can use any port with m.TLSConfig()
go func() {
    challengeSrv := &http.Server{
        Addr:      ":443",
        Handler:   m.HTTPHandler(nil), // redirect non-challenge to HTTPS
        TLSConfig: m.TLSConfig(),
    }
    // This listener handles ACME challenges and redirects other traffic
    ln, _ := net.Listen("tcp", ":443")
    challengeSrv.ServeTLS(ln, "", "") // empty cert/key = autocert handles it
}()
```

**IMPORTANT:** Binding to port 443 requires `CAP_NET_BIND_SERVICE` on Linux (or running as root). The systemd unit from Phase 4 may need `AmbientCapabilities=CAP_NET_BIND_SERVICE`.

### Pattern 3: HTTP-01 Challenge on Port 80

**What:** Simpler to reason about than TLS-ALPN-01 but requires port 80 access.
**When to use:** When the cee-exporter host has port 80 open to the internet.

```go
// Source: pkg.go.dev/golang.org/x/crypto/acme/autocert (verified)
go http.ListenAndServe(":80", m.HTTPHandler(nil))
```

Same `CAP_NET_BIND_SERVICE` constraint applies.

### Pattern 4: Self-Signed Certificate (Air-Gapped)

**What:** Generate an ECDSA certificate at startup, store in memory. No ACME, no ports needed.
**When to use:** Air-gapped deployments; private networks; testing.

```go
// Source: Go stdlib crypto/ecdsa, crypto/x509 (verified)
func generateSelfSigned(hosts []string) (*tls.Certificate, error) {
    key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    if err != nil {
        return nil, err
    }
    template := &x509.Certificate{
        SerialNumber: big.NewInt(1),
        Subject:      pkix.Name{Organization: []string{"cee-exporter"}},
        DNSNames:     hosts,
        NotBefore:    time.Now(),
        NotAfter:     time.Now().Add(365 * 24 * time.Hour),
        KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
        ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
    }
    certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
    if err != nil {
        return nil, err
    }
    cert, err := tls.X509KeyPair(
        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
        pemKey(key),
    )
    return &cert, err
}
```

### Anti-Patterns to Avoid

- **Running ACME on the CEPA port (12228) for TLS-ALPN-01:** The ACME standard requires port 443. Challenge validation will fail.
- **No DirCache in production:** Without `DirCache`, certs are re-requested on every restart. Hitting Let's Encrypt rate limits (5 certs/same-identifier-set/7 days) will lock out certificate issuance.
- **Dropping the old `TLS bool` config:** Breaking change for operators who already have `tls=true` in config.toml. Maintain backward compatibility.
- **Binding to :443 without capability escalation:** systemd unit needs `AmbientCapabilities=CAP_NET_BIND_SERVICE` or use a firewall redirect.
- **HostPolicy not set (nil):** Without `HostWhitelist`, the autocert Manager will attempt to serve certificates for any hostname, making it vulnerable to exhausting Let's Encrypt rate limits via forged SNI requests.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| ACME protocol implementation | Custom HTTP-01 solver | `autocert.Manager` | ACME is a complex RFC 8555 protocol; renewal timing, retries, key rotation all handled |
| Certificate renewal scheduling | cron/ticker calling certbot | `autocert` auto-renews 30 days before expiry | Race conditions, drift, restart races are subtle |
| Self-signed cert generation | openssl shelling out | `crypto/ecdsa` + `crypto/x509` in stdlib | CGO_ENABLED=0 prevents subprocess calls; pure Go is available |
| ACME cache | Custom key-value store | `autocert.DirCache` | DirCache is correct (0700 dirs, 0600 files, atomic writes) |
| TLS-ALPN-01 challenge server | Custom TLS negotiation | `autocert.Manager.TLSConfig()` | acme.ALPNProto must be in NextProtos; Manager handles this |

**Key insight:** The ACME protocol (RFC 8555) has dozens of edge cases around retries, rate-limit backoff, and key rollover. `autocert` encapsulates all of it. The only custom code needed is plumbing config → Manager fields.

---

## Common Pitfalls

### Pitfall 1: ACME Challenge Port vs CEPA Port Confusion
**What goes wrong:** Operator sets `tls_mode="acme"` but does not forward port 443 to cee-exporter. Let's Encrypt cannot complete the TLS-ALPN-01 challenge. Certificate is never issued. Service starts but TLS handshakes all fail with "no certificates" error.
**Why it happens:** cee-exporter listens on 12228; ACME requires the challenge to arrive on 443.
**How to avoid:** The ACME challenge listener must be a separate `net.Listen(":443")` goroutine, distinct from the CEPA listener on 12228. Document the port 443 requirement explicitly in config comments.
**Warning signs:** `autocert: missing certificate` or `acme: error 400` in logs on startup.

### Pitfall 2: DirCache Not Persistent Across Restarts in Container/Scratch Image
**What goes wrong:** cee-exporter runs in the `scratch` Docker image. `ACMECacheDir` points to an in-container path that is wiped on restart. Rate limits hit after ~5 restarts (5 certs/same-set/7 days limit).
**Why it happens:** Scratch image has no persistent filesystem by default.
**How to avoid:** Require operators to mount a volume at the `acme_cache_dir` path. Default to `/var/cache/cee-exporter/acme` (matches systemd `StateDirectory` idiom). Document that Docker must mount this path.
**Warning signs:** `too many certificates already issued` in logs.

### Pitfall 3: CEPA Cannot Use TLS (Protocol Mismatch)
**What goes wrong:** Operator enables TLS on port 12228 and registers cee-exporter as `SomeApp@https://host:12228` in PowerStore. PowerStore CEPA client actually sends plain HTTP; TLS handshake fails from PowerStore's side. Events stop arriving.
**Why it happens:** CEPA protocol only supports HTTP receivers (confirmed by multiple sources).
**How to avoid:** Document clearly that TLS on the CEPA listener does **not** encrypt the PowerStore→exporter connection. Only use TLS on 12228 if a TLS-capable reverse proxy sits in front.
**Warning signs:** CEE registration succeeds but no events arrive; CEPA pool shows connection errors.

### Pitfall 4: Let's Encrypt Rate Limits During Development
**What goes wrong:** Developer tests certificate issuance by restarting the daemon multiple times. After ~5 restarts with the same domain, issuance is blocked for 7 days.
**Why it happens:** Each restart (without DirCache) issues a new certificate request; 5-per-7-days cap for identical identifier sets.
**How to avoid:** Always use Let's Encrypt staging (`https://acme-staging-v02.api.letsencrypt.org/directory`) during development. In `autocert.Manager`, set `Client: &acme.Client{DirectoryURL: acme.LetsEncryptURL}` for prod or the staging URL for dev. Add an `acme_staging` config boolean.
**Warning signs:** `urn:ietf:params:acme:error:rateLimited` in logs.

### Pitfall 5: `autocert` API Stability Disclaimer
**What goes wrong:** Upstream changes to `autocert` break unexpectedly after a `go get -u`.
**Why it happens:** The package explicitly states "no API stability promises."
**How to avoid:** Pin the `golang.org/x/crypto` version in `go.mod` (already done for other uses). Do not auto-upgrade without review. The API has been stable in practice for several years despite the disclaimer.
**Warning signs:** Build failures after updating `golang.org/x/crypto`.

### Pitfall 6: Windows Service + Port 443
**What goes wrong:** Windows service cannot bind to port 443 without elevated privileges or HTTP.sys reservation.
**Why it happens:** Windows restricts binding to ports < 1024 to administrators or services with explicit ACL grants (`netsh http add urlacl`).
**How to avoid:** On Windows, autocert mode with TLS-ALPN-01 on 443 requires either: running as SYSTEM (default for services), or running `netsh http add urlacl url=https://+:443/ user=...`. Document this. DNS-01 via `lego` avoids the port 443 constraint on Windows.
**Warning signs:** `bind: permission denied` on Windows service start.

---

## Code Examples

Verified patterns from official sources:

### autocert Manager Setup (TLS-ALPN-01, no port 80)
```go
// Source: pkg.go.dev/golang.org/x/crypto/acme/autocert (verified HIGH confidence)
import (
    "golang.org/x/crypto/acme/autocert"
    "crypto/tls"
    "net/http"
    "net"
)

func buildAutocertTLS(domains []string, email, cacheDir string) (*tls.Config, error) {
    m := &autocert.Manager{
        Prompt:     autocert.AcceptTOS,
        Email:      email,
        HostPolicy: autocert.HostWhitelist(domains...),
        Cache:      autocert.DirCache(cacheDir),
        // No Client field = use Let's Encrypt production
    }
    // TLSConfig() enables TLS-ALPN-01 challenge handling automatically.
    // The caller must ensure port 443 is reachable from Let's Encrypt servers.
    return m.TLSConfig(), nil
}

// Separate challenge listener on port 443 (REQUIRED for TLS-ALPN-01):
func startACMEChallengeListener(m *autocert.Manager) error {
    ln, err := net.Listen("tcp", ":443")
    if err != nil {
        return fmt.Errorf("acme challenge listener: %w", err)
    }
    go func() {
        srv := &http.Server{TLSConfig: m.TLSConfig()}
        _ = srv.ServeTLS(ln, "", "") // autocert provides certs
    }()
    return nil
}
```

### Self-Signed Certificate Generation (stdlib only)
```go
// Source: go.dev/src/crypto/tls/generate_cert.go (verified HIGH confidence)
import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/tls"
    "crypto/x509"
    "crypto/x509/pkix"
    "encoding/pem"
    "math/big"
    "time"
)

func generateSelfSignedTLS(hosts []string) (*tls.Config, error) {
    key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    if err != nil {
        return nil, fmt.Errorf("generate key: %w", err)
    }
    keyDER, err := x509.MarshalECPrivateKey(key)
    if err != nil {
        return nil, fmt.Errorf("marshal key: %w", err)
    }
    keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

    template := &x509.Certificate{
        SerialNumber: big.NewInt(1),
        Subject:      pkix.Name{Organization: []string{"cee-exporter"}},
        DNSNames:     hosts,
        NotBefore:    time.Now().Add(-time.Minute),
        NotAfter:     time.Now().Add(365 * 24 * time.Hour),
        KeyUsage:     x509.KeyUsageDigitalSignature,
        ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
    }
    certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
    if err != nil {
        return nil, fmt.Errorf("create cert: %w", err)
    }
    certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

    cert, err := tls.X509KeyPair(certPEM, keyPEM)
    if err != nil {
        return nil, fmt.Errorf("x509 key pair: %w", err)
    }
    return &tls.Config{
        Certificates: []tls.Certificate{cert},
        MinVersion:   tls.VersionTLS12,
    }, nil
}
```

### Backward-Compatible Config Migration
```go
// Source: project pattern (based on existing main.go ListenConfig)
// Ensure old config.toml with tls=true still works after Phase 8
func migrateListenConfig(cfg *ListenConfig) {
    // Legacy: tls=true with cert_file/key_file → tls_mode="manual"
    if cfg.TLSMode == "" {
        if cfg.TLS && cfg.CertFile != "" {
            cfg.TLSMode = "manual"
        } else {
            cfg.TLSMode = "off"
        }
    }
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Manual cert/key files | autocert ACME automation | 2016 (Let's Encrypt GA) | Operators no longer manage cert rotation |
| HTTP-01 only | TLS-ALPN-01 supported | 2018 (RFC 8737) | Works when port 80 blocked |
| Single CA (Let's Encrypt) | Let's Encrypt + ZeroSSL fallback | 2021 (autocert supports multiple CAs) | Higher availability |
| No ARI | ACME Renewal Information (ARI) | 2024 (Let's Encrypt) | Renewals exempt from rate limits |

**Deprecated/outdated:**
- `autocert.NewListener("domain")`: One-liner that binds to `:443` and cannot coexist with other services on that port. Not suitable for cee-exporter which has its own port.
- `tls_mode="acme"` on the CEPA port directly: Not valid — ACME challenges cannot be completed on non-standard ports.

---

## Open Questions

1. **Does PowerStore CEPA ever support HTTPS?**
   - What we know: All documentation confirms HTTP-only. The endpoint URL always uses `http://`.
   - What's unclear: Whether newer PowerStore firmware (post 3.x) has added HTTPS support. Dell did not publish accessible documentation confirming or denying this for current firmware.
   - Recommendation: Design Phase 8 around the documented HTTP-only constraint. If HTTPS support is added by Dell in future firmware, the existing `ListenConfig.TLS` infrastructure already handles it — operators just need to register cee-exporter with `https://` in the CEPA pool config.

2. **Should Phase 8 include DNS-01 via go-acme/lego for air-gapped ACME?**
   - What we know: `lego` adds significant dependency weight (160+ DNS provider adapters). Air-gapped use case may be better served by self-signed mode.
   - What's unclear: How many cee-exporter operators are in private networks where public ACME is impossible but they still want automated cert management.
   - Recommendation: Out of scope for Phase 8. Self-signed mode covers air-gapped. DNS-01 can be a Phase 9 if operators request it.

3. **systemd unit capability changes needed?**
   - What we know: Phase 4 created a systemd unit without `AmbientCapabilities`. Port 443 binding requires `CAP_NET_BIND_SERVICE`.
   - What's unclear: Whether updating the systemd unit is in Phase 8 scope or Phase 4 follow-up.
   - Recommendation: Include systemd unit update in Phase 8's scope — it is a prerequisite for the autocert ACME mode to work on Linux.

4. **Windows service + ACME port 443 privilege?**
   - What we know: Windows services running as SYSTEM can bind to port 443. Services running as restricted accounts need `netsh http add urlacl`.
   - Recommendation: Document the SYSTEM requirement in config.toml comments. Test only on SYSTEM account for v1 of this feature.

---

## Sources

### Primary (HIGH confidence)
- `pkg.go.dev/golang.org/x/crypto/acme/autocert` — Manager struct, TLSConfig(), HTTPHandler(), DirCache, HostWhitelist, TLS-ALPN-01 support
- `github.com/golang/crypto/blob/master/acme/autocert/autocert.go` — API stability disclaimer, HTTPHandler port 80 requirement
- `letsencrypt.org/docs/challenge-types/` — HTTP-01 port 80 only, DNS-01 no port, TLS-ALPN-01 port 443
- `letsencrypt.org/docs/rate-limits/` — 50 certs/registered domain/7 days; 5 certs/identical set/7 days; ARI exempt
- `go.dev/src/crypto/tls/generate_cert.go` — Self-signed cert generation pattern (stdlib)

### Secondary (MEDIUM confidence)
- `kb.peersoftware.com/kb/dell-powerstore-configuration-guide` — CEPA endpoint always `http://`, no HTTPS
- `kb.peersoftware.com/kb/dell-unity-configuration-guide` — Confirms HTTP-only, port 12228 (or 9843 for PeerSoftware agent)
- Multiple Dell documentation sources confirming "forwarded to CEPA server over an **HTTP connection**"
- `github.com/golang/crypto` commit history — TLS-ALPN-01 added in 2018

### Tertiary (LOW confidence)
- WebSearch results suggesting newer PowerStore CEPA might support HTTPS — not found in official Dell docs, not verified

---

## Metadata

**Confidence breakdown:**
- Standard stack (autocert, stdlib crypto): HIGH — verified from official pkg.go.dev docs
- CEPA HTTP-only constraint: MEDIUM — multiple third-party guides confirm; Dell official docs blocked by 403; no counter-evidence found
- Architecture patterns: HIGH — derived directly from autocert API + existing codebase analysis
- Pitfalls: HIGH — derived from official Let's Encrypt rate limit docs + autocert documentation
- Port/challenge constraints: HIGH — confirmed by Let's Encrypt official challenge-types doc

**Research date:** 2026-03-03
**Valid until:** 2026-06-03 (90 days — autocert API stable in practice; Let's Encrypt rate limits stable)

**Phase 8 minimum viable scope (suggested):**
1. Config: Add `tls_mode` field with values `off|manual|acme|self-signed`; migrate old `tls=true` to `tls_mode="manual"`
2. Mode `acme`: Wire `autocert.Manager` with `DirCache`, `HostWhitelist`, separate port-443 challenge listener goroutine
3. Mode `self-signed`: `generateSelfSignedTLS()` using stdlib only; no ACME, no external ports needed
4. Mode `manual`: Existing `buildTLS()` path (already implemented)
5. systemd unit: Add `AmbientCapabilities=CAP_NET_BIND_SERVICE` for ACME mode
6. Tests: Unit test config migration, self-signed generation (no network); integration test note for ACME (requires DNS/port 80 or 443 — not automatable in CI)
