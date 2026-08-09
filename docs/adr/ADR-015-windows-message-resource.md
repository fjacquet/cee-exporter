# ADR-015: Compile a Windows message resource into the binary

**Status:** accepted
**Date:** 2026-08-09

## Context

`Win32EventLogWriter` registered its event source with
`eventlog.InstallAsEventCreate`, which points the registry value
`EventMessageFile` at `EventCreate.exe`. That resource carries message
definitions for event IDs 1–1000. `pkg/mapper` emits 4660, 4663 and 4670.

Windows therefore could not resolve a description for any event this product
writes. Event Viewer, `Get-EventLog`, and every forwarder built on the Event
Log API rendered:

> The description for Event ID 4663 from source PowerStore-CEPA cannot be
> found. … The following information is part of the event: '<payload>'

The payload survived as a raw insertion string, so the data was never lost —
what was lost was the description, and with it any chance of the output
matching what a Windows-native consumer expects.

This shipped in every release for the product's entire life. It was never
caught because **nothing executed the code**. `make ci` runs on Linux, where
`//go:build windows` excludes `writer_windows.go` from the compiler
altogether — not compiled-then-discarded; the compiler never sees it. A
Windows-only defect could be green on every check.

## Decision

**Author a `.mc` message file, compile it to a `.syso`, and commit the
compiled object to the repository.**

`pkg/evtx/messages.mc` defines one message per mapped event ID, each carrying
the output of `formatWin32Message` as its single insertion string `%1`.
`make winres` compiles it in two steps — `windmc` produces `.rc` plus a
binary message table, `windres` produces the linkable COFF object — using a
local mingw-w64 toolchain when present and a container otherwise.

**Register against the exporter's own executable.**
`eventlog.Install(win32SourceName, os.Executable(), …)` replaces
`InstallAsEventCreate`, so `EventMessageFile` names a binary that actually
carries the messages.

**Detect and repair a stale registration.** `eventlog.Install` is a no-op when
the source already exists. Every host that ran a previous version has a
`PowerStore-CEPA` source pointing at `EventCreate.exe` and would have rendered
placeholder text forever. `ensureEventSource` reads the registered
`EventMessageFile`, and when it does not match the current executable, removes
and reinstalls the source.

**Degrade rather than refuse to start.** Registration needs Administrator
rights. Without them the writer logs a warning naming the consequence and
continues. An exporter writing badly-rendered events is more useful than one
that will not run.

## Why the `.syso` is committed

This is the part that looks wrong at first glance — a binary artifact in
version control — so the reasoning is recorded rather than left to be
rediscovered:

- The Go linker picks up `*.syso` files in a package directory automatically,
  by filename convention. `_windows_amd64` in the name keeps it out of every
  other target. No build-tag plumbing, no linker flags.
- It works under `CGO_ENABLED=0`. A C toolchain is needed to *produce* the
  object, never to consume it, so cross-compiling to Windows from Linux or
  macOS stays dependency-free.
- Nothing extra ships. The message resource lives inside `cee-exporter.exe`,
  so an operator has no separate DLL to install, register, or get wrong.
- The alternative — generating it during the build — would put mingw-w64 in
  the dependency path of every build on every platform, including the release
  pipeline, to produce a 468-byte file that changes only when the message text
  changes.

The cost is that a committed binary can rot silently: nothing in Go references
it, so deleting it, truncating it, or building it for the wrong architecture
would break Event Viewer output while every build and test stayed green.
`pkg/evtx/rsrc_test.go` guards against that on **every** platform — it checks
the file exists, is non-empty, and opens with the COFF machine type
`0x8664` — so the Linux CI gate catches a resource problem even though the
resource is only ever used on Windows.

## Consequences

**A Windows CI job became mandatory.** A message resource whose correctness
cannot be observed is no better than the placeholder it replaces. `ci.yml`
gained a `windows-latest` job — the first thing in this repo's history to
*execute* `writer_windows.go` and `service_windows.go` rather than merely
compile them.

Two things about that job are worth recording, because both were found by
mutation and neither is obvious:

1. `Get-WinEvent` returns a **`$null`** `Message` for an unresolvable event.
   The string `The description for Event ID` never appears in its output; it
   only surfaces via `Get-EventLog`. An assertion searching `Get-WinEvent` for
   that text would pass on broken output.
2. Asserting that the message contains the payload is not enough. `Object
   Name:` comes from the `%1` insertion string, not from the resource, so a
   resource with missing or swapped descriptions satisfies it. The job asserts
   **each event ID's own description text**.

**Adding a mapped event ID now requires four coordinated edits** —
`pkg/mapper`, `messages.mc`, `emit_test_events.go` and the assertion table in
`ci.yml`. Nothing enforces that they agree. A fifth ID added to the mapper
alone would render as a placeholder again, and no job would notice.

**`windows/arm64` was dropped from the release matrix** (v5.0.0). Windows
Server does not ship for ARM64 and no CI runner can execute the artifact, so
it would have been the one published target with an unverifiable message
resource. Producing an arm64 object is possible —
`llvm-windres --target=aarch64-w64-mingw32` yields a valid `coff-arm64` — and
was deliberately not done.

**Only English is defined.** `messages.mc` declares
`LanguageNames=(English=0x409:MSG00409)`. On a non-English Windows install the
description falls back to the English text rather than the placeholder, which
is a degradation and not a regression, but it is undefined territory nobody
has tested.

## Alternatives considered

**Ship a separate message DLL.** Rejected: an operator step that can be
skipped or done wrong, and a second artifact to version, sign and distribute.

**Keep `InstallAsEventCreate` and remap to IDs under 1000.** Rejected: 4663,
4660 and 4670 are the Windows Security audit IDs that SIEM content and
operator familiarity are built around. Renumbering to make the tooling happy
would trade a rendering defect for a semantic one.

**Generate the `.syso` in CI and commit it from there.** Rejected: a bot
commit on every build, and the release pipeline would still need the
toolchain.

## References

- `pkg/evtx/messages.mc`, `pkg/evtx/rsrc_windows_amd64.syso`
- `pkg/evtx/writer_windows.go` — `ensureEventSource`
- `pkg/evtx/rsrc_test.go` — the cross-platform guard
- `.github/workflows/ci.yml` — the `windows` job
- [docs/windows-verification.md](../windows-verification.md) — the manual
  Event Viewer protocol, including what CI cannot cover
- [ADR-010](ADR-010-kardianos-service-windows-scm.md) — Windows SCM
  integration, whose `svcProgram.Start`/`Stop` the same job now exercises
