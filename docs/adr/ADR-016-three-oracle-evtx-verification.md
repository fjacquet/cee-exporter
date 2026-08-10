# ADR-016: Verify generated `.evtx` with three oracles, not one

**Status:** accepted
**Date:** 2026-08-10

## Context

`OUT-06` promised that `.evtx` files generated on Linux "open correctly in
Windows Event Viewer and parse with forensics tools". It was published as
**Unverified** from v2 to v5.0.1.

It was not merely unverified. It was false. Measured on Windows Server 2025:

```text
Get-WinEvent -Path <pre-v5.1.0 file>
The event log file is corrupted
```

Every `.evtx` this exporter wrote on a non-Windows host, for the product's
entire life, was rejected outright by the tool an operator would use to read
it. Nothing here could have noticed, because **nothing in this repository had
ever read one back**. The writer was tested by asserting on bytes it had just
written — magic numbers, a CRC, a chunk header — which is a check that the
encoder is self-consistent, not that anything else accepts its output.

The obvious fix is "read the file back in CI". The question this ADR settles
is *with what*, and the answer is not the obvious one.

## Decision

**Three readers, ranked, each with its blind spot written down.**

| Layer | Where | Catches | Blind spot |
|---|---|---|---|
| python-evtx | `evtx-oracle` job, ubuntu, every push | Structural regression, field names and values; the literal "parse with forensics tools" half of the claim | **Proven blind** to the defect class Windows rejects |
| `Get-WinEvent -Path` | `evtx-readback` job, windows-latest, every push | Strict Windows reading — the shipped defect | Reads a file; it is not Event Viewer |
| Event Viewer GUI | winvm, Windows Server 2025, manual and dated | Placement, rendering as an operator sees it | Point-in-time, not a guard |

`OUT-06`'s proof rests on layer 2. Layers 1 and 3 bracket it without
substituting for it.

### Why python-evtx stays, given it is blind

python-evtx parsed, without complaint, **both** files that Windows rejected —
the pre-v0.7.0 output (`The event log file is corrupted`) and a later
candidate bump (`NullReferenceException`). Three records, XML rendered, all
twelve fields correct, in both cases. It is lenient exactly where Windows is
strict.

A reader that cannot fail on the defect you shipped is not a proof. It is
still worth having:

- It is an **independent implementation**. Layer 2 is Microsoft's reader
  checking a file against Microsoft's format; python-evtx is a third party
  agreeing that the structure is what we say it is.
- It runs **where the file is produced**, on the fast Linux job, so a broken
  field set fails minutes before a Windows runner is involved.
- "Parse with forensics tools" is half of what `OUT-06` literally promises,
  and python-evtx is a forensics tool.

Keeping it is fine. Letting it be cited as the Windows proof is not, which is
why the blind spot is stated in the script's docstring, in
`tools/evtx-debug/README.md`, and in `docs/PROMISES.md` rather than left for
someone to discover.

### Why the file is generated on Linux and shipped as an artifact

Not a convenience. `pkg/evtx/writer_evtx_notwindows.go` carries
`//go:build !windows`, so **the Windows runner cannot compile
`BinaryEvtxWriter` at all** — it cannot produce the file it needs to read. The
artifact hop is the only available shape, and it happens to be the right one:
the promise is about files generated on Linux.

### Why `evtx-readback` is its own job, not part of `windows`

Folding it into the existing `windows` job would light one runner instead of
two. But `needs: evtx-oracle` would then mean a failure in this new oracle
stops the message-resource and SCM-lifecycle assertions (ADR-015, ADR-010)
from running at all. Trading established coverage for a minute of runner time
is the wrong direction.

## Consequences

**Two defects were caught before release, by layer 2 alone.** Both would have
shipped under any single-oracle design, and neither was caught by layer 1:

1. A candidate go-evtx bump made `Get-WinEvent` throw
   `NullReferenceException`. Isolation showed the cause was **ours**:
   `-emit-test-events` left `ProviderName` empty, which renders as
   `<Provider></Provider>` with no `Name` attribute. Real mapped events always
   set it, so the fixture was broken in a way production is not — it would
   have failed CI for a defect no operator could hit.
2. `WindowsEvent.Channel` was set by `pkg/mapper` on every event since v2 and
   silently dropped by `windowsEventToFields`. Every record rendered as
   `<Channel></Channel>` and Windows resolved `LogName` to the empty string —
   the events belonged to no log. Passing it through moves `LogName` from `[]`
   to `[Security]`.

**Invariants are defaulted at the boundary, not assumed.** `pkg/evtx` owns
`DefaultProviderName` and `DefaultChannel`, and `windowsEventToFields`
substitutes them when a field is empty. `pkg/evtx` is the only package both
the mapper and the Win32 writer can import without a cycle
(`pkg/mapper → pkg/evtx`, never the reverse), so it is the only place a shared
constant can live. Two literals that "should" agree, and nothing forcing them
to, is what let the provider name diverge in the first place.

**Every assertion must be watched failing.** Four checks written for this work
turned out to be incapable of failing — a script that called `record.xml()`
after the backing mmap closed; a field-presence check against an encoder that
always emits every slot; a `perl` mutation using four backslashes against a Go
raw string containing single ones; and the fixture fix itself, initially
ungated. Two of those would have *reported a passing mutation test while
mutating nothing*, which is worse than no test at all. The rule this leaves
behind: a mutation step ends by asserting the mutation actually landed.

**One assertion is knowingly unfalsifiable and labelled as such.**
`verify_evtx.py` checks `System/TimeCreated` has a `SystemTime` attribute; no
code path in this repository can drive it empty, because go-evtx backfills it.
It is kept as defence against a future encoder change and recorded as
defence-in-depth rather than counted as a guard.

**The claim is `Verified (partial)`, not `Verified`.** Layers 1 and 2 are
CI-gated on every push. Description rendering rests on a dated manual
measurement. The Event Viewer GUI question — whether it opens the saved log
and where it places the events — **has not been run**: the test host is
reachable only over SSH with no interactive desktop. `Get-WinEvent -Path` is
not the GUI, and an unrun check is not a verified one.

**CI gains a Python dependency.** `tools/evtx-debug` stops being "not part of
the shipped product". `uv sync --frozen` against a committed `uv.lock` pins
python-evtx 0.8.1 so the oracle cannot drift under a green build.

## Alternatives considered

**Inherit go-evtx's own CI proof.** Rejected. go-evtx proves *its* fixtures
are readable. `OUT-06` is about files carrying cee-exporter's twelve fields,
its provider name and its event IDs — a different byte sequence. Citing
evidence that does not cover the claim is the exact failure this project keeps
finding, and it would have missed both defects above, which live in our
adapter rather than in the library.

**python-evtx alone, on Linux.** Rejected on measurement, not on principle: it
accepted both broken files. It would have made `OUT-06` look closed while the
defect shipped.

**A Windows-only job that both writes and reads.** Impossible. The writer is
`//go:build !windows`.

**Round-trip against `0xrawsec/golang-evtx` as an in-process oracle.**
Rejected earlier and still rejected — its transitive dependencies break
`CGO_ENABLED=0` builds. Recorded in the header comment of
`pkg/evtx/writer_evtx_notwindows_test.go`.

## References

- `.github/workflows/ci.yml` — the `evtx-oracle` and `evtx-readback` jobs
- `tools/evtx-debug/verify_evtx.py` — the Linux oracle and its stated blind spot
- [docs/windows-verification.md](../windows-verification.md) section 5 — the dated manual protocol
- [docs/PROMISES.md](../PROMISES.md) — `OUT-06`'s attribution
- [ADR-014](ADR-014-go-evtx-library-extraction.md) — go-evtx owns the binary format; this is the loop that checks what it produces
- [ADR-015](ADR-015-windows-message-resource.md) — the message resource whose rendering the manual layer also covers
