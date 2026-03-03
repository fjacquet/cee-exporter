# ADR-006: Expose Prometheus /metrics on a dedicated port (9228)

**Status:** accepted

## Context

v2.0 adds a Prometheus `/metrics` endpoint to expose the existing atomic counters
(`events_received_total`, `events_dropped_total`, `queue_depth`, `writer_errors_total`).

Two options were considered for where to mount the endpoint:

1. **Same mux as CEPA** — add `/metrics` as a fourth route on `:12228` alongside the
   CEPA PUT handler and `/health`.
2. **Separate HTTP server** — start a second `net/http` server on a dedicated port
   (default `:9228`).

Arguments against option 1:

- In production, the CEPA listener may be TLS-only (operator configures
  `listen.tls = true`). Mounting `/metrics` on the same mux would force every
  Prometheus scraper to present a valid TLS client configuration — an operational
  burden Prometheus operators do not expect.
- Mixing protocol concerns: `:12228` is a CEPA ingestion endpoint gated for
  PowerStore network access. Prometheus scrapers typically come from a separate
  monitoring VLAN and should not be whitelisted on the CEPA firewall rule.
- Industry standard for Go exporters and daemons is a **dedicated metrics port**
  in the `9000–9999` range (Prometheus default allocations use `:9090`, exporters
  use `:9100`, `:9187`, etc.).

## Decision

Prometheus `/metrics` is served on a **separate HTTP server** bound to a configurable
port (default `0.0.0.0:9228`). The existing CEPA mux on `:12228` is unchanged.

A new config key `[metrics] addr = "0.0.0.0:9228"` controls the bind address.
Setting `addr = ""` disables the metrics server entirely.

## Consequences

- The CEPA listener and metrics endpoint can be firewalled independently.
- Prometheus scrapers require no TLS configuration regardless of CEPA TLS settings.
- Operators must open port `9228/TCP` if scraping from a remote Prometheus instance.
- A second `net/http.Server` goroutine is started at daemon boot; it participates
  in the graceful-shutdown sequence alongside the CEPA server.
- Port `9228` follows the Prometheus convention of allocating ports near the
  application's primary port (`12228` → `9228` by dropping the leading `1`).
