# ADR-014: Extract BinaryEvtxWriter to the go-evtx library

**Status:** accepted
**Date:** 2026-08-08

## Context

ADR-009 chose to implement `BinaryEvtxWriter`
from scratch inside cee-exporter, on the grounds that no pure-Go EVTX writer
library existed. That implementation grew, across Phases 7 through 9, into a
substantial body of format-specific code: BinXML template encoding, chunk
assembly and CRC32 checksums, file rotation, and round-trip verification
against an external parser. None of that code has any dependency on CEPA,
`WindowsEvent`, or anything else specific to this exporter — its correctness
concerns (does the chunk header CRC match, does python-evtx parse the output,
does a multi-chunk file rotate correctly) are entirely separate from cee-exporter's
own concerns (does a CEPA batch parse, does the queue drop events under
backpressure).

Two later ADRs already assume a separate library exists without either of
them being the decision that created it:

- [ADR-012](ADR-012-flush-ticker-ownership.md) places the periodic flush
  ticker "in the go-evtx library layer, inside the `Writer` struct" and
  reasons explicitly about `github.com/fjacquet/go-evtx` as a standalone,
  independently-versioned dependency.
- [ADR-013](ADR-013-write-on-close-model.md) describes `go-evtx v0.1.0`'s
  write-on-close model and plans its Phase 10 supersession entirely in terms
  of that library's internals.

This ADR records the decision both of them presuppose.

## Decision

Extract the EVTX binary-format encoder out of cee-exporter into a standalone
library, `github.com/fjacquet/go-evtx`, and consume it as a direct
dependency. `pkg/evtx/writer_evtx_notwindows.go` is reduced to an adapter:
it translates a `WindowsEvent` into the `map[string]string` shape go-evtx's
`WriteRecord` expects, and delegates all binary encoding, chunk management,
rotation, and flush-ticker lifecycle to the library.

The extraction landed in commit `29ed067`
("feat(08.5-02): replace BinaryEvtxWriter with go-evtx adapter"), which cut
`writer_evtx_notwindows.go` from 546+ lines of format logic down to a 76-line
adapter and removed `evtx_binformat.go` and its test file entirely — that
code, and its tests, now live in go-evtx. The dependency has since been
bumped through `v0.2.0` (Phase 9's `RotationConfig`/flush-ticker wiring) to
the `v0.5.1` currently in `go.mod`.

## Consequences

**Positive:**

- EVTX format correctness — BinXML templates, chunk CRCs, rotation, external
  parser compatibility — is now testable and versioned independently of
  cee-exporter, and reusable by other consumers (e.g. forensics tooling) that
  have no interest in CEPA at all.
- cee-exporter's own `pkg/evtx` package shrank dramatically: the adapter
  translates field names and enforces size limits, nothing more.
- A format bug can be fixed and released in go-evtx without a cee-exporter
  code change beyond a `go.mod` version bump.

**Negative:**

- The "no external dependencies" framing under which ADR-009 was accepted —
  and which the README and PRD both repeated — no longer holds for the EVTX
  output path. `github.com/fjacquet/go-evtx` is a direct, non-stdlib
  dependency.
- A format defect now requires a coordinated release across two repositories
  instead of one. The 2026-08-08 audit
  (`docs/superpowers/specs/2026-08-08-promise-remediation-design.md`) made
  this concrete: it found that go-evtx `v0.5.1` silently truncates
  oversized records instead of rejecting them, producing a `.evtx` file
  whose chunk CRCs verify but which no parser can read — a defect this repo
  cannot fix directly. The durability and format-correctness fixes are
  scoped to ship as go-evtx `v0.6.0`, which had not landed at the time of
  this writing; cee-exporter cannot benefit from that fix until it bumps its
  dependency. In the meantime, this repo added its own defensive caps in
  `windowsEventToFields` (capping any filesystem-controlled field before it
  reaches go-evtx, and guarding `BinaryEvtxWriter.Close` against go-evtx's
  non-idempotent `Close`) precisely because the upstream defect could not be
  fixed on cee-exporter's own timeline. `writer_evtx_notwindows.go` is
  larger today than the 76 lines the extraction produced, but that growth is
  defensive translation-layer code guarding against a library defect, not a
  reversion to encoding EVTX format internals in this repo.

## Supersedes

ADR-009. Its "No new production dependencies added to `go.mod`" claim
(ADR-009, Consequences) is void: as of this decision,
`github.com/fjacquet/go-evtx` is a direct production dependency.

## References

- Commit `29ed067` — "feat(08.5-02): replace BinaryEvtxWriter with go-evtx adapter"
- `docs/superpowers/specs/2026-08-08-promise-remediation-design.md` — the
  2026-08-08 audit that surfaced the unrecorded reversal and the go-evtx
  `v0.5.1` truncation defect
- Companion spec in the go-evtx repository:
  `go-evtx/docs/superpowers/specs/2026-08-08-durability-and-format-correctness-design.md`
- [ADR-012](ADR-012-flush-ticker-ownership.md), [ADR-013](ADR-013-write-on-close-model.md) —
  decisions made against go-evtx's internals without this ADR existing yet
