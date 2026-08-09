# EVTX read-back verification — design

**Date:** 2026-08-09
**Status:** approved, not yet implemented
**Target release:** v5.1.0

## Problem

`OUT-06` promises that `.evtx` files generated on Linux "open correctly in
Windows Event Viewer and parse with forensics tools". It has been marked
**Unverified** since v2, tracked as go-evtx F6.

It was not merely unverified. It was false.

go-evtx v0.7.0's changelog records that `EventLogRecord.ToXml()` and
`Get-WinEvent` rejected **every file go-evtx had ever produced** —
`"The data is invalid."` — on records that go-evtx's own reader accepted
without complaint. The cause was five bytes per record: an
`OptionalSubstitution`'s token declares the field's own type while its entry
in the substitution array declares `NULL` when the value is absent; go-evtx
wrote `NULL` in both places. That combination occurs zero times in 27 million
structural observations across 320 398 real Windows records.

So every `.evtx` cee-exporter has written on Linux since v2 is unreadable by
the tool operators would use to read it, and nothing in this repository could
have noticed, because nothing here has ever read one back.

## Goal

Bump `github.com/fjacquet/go-evtx` to v0.7.0 and build the verification loop
that has never existed, so `OUT-06` is closed by a job rather than by a claim.

## What the bump costs: nothing structural

Measured, not assumed (2026-08-09, this repository at `838ef02`):

- cee-exporter uses only the writer API: `goevtx.New`, `RotationConfig`,
  `Writer`, `WriteRecord`, `Close`, `Rotate`. v0.7.0's BREAKING change removes
  `Reader.ReadRecord()` and the `Record` struct — reader side only. No
  intersection.
- With the pin at v0.7.0, `go build ./...` succeeds and the full suite passes
  under `-race`.

Three things that could have made that measurement a lie were each checked and
ruled out:

| Risk | Check | Result |
|---|---|---|
| A `replace` pointing at the dirty local go-evtx worktree | `grep replace go.mod`, `ls go.work*` | Neither exists; resolution is via the module proxy |
| Module cache not matching the tag | `go list -m -json` → `Origin.Hash` | `f622ac4…`, equal to `git rev-parse v0.7.0^{}` |
| Output differences caused by writer nondeterminism, not the version | Two emits per version, `cmp` | Each version is byte-identical to itself |

With identical input events and a deterministic writer on both sides, **5310
bytes differ** between v0.6.0 and v0.7.0 output. The bump reaches our write
path and changes our files.

Rendered through python-evtx, one record goes from this:

```xml
<Event><System><Provider Name="PowerStore-CEPA"></Provider>
<EventID>4663</EventID>
```

to a full Windows `System` block — `xmlns`, `Guid`, `Qualifiers`, `Version`,
`Task`, `Opcode`, `Keywords`, `EventRecordID`, `Correlation`, `Execution`,
`Channel`, `Security`. All twelve of cee-exporter's `EventData` fields survive
with correct names and values.

Two new attributes are unexplained and must not be waved through:
`Qualifiers="2727"` (absent in v0.6.0; the value looks uninitialised) and an
empty `Channel`. Event Viewer places events by channel. Both are assigned to
the manual layer below, because instrumenting them would mean changing
go-evtx, which is out of scope for this repository.

## Architecture: three oracles, each with a stated blind spot

The central finding that shapes this design: **python-evtx parses both the
v0.6.0 and the v0.7.0 files** — three records, XML rendered, no error. The
file `Get-WinEvent` rejected outright passes python-evtx without complaint. It
is lenient where Windows is strict.

That does not disqualify it. It ranks it.

| Layer | Where | Catches | Blind spot |
|---|---|---|---|
| python-evtx | `evtx-oracle` job, Linux, every push | Structural regression; the literal "forensics tools" half of OUT-06 | **Proven blind** to the Get-WinEvent defect class |
| `Get-WinEvent -Path` | `evtx-readback` job, windows-latest, every push | Strict Windows reading — the shipped defect | Reads a file; does not open Event Viewer |
| Event Viewer GUI | winvm, Windows Server 2025, manual + dated | Real rendering, channel placement, description text | Point-in-time, not a guard |

`OUT-06`'s proof rests on layer 2. Layers 1 and 3 bracket it without
substituting for it.

## The constraint that decides the job shape

`pkg/evtx/writer_evtx_notwindows.go` carries `//go:build !windows`. **The
Windows runner cannot compile `BinaryEvtxWriter` at all.** It cannot produce
the file it needs to read.

The file must therefore be generated on Linux and transferred as a workflow
artifact. This is not a convenience choice — it is the only available shape,
and it happens to be the correct one, since the promise is about files
generated on Linux.

## No new production code

`emitTestEvents(w evtx.Writer)` already accepts any `Writer`. Run on Linux
with `type = "evtx"`, `-emit-test-events` writes the three mapped event IDs
into a `.evtx`. Verified 2026-08-09: exit 0, and python-evtx reads back
4660, 4663 and 4670, each carrying `ObjectName=C:\test\emit-test-events.txt`.

The machinery built for the v5.0 Windows message-resource check is reused
without a line of new production code. The only additions are CI jobs, one
Python verification script, and documentation.

## Components

### 1. `evtx-oracle` job (ubuntu-24.04, new)

1. Build the binary.
2. Run it with an `evtx`-typed config and `-emit-test-events`.
3. `astral-sh/setup-uv@v6` — pinned to a major, like every other action in
   this repo after the v5.0.1 `actions/checkout@v4` correction — then
   `uv sync --frozen` in `tools/evtx-debug`. The committed `uv.lock` pins
   python-evtx 0.8.1, so the oracle cannot drift under us.
4. Run `verify_evtx.py <path>`; non-zero exit fails the job.
5. `actions/upload-artifact` the `.evtx`.

`verify_evtx.py` asserts:

- exactly 3 records;
- event IDs 4660, 4663, 4670, each exactly once;
- all twelve `EventData` field names present;
- `ObjectName`, `AccessMask`, `SubjectUserName`, `SubjectDomainName` carry
  their expected values — a parse that succeeds while rendering empty fields
  must fail.

### 2. `evtx-readback` job (windows-latest, new, `needs: evtx-oracle`)

Downloads the artifact and asserts, per record:

| Assertion | Catches |
|---|---|
| `Get-WinEvent -Path` does not throw | Wholesale rejection — the shipped defect |
| 3 records returned | Silent loss |
| `.Id` ∈ {4660, 4663, 4670}, each once | Wrong or duplicated ID |
| `.ToXml()` does not throw, **per record** | `"The data is invalid."` — the exact v0.6.0 failure |
| `ObjectName` present in the XML with its expected value | A parse that succeeds but renders nothing |

The fourth is the load-bearing one. It is what v0.7.0 fixes, and without it
the job would go green on a file Event Viewer refuses to display.

**This is a separate job from the existing `windows` job, deliberately.**
Folding the read-back into `windows` would light one runner instead of two,
but `needs:` would then mean a failure in this new oracle prevents the
message-resource and SCM-lifecycle assertions from running at all. Trading
established coverage for a minute of runner time is the wrong direction.

### 3. `tools/evtx-debug` promoted

Its README currently says "Not part of the shipped product." It becomes
CI-load-bearing. `verify_evtx.py` is added with a proper CLI and exit codes;
the existing ad-hoc `parse_evtx.py` and `debug_evtx.py` stay as debugging
aids, and the README states which is which.

### 4. Manual winvm protocol

A dated section in `docs/windows-verification.md`, beside the message-resource
protocol — same machine, same reader. It answers the three questions no
automated layer can:

1. Does the file open via **File → Open Saved Log**?
2. Does the Description pane show our text or the placeholder, given the empty
   `Channel`?
3. Where does `Qualifiers="2727"` come from, and does Windows object?

## Documentation

- `CHANGELOG.md` — a `[5.1.0]` entry with an explicit operator callout (below).
- `docs/PROMISES.md` — `OUT-06` from `**Unverified**` to `**Verified**`, naming
  the GUI portion as manual and dated, as WIN-01/WIN-02 were handled in v5.0.
- `docs/requirements.md`, `docs/PRD.md` — same status change; remove the
  "tracked as go-evtx F6" pointers.
- `docs/operator-guide.md` — the callout below.
- An ADR is **not** written. ADR-014 already records that go-evtx owns the
  binary format and that format bugs are fixed there and consumed by version
  bump. This is that process working as designed, not a new decision.

### The operator callout

Every `.evtx` cee-exporter wrote before v5.1.0 is unreadable by Event Viewer
and `Get-WinEvent`, and will stay that way. The bytes are wrong, the source
events are gone, and no conversion exists. An operator who has been archiving
these files for months has a dead archive and does not know it. This gets its
own callout in both the CHANGELOG and the operator guide, not a line buried in
a list.

## Version

**v5.1.0.** The public Go API does not change — go-evtx's BREAKING change is
on the reader, which this project does not use. But the shape of the files
produced changes substantially, and an operator's tooling can observe it.
Minor, not patch.

## Success criteria

1. `go.mod` pins go-evtx v0.7.0; `make ci` green.
2. `evtx-oracle` and `evtx-readback` both green on a PR, and each has been
   shown to fail: `evtx-oracle` when an `EventData` field is removed from
   `windowsEventToFields`, and `evtx-readback` when the pin is reverted to
   v0.6.0. A read-back job that has never failed has proven nothing.
3. The winvm protocol run and its result recorded with a date, including a
   finding for `Qualifiers` and `Channel` — "did not investigate" is an
   acceptable recorded outcome; silence is not.
4. `OUT-06` reads `**Verified**` in PROMISES.md, requirements.md and PRD.md,
   and the docs-lint citation guard passes against the new test/script names.

## Out of scope

- Any change to `/Users/fjacquet/Projects/go-evtx`. Standing constraint.
- The `Qualifiers`/`Channel` values themselves. Investigate and record; fixing
  belongs upstream.
- go-evtx's own known limitations (8-byte alignment, per-chunk template
  declaration, the 3.2 bucket rule). Windows reads the files regardless.
- `DEPLOY-05` (crash auto-restart), still unverified and untouched here.
