# ADR-017: Register with Dell CEE under an allowlisted partner identity

**Status:** accepted
**Date:** 2026-08-22

## Context

Dell CEE will not forward events to a consumer it has not registered, and it
will not register one whose identity it does not already know.

`CGuidStore`, a table compiled into `libCEPPAPIWrapper.so`, maps
*(friendlyName, facility)* → GUID across 47 entries. `CBaseClient::
ValidateResponse()` looks the pair up with `CGuidStore::FindPairedGuid()` and
refuses anything else:

```text
Partner error, unknown or invalid GUID; Partner Provided FriendlyName: … Guid: …
Partner error. GUID mismatch;          Partner Provided FriendlyName: … Guid: …
```

A refused registration means no partner. CEE then answers **every** array
heartbeat `status="0x16"` (`VC_ERROR_CEPP_NOT_FOUND`), and the array counts its
events missed and transmits none. Nothing logs an error. The service is running,
the port is listening, the array is heartbeating, the consumer is being contacted
every ten seconds — and no event will ever be published.

This cost three bring-ups. It was found by raising CEE's `Debug` to 63, the only
level at which it names the reason, and confirmed end to end on 2026-08-22
against PowerStore `diabps01`: `0x16` with 151 discarded events became `0x0`
with events flowing and a `.evtx` read back by `Get-WinEvent`.

There is no mechanism for a third-party consumer to obtain its own entry. The
table ships inside the CEE binary; a new identity requires Dell to add one to a
future CEE build.

## Decision

**The consumer registers under an identity from CEE's table, chosen by the
operator, with no working default.**

`RegistrationConfig` exposes `friendly_name`, `guid`, `version`, `description`,
`protocols` and `event_filter`, bound to `[cepa]` in the config file. The
built-in defaults are `ceeexporter` and a GUID generated for this project —
values CEE is **guaranteed to refuse**.

That is deliberate. Three consequences follow, and each is why the alternatives
below were rejected:

1. **Adopting another vendor's registered identity is a real decision** — CEE
   and its logs will report this consumer under that vendor's name, and it
   collides with a genuine deployment of that product on the same CEE host. An
   operator should make that choice knowingly, not inherit it from a default.
2. **The correct identity depends on the facility** CEE has enabled. Twenty-eight
   of the 47 entries are valid for Audit; others are CAVA, CQM, Backup, Index or
   VCAPS+ only. There is no single right answer to ship.
3. **Failure is loud at the right moment.** With a refused default, `Debug=63`
   on the CEE side names the problem immediately during bring-up. With a working
   default silently baked in, the product would appear to work while
   impersonating a vendor the operator never chose.

The table, its provenance, and the caveat are documented in cee-worker's
`docs/cee-partner-allowlist.md`; the protocol around it in `docs/cepa-protocol.md`.

## Alternatives considered

**Ship a working default (`PeerSoftwareCollector` + its GUID).** Rejected. It
makes the product work out of the box at the cost of impersonating a named
commercial product by default, in a system where two consumers using one
identity on the same CEE host is a real collision. The convenience is not worth
making that choice on the operator's behalf silently.

**Generate a GUID per install.** Does not work. CEE validates against its
compiled-in table; any generated GUID is refused. This was the original design
and is exactly what produced `0x16`.

**Ask Dell to register `cee-exporter`.** The right long-term answer and worth
pursuing, but it lands in a future CEE release at best and does nothing for
deployments running 9.2/9.3 today.

**Bypass CEE entirely.** Works for PowerScale — OneFS speaks CEPA directly to
this consumer and needs none of this. It does **not** work for PowerStore: the
Events Publisher's transport list requires Microsoft RPC and it cannot be
unticked, so the array disqualifies a non-Windows consumer before opening a TCP
session. CEE is unavoidable in that topology.

## Consequences

- A PowerStore deployment does not work until `[cepa]` is set. This is stated in
  the README Quick Start, `config.toml.example`, the operator guide, and Step 0
  of cee-worker's setup runbook, because a silent non-working default is the
  failure this ADR exists to prevent.
- Registration is not sufficient on its own: CEE then probes with
  `<HeartBeatRequest />` and needs `hbStatus=0`, or the partner stays OFFLINE
  and the array receives `0x12`. Handled in `pkg/server/heartbeat.go`.
- CEE switches the registration exchange to UTF-8 once the identity is
  allowlisted, having used UTF-16LE before that. The handler mirrors the request
  encoding, so this is transparent — but a consumer that hard-codes UTF-16 will
  fail at exactly the moment its identity starts being accepted, which is a
  singularly confusing failure.
- If Dell ever registers a `cee-exporter` identity, only the defaults change.

## Verification

- `TestServeHTTP_RegisterRequest_ReturnsRegisterResponse` — the four attributes
  `CEndPoint::Init()` requires, and a non-zero event filter. Mutation-tested.
- `TestServeHTTP_RegisterResponse_IsUTF16WhenAsked` — encoding mirroring.
- `TestServeHTTP_HeartBeatRequest_ReportsOnline` — `hbStatus=0`.
- End to end, 2026-08-22, PowerStore `diabps01` → CEE 9.3.0.0 → this consumer:
  `0x16` → `0x0`, `postSuccessEventsMissed` 151 → 0, 25 records read back by
  `Get-WinEvent` on Windows Server 2025.
