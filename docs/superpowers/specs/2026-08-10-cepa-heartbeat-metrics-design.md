# CEPA registration/heartbeat metrics

Design for [issue #27](https://github.com/fjacquet/cee-exporter/issues/27).
Status: approved 2026-08-10.

## Problem

The exporter performs the CEPA handshake and ACKs heartbeats but exposes
nothing about either. The eight series in `pkg/prometheus/handler.go` are all
label-free and all describe events, so `cee_events_received_total == 0` is
ambiguous between two states that demand opposite responses:

- the NAS is quiet — nothing to report, the pipeline is healthy
- the publisher stopped registering or lost its route, and every event is
  being lost silently

Prometheus's own `up` does not separate them: it reports that this process
answers a scrape, not that anything is still publishing to it. A dashboard
built on today's metrics reports green on a dead path.

`pkg/server/server.go` already has the missing fact — `r.RemoteAddr` is logged
on every branch (lines 54, 61, 75, 87). None of it reaches the registry.

## Metrics

```
cee_cepa_last_request_unix_seconds{remote="10.0.2.250"}   gauge
cee_cepa_registrations_total{remote="10.0.2.250"}         counter
cee_cepa_peers_dropped_total                              counter
```

`cee_cepa_last_request_unix_seconds` is the load-bearing one. It is stamped on
**every PUT from the peer** — handshake, event batch, parse failure, body-read
failure alike — not only on `RegisterRequest`. The question it answers is "is
this publisher still talking to us", so a NAS streaming events while its
handshake path is broken must still read alive. `0` is not emitted: a peer with
no recorded request has no series at all.

`cee_cepa_registrations_total` counts handshakes only.

`cee_cepa_peers_dropped_total` exists so that hitting the peer cap is visible
rather than silent. See Cardinality.

### On the `remote` label

This departs from the deliberately label-free design of the existing metrics.
It is justified because the metric has to be per-publisher to be useful: a
single aggregate "last heartbeat from anyone" stays green while one of three
publishers goes dark, which is precisely the case worth catching. Observed
topology in testing is three CEE hosts publishing to one exporter.

### Cardinality

`r.RemoteAddr` carries an ephemeral port (`10.0.2.250:54321`) that changes per
connection, so it is unbounded if used raw. Two bounds apply:

1. The address is reduced to its host with `net.SplitHostPort`. If the address
   does not parse, the raw string is used — the cap below still bounds it.
2. Distinct peers are capped at `metrics.MaxPeers` (64). Past the cap a new
   peer is not recorded: `cee_cepa_peers_dropped_total` is incremented and a
   WARN is logged. A port scanner or misconfigured client cannot grow the
   registry without limit.

Peers are never expired. An expiry window would delete the series for a peer
that went dark, destroying exactly the signal the alert depends on.

## State — `pkg/metrics`

`pkg/metrics` continues to own all state; `pkg/prometheus` only exposes it.
This keeps `client_golang` out of the event path and lets `Snapshot()` carry
the peer table.

```go
// MaxPeers bounds the remote-label cardinality.
const MaxPeers = 64

type peerStat struct {
    lastRequestUnix atomic.Int64
    registrations   atomic.Int64
}

// added to Store
peersMu      sync.RWMutex
peersDropped atomic.Int64
peers        map[string]*peerStat
```

Methods, following the injectable-time shape already used by `RecordFsyncAt`:

| Method | Behaviour |
| --- | --- |
| `RecordPeerRequestAt(host string, t time.Time)` | Stamps the peer. Creates it if absent and under `MaxPeers`; otherwise increments `peersDropped` and returns without recording. |
| `RecordPeerRegistration(host string)` | Increments the peer's registration counter. No-op for a peer that was dropped at the cap. |
| `PeerSnapshot() map[string]PeerStat` | Immutable copy for the collector. |
| `ResetPeers()` | Test isolation, matching the existing singleton-reset convention. |

The hot path — a peer already present — takes `RLock` only. The write lock is
taken solely on first sight of a peer.

`PeerStat` is the exported value type: `{LastRequestUnix int64; Registrations
int64}`.

## Server integration — `pkg/server/server.go`

```go
host := peerHost(r.RemoteAddr)
metrics.M.RecordPeerRequestAt(host, start)
```

placed immediately after the method check and **before** `readBody`, so a
body-read failure still records that the publisher is alive and something else
is wrong. `metrics.M.RecordPeerRegistration(host)` goes in the
`parser.IsRegisterRequest` branch.

`peerHost` is an unexported helper in `package server`:

```go
func peerHost(addr string) string {
    if h, _, err := net.SplitHostPort(addr); err == nil {
        return h
    }
    return addr
}
```

The 3-second ACK budget is unaffected: the added work on the hot path is one
`RLock` and two atomic stores.

## Exposure — `pkg/prometheus/handler.go`

`CounterFunc` and `GaugeFunc` cannot carry a dynamic label set, so the two
per-peer series come from a small collector implementing
`prometheus.Collector`:

- `Describe` sends the two descriptors.
- `Collect` takes one `PeerSnapshot()` and emits
  `prometheus.MustNewConstMetric` per peer — `GaugeValue` for the timestamp,
  `CounterValue` for the registrations.

A single snapshot per scrape keeps the two series mutually consistent.

`cee_cepa_peers_dropped_total` is a plain label-free `CounterFunc`, registered
alongside the existing eight.

## Alerting

Shipped in the operator guide. The default heartbeat interval is 10s
(`HeartBeatIntervalSecs`), so 60s is six missed beats:

```yaml
- alert: CEEPublisherSilent
  expr: time() - cee_cepa_last_request_unix_seconds > 60
  for: 2m
  annotations:
    summary: "CEE publisher {{ $labels.remote }} has not sent a request in over a minute"
```

## Dashboard — `dashboards/cee-exporter.json`

Panels: per-publisher liveness
(`time() - cee_cepa_last_request_unix_seconds`), registration rate,
received/written/dropped, queue depth, fsync age. This follows the convention
`pstore_exporter` uses for its own dashboards.

**The dashboard is Unverified and must be labelled so.** No CI job renders,
lints, or loads this JSON; the only thing standing behind it is that it was
written carefully. On this project specifically, a dashboard shipped alongside
CI-verified metrics reads as equally verified, and that inference is exactly
the failure this repository's documentation discipline exists to prevent. It
is recorded in `PROMISES.md` as Unverified with that reason stated. Adding a
`dashboard-linter` job is a separate issue, not a rider on this one.

## Testing

`pkg/metrics` (`metrics_test.go`):

- a peer is created on first request and its timestamp updated, not duplicated,
  on the second
- the cap admits exactly `MaxPeers` peers and rejects the 65th
- `cee_cepa_peers_dropped_total` increments once per rejected peer
- `RecordPeerRegistration` on a peer rejected at the cap is a no-op and does
  not create the peer
- concurrent `RecordPeerRequestAt` / `PeerSnapshot` under `-race`

`pkg/server` (`server_test.go`, extending the existing `TestServeHTTP_*` set):

- the peer is stamped on all four paths: register, event batch, parse error,
  oversized body
- the ephemeral port is stripped — `10.0.2.250:54321` records as `10.0.2.250`
- registrations are counted on the handshake path only

`pkg/prometheus` (`handler_test.go`):

- an `httptest` scrape contains both labelled series, asserted as exact text
  lines, for two distinct peers
- the empty case emits neither series (no `{remote=""}` artefact)
- `cee_cepa_peers_dropped_total` is present and label-free

All tests are stdlib-only, table-driven with `t.Run`, white-box (`package
metrics`, `package server`), and reset the `metrics.M` singleton — including
`ResetPeers()` — before asserting.

## Documentation

- `docs/operator-guide.md` — the three new rows in the metrics table, the
  `remote` label rationale, the cap and what exceeding it looks like, and the
  alert rule.
- `docs/PROMISES.md` — the metrics as Verified with the test functions named;
  the dashboard as Unverified with the reason above.
- `docs/requirements.md` — traceability rows.

`docs-lint.yml` enforces that any test cited in `PROMISES.md` or
`requirements.md` is a real function and that Status values come from the
defined vocabulary, so the citations must be written to match the test names
actually committed.

## Out of scope

- Surfacing the peer table in `GET /health`. Small, but it changes the
  response shape and its tests; separate change if wanted.
- CI validation of the dashboard JSON.
- Expiry of stale peers — see Cardinality for why it is the wrong shape here.
