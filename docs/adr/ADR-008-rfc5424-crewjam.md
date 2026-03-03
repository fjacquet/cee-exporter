# ADR-008: Use crewjam/rfc5424 for the SyslogWriter (not stdlib log/syslog)

**Status:** accepted

## Context

v2.0 adds a SyslogWriter that emits RFC 5424 structured syslog messages over UDP or TCP.
RFC 5424 structured data (SD-ELEMENTs) is required to carry all audit metadata fields
(EventID, ObjectName, SubjectUsername, AccessMask, etc.) without polluting the MSG field.

Four options were evaluated:

1. **`github.com/crewjam/rfc5424`** — a minimal, pure-Go RFC 5424 read/write library.
   Writes to any `io.Writer`; transport provided by stdlib `net.Dial`.
2. **stdlib `log/syslog`** — the Go standard library syslog package.
3. **`github.com/nathanaelle/syslog5424/v2`** — an RFC 5424 library that explicitly
   refuses UDP transport (by library design).
4. **Custom formatter** — implement the RFC 5424 PRI/SD-ELEMENT format in ~80-100 LOC
   using stdlib `fmt` and `strings`.

Arguments against option 2 (stdlib `log/syslog`):

- The standard library `log/syslog` package carries the build constraint
  `//go:build !windows` — it **does not compile on Windows**. Since cee-exporter
  cross-compiles to `GOOS=windows`, importing `log/syslog` breaks the Windows build.
- `log/syslog` produces RFC 3164-style messages (BSD syslog). RFC 3164 has no
  concept of structured data; audit metadata would need to be encoded in the
  unstructured MSG field, making machine parsing unreliable.

Arguments against option 3 (`nathanaelle/syslog5424`):

- The library explicitly refuses to support UDP by design (author opposes UDP for
  audit log transport). cee-exporter must support UDP for compatibility with legacy
  syslog daemons (rsyslog, syslog-ng over UDP port 514).

Arguments against option 4 (custom formatter):

- A custom RFC 5424 formatter is feasible (~100 LOC) but introduces the risk of
  subtle protocol violations (incorrect PRI calculation, missing SD-PARAM escaping
  for `]`, `\`, `"`). Using a tested library eliminates this risk.

## Decision

Use `github.com/crewjam/rfc5424` (v0.1.0) for message construction and serialisation.
Transport is handled by stdlib `net.Conn` from `net.Dial("udp", addr)` or
`net.Dial("tcp", addr)` — the same pattern used by `GELFWriter`.

`crewjam/rfc5424` is pure Go, stdlib-only, and compiles on all platforms including
Windows (`CGO_ENABLED=0` compatible).

## Consequences

- A new dependency `github.com/crewjam/rfc5424 v0.1.0` is added to `go.mod`.
- The library is minimal (~500 LOC); the last release is v0.1.0 (2020). RFC 5424 is
  a stable standard, so library staleness does not represent a protocol risk.
- SD-PARAM value escaping (`"` → `\"`, `\` → `\\`, `]` → `\]`) is handled by the
  library; no custom escaping logic needed in cee-exporter.
- `SyslogWriter` needs no build tags — `crewjam/rfc5424` and `net.Dial` compile
  on Linux and Windows alike.
- Operators must configure `[output] syslog_addr`, `syslog_network`, and optionally
  `syslog_facility` in `config.toml`.
