# EVTX Read-Back Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bump `github.com/fjacquet/go-evtx` to v0.7.2 and build the CI loop that reads a Linux-generated `.evtx` back on Windows, so `OUT-06` is closed by a job instead of a claim.

**Version history:** the branch first pinned v0.7.0, briefly tried v0.7.1, and shipped v0.7.2.

**Architecture:** Three oracles with stated blind spots. A new `evtx-oracle` job on Linux emits a `.evtx` via the existing `-emit-test-events` flag, verifies it with python-evtx, and uploads it as an artifact. A new `evtx-readback` job on `windows-latest` downloads that artifact and reads it with `Get-WinEvent -Path`. A dated manual protocol on winvm covers the Event Viewer GUI, which neither job can reach.

**Tech Stack:** Go 1.26.5, GitHub Actions, PowerShell 5.1 (`windows-latest`), Python 3.12 via `uv`, python-evtx 0.8.1.

**Spec:** `docs/superpowers/specs/2026-08-09-evtx-readback-verification-design.md`

## Global Constraints

- **Never modify `/Users/fjacquet/Projects/go-evtx`.** Standing user constraint. Consume it only as a versioned module.
- Target release is **v5.1.0** — minor, not patch. The Go API does not change; the produced file shape does.
- Prefix every shell command with `rtk` (project convention), including inside `&&` chains.
- `make lint` now checks formatting. Run `make format` to fix, never hand-align.
- No `_linux.go` suffix ever. Non-Windows files use `_notwindows.go` with `//go:build !windows`.
- Tests are stdlib-only, white-box (same package), table-driven with `t.Run`.
- `errorlint` runs with `comparison: true` — never `==`/`!=` on errors, including in tests. Use `errors.Is`.
- Action majors, checked 2026-08-09: `actions/checkout@v5`, `actions/setup-go@v6`, `actions/upload-artifact@v7`, `actions/download-artifact@v8`, `astral-sh/setup-uv@v9.0.0`. Upload and download version independently — the mismatch is correct. setup-uv is an exact release, not a major: no moving `v9` tag exists, and `@v9` fails at action resolution before any step runs. Check every ref with `gh api repos/OWNER/REPO/git/ref/tags/TAG`.
- Every claim added to `docs/PROMISES.md` must cite a real job or test. The `docs-lint` workflow fails the build on a `Test[A-Z]…` name that is not a real `func` in a `*_test.go`.
- All work happens on branch `feat/v5.1-evtx-readback`, one PR at the end.

### Measured facts this plan must honour

These were measured on 2026-08-09, not inferred. Contradicting them will produce a broken job.

| Fact | Consequence for the implementation |
|---|---|
| `Get-WinEvent -Path` on v0.6.0 output fails with **`The event log file is corrupted`** | Quote this exact string in the operator callout, not go-evtx's `"The data is invalid."` |
| `Get-WinEvent` enumerates **newest-first** — measured `4670,4660,4663` for events written 4663, 4660, 4670 | Compare the **set** of IDs. An order-sensitive assertion fails for an unrelated reason. |
| `.Message` is null and `.LogName` is empty when the event source is not registered on the host | The CI job must **not** assert on either. The manual protocol must register the source first. |
| `emitTestEvents` writes `ClientAddr` but `windowsEventToFields` does **not** map it | `verify_evtx.py` must not expect a `ClientAddr` field. There are exactly twelve `EventData` fields. |
| Windows echoes `Qualifiers='2727'` back without objecting | Record as observed-and-unexplained. Not blocking, not chased. |

### The twelve EventData fields and their emit-test-events values

`windowsEventToFields` (`pkg/evtx/writer_evtx_notwindows.go`) produces exactly these keys. With `-emit-test-events` the values are:

| Field | Value |
|---|---|
| `SubjectUserSid` | *(empty)* |
| `SubjectUserName` | `test-user` |
| `SubjectDomainName` | `TEST` |
| `SubjectLogonId` | *(empty)* |
| `ObjectServer` | `Security` |
| `ObjectType` | `File` |
| `ObjectName` | `C:\test\emit-test-events.txt` |
| `HandleId` | *(empty)* |
| `AccessList` | `ReadData` |
| `AccessMask` | `0x1` |
| `ProcessId` | `0` |
| `ProcessName` | *(empty)* |

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Modify — pin go-evtx v0.7.2 |
| `tools/evtx-debug/verify_evtx.py` | **Create** — the Linux oracle. One job: assert a `.evtx` matches the emit-test-events contract. Exit 0 or 1. |
| `tools/evtx-debug/README.md` | Modify — the directory stops being "not part of the shipped product"; also fix a phantom config example |
| `.github/workflows/ci.yml` | Modify — two new jobs, `evtx-oracle` and `evtx-readback` |
| `docs/windows-verification.md` | Modify — new dated section 5, the saved-log protocol |
| `CHANGELOG.md` | Modify — `[5.1.0]` entry with the operator callout |
| `docs/PROMISES.md` | Modify — `OUT-06` row re-attributed |
| `docs/requirements.md`, `docs/PRD.md` | Modify — `OUT-06` status, drop the go-evtx F6 pointers |
| `docs/operator-guide.md` | Modify — the operator callout |

`verify_evtx.py` is a new file rather than an edit to `parse_evtx.py` because the two have different jobs: `parse_evtx.py` is a human debugging aid that prints, `verify_evtx.py` is a machine gate that exits non-zero. Merging them would make the CI contract depend on a script whose purpose is to be edited ad hoc.

---

## Task 1: Bump go-evtx to v0.7.0

**Files:**
- Modify: `go.mod:8`
- Modify: `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces: a tree whose `BinaryEvtxWriter` output is readable by `Get-WinEvent`. Every later task depends on this.

- [ ] **Step 1: Create the branch**

```bash
rtk git checkout main && rtk git pull
rtk git checkout -b feat/v5.1-evtx-readback
```

- [ ] **Step 2: Confirm no `replace` or workspace is aliasing the local go-evtx worktree**

The local `/Users/fjacquet/Projects/go-evtx` is on a feature branch with uncommitted changes. If a `replace` or `go.work` pointed at it, everything measured below would be about that dirty tree rather than the published tag.

```bash
rtk grep -n replace go.mod || echo "no replace — good"
ls go.work* 2>/dev/null || echo "no go.work — good"
```

Expected: both report the "good" branch.

- [ ] **Step 3: Bump the pin**

```bash
rtk go get github.com/fjacquet/go-evtx@v0.7.0
rtk grep -n "go-evtx" go.mod
```

Expected: `go: upgraded github.com/fjacquet/go-evtx v0.6.0 => v0.7.0`, and go.mod line 8 reads `v0.7.0`.

- [ ] **Step 4: Verify the module cache matches the tag, not something stale**

```bash
rtk go list -m -json github.com/fjacquet/go-evtx@v0.7.0 | rtk grep -A2 '"Origin"'
```

Expected: `"Hash": "f622ac4165763bd0d4502634fe93c6d374606fa5"`. That is `git rev-parse v0.7.0^{}` in the go-evtx repository. If it differs, stop — the proxy served something other than the tag.

- [ ] **Step 5: Build and run the full suite**

```bash
rtk go build ./... && rtk go test ./... -race
```

Expected: build succeeds, every package `ok`. v0.7.0's BREAKING change removes `Reader.ReadRecord()` and the `Record` struct, which this project does not use — it consumes only `goevtx.New`, `RotationConfig`, `Writer`, `WriteRecord`, `Close`, `Rotate`.

- [ ] **Step 6: Confirm the bump actually reaches the write path**

A green suite does not prove the output changed. Generate a file and compare it against the one v0.6.0 produced for the same input.

```bash
cat > /tmp/evtxcheck.toml <<'EOF'
[listen]
addr = "127.0.0.1:12997"
[output]
type = "evtx"
evtx_path = "/tmp/evtxcheck-v070.evtx"
[metrics]
addr = "127.0.0.1:19997"
EOF
rm -f /tmp/evtxcheck-v070.evtx
rtk go run ./cmd/cee-exporter -config /tmp/evtxcheck.toml -emit-test-events
ls -l /tmp/evtxcheck-v070.evtx
```

Expected: exit 0, a log line `"msg":"test_events_emitted","count":3`, and a 69632-byte file.

- [ ] **Step 7: Commit**

```bash
rtk git add go.mod go.sum
rtk git commit -m "deps: go-evtx v0.7.0 — Windows can read what we write

v0.6.0 wrote NULL in both an OptionalSubstitution's token and its entry in
the substitution array. Windows rejects that combination outright: on Windows
Server 2025, Get-WinEvent -Path on a v0.6.0-produced file returns \"The event
log file is corrupted\". Every .evtx this exporter has written since v2 is
unreadable.

v0.7.0's BREAKING change removes Reader.ReadRecord() and the Record struct.
This project uses only the writer API, so the bump is mechanical: build and
the full -race suite pass unchanged.

Verified the pin resolves to the published tag rather than the local worktree:
no replace directive, no go.work, and go list -m -json reports Origin.Hash
f622ac4, equal to git rev-parse v0.7.0^{}."
```

---

## Task 2: `verify_evtx.py`, the Linux oracle

**Files:**
- Create: `tools/evtx-debug/verify_evtx.py`
- Modify: `tools/evtx-debug/README.md`

**Interfaces:**
- Consumes: a `.evtx` produced by Task 1's tree via `-emit-test-events`.
- Produces: `verify_evtx.py <path>` — exit 0 on success, exit 1 with one `FAIL: <reason>` line per problem on stderr. Task 3's `evtx-oracle` job calls exactly this.

- [ ] **Step 1: Write the script**

Create `tools/evtx-debug/verify_evtx.py`:

```python
#!/usr/bin/env python3
"""Verify a cee-exporter .evtx against the -emit-test-events contract.

This is the Linux-side oracle in CI. python-evtx is an independent
implementation, so it is a genuine second opinion on structure -- and it
covers the literal "parse with forensics tools" half of OUT-06.

Its blind spot is measured, not assumed: python-evtx parses the v0.6.0 files
that Get-WinEvent rejects outright as "The event log file is corrupted". It is
lenient where Windows is strict. This script therefore cannot be the proof for
OUT-06; the evtx-readback job is. Do not promote it.

Usage: verify_evtx.py <path-to-evtx>
Exit:  0 all assertions hold; 1 otherwise, one FAIL line per problem.
"""

import sys
import xml.etree.ElementTree as ET

import Evtx.Evtx as evtx

EXPECTED_IDS = {4660, 4663, 4670}

# Exactly the keys windowsEventToFields emits. ClientAddr is deliberately
# absent: emitTestEvents sets it, but the evtx field map does not carry it --
# only the syslog writer does. Expecting it here would fail on correct output.
EXPECTED_FIELDS = [
    "SubjectUserSid",
    "SubjectUserName",
    "SubjectDomainName",
    "SubjectLogonId",
    "ObjectServer",
    "ObjectType",
    "ObjectName",
    "HandleId",
    "AccessList",
    "AccessMask",
    "ProcessId",
    "ProcessName",
]

# Values -emit-test-events produces. Asserting these is what separates "the
# file parsed" from "the file carries our data": a writer that emitted twelve
# correctly-named empty fields would satisfy a names-only check.
EXPECTED_VALUES = {
    "ObjectName": r"C:\test\emit-test-events.txt",
    "ObjectType": "File",
    "ObjectServer": "Security",
    "SubjectUserName": "test-user",
    "SubjectDomainName": "TEST",
    "AccessList": "ReadData",
    "AccessMask": "0x1",
}


def strip_namespaces(root):
    """Drop XML namespaces so plain tag paths work.

    v0.7.0 output carries xmlns on <Event>; v0.6.0 output does not. Stripping
    means a v0.6.0 file reaches the real assertions and fails on its merits
    rather than on a path that silently matches nothing.
    """
    for el in root.iter():
        if "}" in el.tag:
            el.tag = el.tag.split("}", 1)[1]
    return root


def check(path):
    failures = []

    try:
        with evtx.Evtx(path) as log:
            records = list(log.records())
    except Exception as exc:  # noqa: BLE001 - any parse failure is a failure
        return [f"python-evtx could not open the file: {type(exc).__name__}: {exc}"]

    if len(records) != 3:
        failures.append(f"expected 3 records, got {len(records)}")

    seen_ids = []
    for index, record in enumerate(records):
        try:
            raw = record.xml()
        except Exception as exc:  # noqa: BLE001
            failures.append(f"record {index}: xml() raised {type(exc).__name__}: {exc}")
            continue

        try:
            root = strip_namespaces(ET.fromstring(raw))
        except ET.ParseError as exc:
            failures.append(f"record {index}: rendered XML is not well-formed: {exc}")
            continue

        event_id_el = root.find("./System/EventID")
        if event_id_el is None or not (event_id_el.text or "").strip():
            failures.append(f"record {index}: no EventID")
        else:
            seen_ids.append(int(event_id_el.text.strip()))

        data = {}
        for el in root.findall("./EventData/Data"):
            data[el.get("Name")] = el.text if el.text is not None else ""

        for field in EXPECTED_FIELDS:
            if field not in data:
                failures.append(f"record {index}: EventData is missing {field}")

        extra = sorted(set(data) - set(EXPECTED_FIELDS))
        if extra:
            failures.append(f"record {index}: unexpected EventData fields {extra}")

        for field, want in EXPECTED_VALUES.items():
            got = data.get(field)
            if got != want:
                failures.append(f"record {index}: {field} = {got!r}, want {want!r}")

    # A set, not a list. Get-WinEvent enumerates newest-first on the Windows
    # side and this script must agree with that contract rather than pin an
    # order neither tool guarantees.
    if seen_ids and set(seen_ids) != EXPECTED_IDS:
        failures.append(f"event IDs {sorted(seen_ids)}, want {sorted(EXPECTED_IDS)}")
    if len(set(seen_ids)) != len(seen_ids):
        failures.append(f"duplicate event IDs: {sorted(seen_ids)}")

    return failures


def main():
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <path-to-evtx>", file=sys.stderr)
        return 2

    path = sys.argv[1]
    failures = check(path)

    if failures:
        for failure in failures:
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1

    print(f"OK: {path} -- 3 records, IDs {sorted(EXPECTED_IDS)}, all 12 EventData fields")
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 2: Repair the lockfile, then sync**

The committed `uv.lock` records `name = "cee-exporter"` while `pyproject.toml`
declares `cee-evtx-debug`. `--frozen` therefore fails outright — measured
2026-08-09 during pre-flight:

```text
error: The lockfile at `uv.lock` needs to be updated, but `--frozen` was
provided: Missing workspace member `cee-evtx-debug`.
```

Every use of `--frozen` in this plan and in the `evtx-oracle` job depends on
fixing this first. It is a one-line change to `uv.lock`.

```bash
cd tools/evtx-debug
rtk uv lock
rm -rf .venv && rtk uv sync --frozen
```

Expected: `uv lock` reports `Added cee-evtx-debug v0.1.0 / Removed cee-exporter v0.1.0`, then `uv sync --frozen` installs `python-evtx==0.8.1` without error. Confirm the diff to `uv.lock` is exactly the one `name =` line — anything larger means a dependency moved too, which is a separate decision.

```bash
cd /Users/fjacquet/Projects/cee-exporter && rtk git diff --stat tools/evtx-debug/uv.lock
```

Expected: `1 file changed, 1 insertion(+), 1 deletion(-)`.

- [ ] **Step 3: Generate a file and run the script against it**

Write the config yourself rather than relying on Task 1 having left it in
`/tmp` — a fresh shell or a cleaned `/tmp` would otherwise fail here for a
reason unrelated to the script.

```bash
cd /Users/fjacquet/Projects/cee-exporter
cat > /tmp/evtxcheck.toml <<'TOML'
[listen]
addr = "127.0.0.1:12997"
[output]
type = "evtx"
evtx_path = "/tmp/evtxcheck-v070.evtx"
[metrics]
addr = "127.0.0.1:19997"
TOML
rm -f /tmp/evtxcheck-v070.evtx
rtk go run ./cmd/cee-exporter -config /tmp/evtxcheck.toml -emit-test-events
cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/evtxcheck-v070.evtx
echo "exit=$?"
```

Expected: `OK: … 3 records, IDs [4660, 4663, 4670], all 12 EventData fields` and `exit=0`.

- [ ] **Step 4: Falsify it — a value that should be empty is not**

A verification script that has never failed has verified nothing.

**Corrected during execution 2026-08-09.** This step originally deleted
`HandleId` from `windowsEventToFields` and expected "missing field" failures.
It does not fail: go-evtx v0.7.0 renders a fixed per-EventID template with all
twelve `EventData` slots always emitted, so a missing map key yields an *empty
value*, not a missing element. The field-name check is therefore structurally
incapable of failing today — which is exactly the defect class this release
exists to remove, found inside its own new gate.

Two consequences, both applied: `EXPECTED_VALUES` covers all twelve fields
rather than seven, and the mutation below targets a value instead of a name.

```bash
cd /Users/fjacquet/Projects/cee-exporter
cp pkg/evtx/writer_evtx_notwindows.go /tmp/w.bak
rtk perl -0pi -e 's/"SubjectUserSid":    clip\(e\.SubjectUserSID\),/"SubjectUserSid":    "S-1-0-0",/' pkg/evtx/writer_evtx_notwindows.go
rm -f /tmp/evtxcheck-v070.evtx
rtk go run ./cmd/cee-exporter -config /tmp/evtxcheck.toml -emit-test-events
cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/evtxcheck-v070.evtx; echo "exit=$?"
cp /tmp/w.bak /Users/fjacquet/Projects/cee-exporter/pkg/evtx/writer_evtx_notwindows.go
```

Expected: three `FAIL: record N: SubjectUserSid = 'S-1-0-0', want ''` lines and `exit=1`. The seven-entry version of `EXPECTED_VALUES` would have passed this.

- [ ] **Step 5: Falsify it again — wrong value, right name**

This is the assertion that separates "parsed" from "carries our data".

```bash
cd /Users/fjacquet/Projects/cee-exporter
cp pkg/evtx/writer_evtx_notwindows.go /tmp/w.bak
rtk perl -0pi -e 's/"ObjectServer":      "Security",/"ObjectServer":      "",/' pkg/evtx/writer_evtx_notwindows.go
rm -f /tmp/evtxcheck-v070.evtx
rtk go run ./cmd/cee-exporter -config /tmp/evtxcheck.toml -emit-test-events
cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/evtxcheck-v070.evtx; echo "exit=$?"
cp /tmp/w.bak /Users/fjacquet/Projects/cee-exporter/pkg/evtx/writer_evtx_notwindows.go
```

Expected: three `FAIL: record N: ObjectServer = '', want 'Security'` lines and `exit=1`.

- [ ] **Step 6: Confirm the tree is restored and still passes**

```bash
cd /Users/fjacquet/Projects/cee-exporter
rtk git status --short && rtk go test ./pkg/evtx/ -race
```

Expected: no modification to `pkg/evtx/writer_evtx_notwindows.go`, tests `ok`.

- [ ] **Step 7: Rewrite the README**

Replace the whole of `tools/evtx-debug/README.md` with:

````markdown
# evtx-debug

python-evtx tooling for `BinaryEvtxWriter` output.

`verify_evtx.py` is **load-bearing**: the `evtx-oracle` job in
`.github/workflows/ci.yml` runs it on every push, and a non-zero exit fails
CI. The other two scripts are ad-hoc debugging aids and are not run by
anything.

## Setup

```bash
cd tools/evtx-debug
uv sync --frozen
```

`--frozen` refuses to update `uv.lock`, which pins python-evtx 0.8.1. CI uses
the same flag so the oracle cannot drift under a green build.

## Scripts

- `verify_evtx.py <path>` — **CI gate.** Asserts a file produced by
  `cee-exporter -emit-test-events` has exactly 3 records, event IDs 4660,
  4663 and 4670, all twelve `EventData` fields, and the expected values for
  twelve of them. Exit 0 or 1.
- `debug_evtx.py` — manual chunk/record walk plus python-evtx round-trip.
  Useful when the writer produces a file python-evtx can open but reports
  zero records (chunk header fields valid, record scan failing).
- `parse_evtx.py` — quick "does python-evtx return records?" check; prints
  the first record's XML.

`debug_evtx.py` and `parse_evtx.py` read `/tmp/audit.evtx`.

## What this tooling cannot tell you

python-evtx parses files that Windows rejects. Measured on 2026-08-09: a
`.evtx` produced by go-evtx v0.6.0 renders fine here, while `Get-WinEvent`
on Windows Server 2025 returns `The event log file is corrupted`. python-evtx
is lenient where Windows is strict.

So a green `verify_evtx.py` is evidence about structure and field content, not
about whether Windows will open the file. That claim belongs to the
`evtx-readback` job. See `docs/PROMISES.md` for how OUT-06 is attributed.

## Generating a file to inspect

```bash
cat > /tmp/evtx-debug.toml <<'TOML'
[listen]
addr = "127.0.0.1:12997"
[output]
type = "evtx"
evtx_path = "/tmp/audit.evtx"
[metrics]
addr = "127.0.0.1:19997"
TOML

go run ./cmd/cee-exporter -config /tmp/evtx-debug.toml -emit-test-events
```
````

Note the config block: the previous README documented `[outputs.evtx]` with a
`path` key. Neither exists — the real schema is `[output]` with `type` and
`evtx_path`, as `main.go`'s `OutputConfig` defines. Anyone following the old
example got a startup error.

- [ ] **Step 8: Commit**

```bash
rtk git add tools/evtx-debug/
rtk git commit -m "test(evtx): add verify_evtx.py, the Linux-side oracle

Asserts a -emit-test-events file has 3 records, event IDs 4660/4663/4670 as a
set, all twelve EventData field names, and the expected values for twelve of
them. Names alone would pass on a writer emitting twelve correctly-named
empty fields, so the values are the load-bearing half.

Falsified twice before being trusted: deleting HandleId from
windowsEventToFields produces three 'missing HandleId' failures, and blanking
ObjectServer produces three value mismatches. Both exit 1.

The script's docstring and the README both state its blind spot rather than
leaving it to be discovered: python-evtx parses the v0.6.0 files Get-WinEvent
rejects as corrupted, so a green run here is evidence about structure, not
about whether Windows will open the file.

Also repairs uv.lock, which recorded name = 'cee-exporter' while
pyproject.toml declares cee-evtx-debug. uv sync --frozen failed outright on
that mismatch, so every --frozen in the plan and in the evtx-oracle job
depended on fixing it.

And fixes a phantom config example in the README. It documented
[outputs.evtx] with a path key; the real schema is [output] with type and
evtx_path. Anyone who followed it got a startup error."
```

The commit must include `tools/evtx-debug/uv.lock` alongside the new script and the README.

---

## Task 3: The two CI jobs

**Files:**
- Modify: `.github/workflows/ci.yml` — append two jobs after the existing `windows` job

**Interfaces:**
- Consumes: `verify_evtx.py <path>` from Task 2; the v0.7.0 pin from Task 1.
- Produces: artifact `evtx-sample` containing `audit.evtx`, consumed by `evtx-readback` in the same workflow run.

- [ ] **Step 1: Append the `evtx-oracle` job**

Add to the end of `.github/workflows/ci.yml`:

```yaml
  # Produces the .evtx that evtx-readback reads, and checks it with an
  # independent parser on the way past.
  #
  # This job exists on Linux because it has to. pkg/evtx/writer_evtx_notwindows.go
  # carries //go:build !windows, so the Windows runner cannot compile
  # BinaryEvtxWriter at all — it cannot produce the file it needs to read. The
  # artifact hop is the only available shape, and it is also the correct one:
  # the promise in OUT-06 is about files generated on Linux.
  #
  # python-evtx is a genuine second opinion on structure and covers the
  # "parse with forensics tools" half of OUT-06. It is NOT the proof that
  # Windows can read the file: measured on 2026-08-09, it parses the v0.6.0
  # output that Get-WinEvent rejects as "The event log file is corrupted".
  # That proof is evtx-readback's job.
  evtx-oracle:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v5

      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod

      - uses: astral-sh/setup-uv@v9.0.0

      - name: Emit a .evtx with the three mapped event IDs
        run: |
          set -euo pipefail
          cat > /tmp/evtx-ci.toml <<'TOML'
          [listen]
          addr = "127.0.0.1:12997"
          [output]
          type = "evtx"
          evtx_path = "/tmp/audit.evtx"
          [metrics]
          addr = "127.0.0.1:19997"
          TOML
          go run ./cmd/cee-exporter -config /tmp/evtx-ci.toml -emit-test-events
          test -s /tmp/audit.evtx

      # --frozen refuses to update uv.lock, which pins python-evtx 0.8.1. A
      # silently-upgraded parser would change what this gate means without
      # anyone choosing it.
      - name: Verify with python-evtx
        working-directory: tools/evtx-debug
        run: |
          set -euo pipefail
          uv sync --frozen
          uv run python verify_evtx.py /tmp/audit.evtx

      - uses: actions/upload-artifact@v7
        with:
          name: evtx-sample
          path: /tmp/audit.evtx
          if-no-files-found: error
          retention-days: 7
```

- [ ] **Step 2: Append the `evtx-readback` job**

Add immediately after `evtx-oracle`:

```yaml
  # The proof behind OUT-06. Reads the Linux-generated file with the tool an
  # operator would use.
  #
  # Kept separate from the `windows` job on purpose. Folding it in would light
  # one runner instead of two, but `needs: evtx-oracle` would then mean a
  # failure in this new oracle stops the message-resource and SCM-lifecycle
  # assertions from running at all. Trading established coverage for a minute
  # of runner time is the wrong direction.
  evtx-readback:
    runs-on: windows-latest
    needs: evtx-oracle
    steps:
      - uses: actions/download-artifact@v8
        with:
          name: evtx-sample
          path: .

      - name: Get-WinEvent must read what BinaryEvtxWriter wrote
        shell: powershell
        run: |
          $ErrorActionPreference = 'Stop'
          $path = Join-Path $PWD 'audit.evtx'

          # Assertion 1: the file is readable at all. This is the one that
          # fails on pre-v0.7.0 output, with "The event log file is corrupted"
          # — measured on Windows Server 2025, 2026-08-09. It dies here before
          # reaching any record.
          try {
            $events = @(Get-WinEvent -Path $path -ErrorAction Stop)
          } catch {
            Write-Host "::error::Get-WinEvent could not read the file: $($_.Exception.Message)"
            exit 1
          }

          # Assertion 2: nothing was silently lost. A writer that dropped two
          # of three events would still produce a readable file.
          if ($events.Count -ne 3) {
            Write-Host "::error::expected 3 records, got $($events.Count)"
            exit 1
          }

          # Assertion 3: the IDs, as a SET. Get-WinEvent enumerates
          # newest-first — measured 4670,4660,4663 for events written
          # 4663,4660,4670 — so an ordered comparison fails for a reason that
          # has nothing to do with correctness.
          $got = ($events | ForEach-Object { $_.Id } | Sort-Object) -join ','
          if ($got -ne '4660,4663,4670') {
            Write-Host "::error::event IDs were [$got], expected [4660,4663,4670]"
            exit 1
          }

          # Assertions 4 and 5, per record: ToXml is what v0.7.0 fixes, and
          # the ObjectName check stops a parse that succeeds while rendering
          # nothing from passing.
          #
          # Deliberately NOT asserted: .Message and .LogName. Both are empty
          # when the event source is not registered on the host, which says
          # nothing about whether the file is well-formed. Asserting them
          # would make this job fail on a correct file.
          foreach ($e in $events) {
            try {
              $xml = $e.ToXml()
            } catch {
              Write-Host "::error::record $($e.Id): ToXml failed: $($_.Exception.Message)"
              exit 1
            }
            if ($xml -notmatch [regex]::Escape('C:\test\emit-test-events.txt')) {
              Write-Host "::error::record $($e.Id): ObjectName missing from the rendered XML"
              exit 1
            }
            Write-Host "record $($e.Id): ToXml ok, $($xml.Length) chars, ObjectName present"
          }

          Write-Host "Get-WinEvent read 3 records from a Linux-generated .evtx"
```

- [ ] **Step 3: Validate the workflow parses**

```bash
cd /Users/fjacquet/Projects/cee-exporter
rtk python3 -c "import yaml,sys; d=yaml.safe_load(open('.github/workflows/ci.yml')); print(sorted(d['jobs']))"
```

Expected: `['ci', 'evtx-oracle', 'evtx-readback', 'security', 'windows']`.

- [ ] **Step 4: Prove the PowerShell assertions bite, locally, before pushing**

The winvm measurement already showed assertion 1 fails on v0.6.0 output. Prove assertion 5 bites too, by running the same logic against a file whose `ObjectName` is wrong. Requires `ssh winvm`.

```bash
cd /Users/fjacquet/Projects/cee-exporter
# Good file
rm -f /tmp/audit-good.evtx
rtk sed 's|/tmp/audit.evtx|/tmp/audit-good.evtx|' /tmp/evtxcheck.toml > /tmp/good.toml 2>/dev/null || \
  cat > /tmp/good.toml <<'TOML'
[listen]
addr = "127.0.0.1:12997"
[output]
type = "evtx"
evtx_path = "/tmp/audit-good.evtx"
[metrics]
addr = "127.0.0.1:19997"
TOML
rtk go run ./cmd/cee-exporter -config /tmp/good.toml -emit-test-events

# Mutated file: a different ObjectName
cp cmd/cee-exporter/emit_test_events.go /tmp/emit.bak
# Two backslashes, not four. ObjectName is a Go raw string with SINGLE
# backslashes (`C:\test\emit-test-events.txt`), so \\\\ matches nothing and the
# "mutation" silently produces a byte-identical file — a mutation test that
# cannot fail. Corrected during execution 2026-08-09 after exactly that.
rtk perl -0pi -e 's/C:\\test\\emit-test-events\.txt/C:\\test\\WRONG.txt/' cmd/cee-exporter/emit_test_events.go
# Confirm the file actually changed before trusting the result:
rtk grep -c 'WRONG' cmd/cee-exporter/emit_test_events.go
rm -f /tmp/audit-bad.evtx
rtk sed 's|audit-good|audit-bad|' /tmp/good.toml > /tmp/bad.toml
rtk go run ./cmd/cee-exporter -config /tmp/bad.toml -emit-test-events
cp /tmp/emit.bak cmd/cee-exporter/emit_test_events.go

rtk scp /tmp/audit-good.evtx winvm:C:/rbtest/audit-good.evtx
rtk scp /tmp/audit-bad.evtx winvm:C:/rbtest/audit-bad.evtx
```

Then run the same assertion body on winvm against each file. Expected: `audit-good.evtx` passes all five; `audit-bad.evtx` passes 1–4 and fails 5 with `ObjectName missing from the rendered XML`.

Clean up afterwards:

```bash
rtk ssh winvm 'powershell -NoProfile -Command "Remove-Item -Recurse -Force C:\rbtest -ErrorAction SilentlyContinue"'
rtk git status --short
```

Expected: `emit_test_events.go` unmodified.

- [ ] **Step 5: Commit**

```bash
rtk git add .github/workflows/ci.yml
rtk git commit -m "ci: read a Linux-generated .evtx back on Windows

Two jobs. evtx-oracle emits a .evtx with the three mapped event IDs, checks
it with python-evtx and uploads it; evtx-readback downloads it on
windows-latest and reads it with Get-WinEvent -Path.

The artifact hop is not a convenience. writer_evtx_notwindows.go is
//go:build !windows, so the Windows runner cannot compile BinaryEvtxWriter and
cannot produce the file it needs to read. Generating on Linux is the only
available shape, and it is the one OUT-06 actually promises.

Three details are measured rather than assumed, and the job would be wrong
without them:

- IDs are compared as a set. Get-WinEvent enumerates newest-first — 4670,
  4660, 4663 for events written 4663, 4660, 4670.
- .Message and .LogName are NOT asserted. Both are empty when the event
  source is unregistered on the runner, which says nothing about the file.
- Assertion 1 is where pre-v0.7.0 output dies, with 'The event log file is
  corrupted', before any record is reached.

evtx-readback is a separate job from `windows` deliberately: with needs:, a
failure in this new oracle would otherwise stop the message-resource and
SCM-lifecycle assertions from running at all."
```

---

## Task 4: The winvm manual protocol

**Files:**
- Modify: `docs/windows-verification.md` — add section 5 before `## Cleanup`

**Interfaces:**
- Consumes: a `.evtx` from Task 1's tree; `cee-exporter.exe` built with the message resource.
- Produces: a dated, recorded outcome for the two questions CI cannot answer. Task 5 cites it.

- [ ] **Step 1: Add the protocol section**

Insert before `## Cleanup` in `docs/windows-verification.md`:

````markdown
## 5. Saved-log rendering — the part CI cannot see

`evtx-readback` proves `Get-WinEvent -Path` reads a Linux-generated `.evtx`.
It does not open Event Viewer, and it deliberately does not assert on
`.Message` or `.LogName`. This section covers what is left.

**Prerequisite — register the event source first.** On a host where
`PowerStore-CEPA` is not registered, `Message` is null and `LogName` is empty
for reasons that have nothing to do with the file. Measured 2026-08-09: on a
fresh winvm the registry key was absent and every record read back with a null
message, which would have looked exactly like a rendering defect.

```powershell
# As Administrator. This registers the source against the binary carrying the
# message resource; see ADR-015.
.\cee-exporter.exe -emit-test-events

# Confirm before continuing.
Test-Path 'HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\PowerStore-CEPA'
```

Expected: `True`. If it is `False`, stop — anything observed below is
meaningless.

### Generate the file on Linux and copy it over

```bash
cat > /tmp/evtx-manual.toml <<'TOML'
[listen]
addr = "127.0.0.1:12997"
[output]
type = "evtx"
evtx_path = "/tmp/manual.evtx"
[metrics]
addr = "127.0.0.1:19997"
TOML

go run ./cmd/cee-exporter -config /tmp/evtx-manual.toml -emit-test-events
scp /tmp/manual.evtx winvm:C:/manual.evtx
```

### Question 1 — does it open, and where do the events land?

Open Event Viewer, **Action → Open Saved Log…**, select `C:\manual.evtx`.

Our records carry an empty `Channel`, so `LogName` resolves empty. Record
what Event Viewer does with that: does it open, does it prompt to convert,
under what node do the three events appear, and are all three listed?

### Question 2 — does the Description pane show our text?

Select the 4663 record. The Description pane should read *"An attempt was made
to access an object."* followed by the payload.

If it reads *"The description for Event ID 4663 from source PowerStore-CEPA
cannot be found"*, the source registration did not take — go back to the
prerequisite rather than filing a file-format defect.

### Record the outcome

Add a dated result below. `Qualifiers='2727'` appears in the rendered XML;
Windows echoes it back without objecting, so it is recorded as
observed-and-unexplained. Chasing it would mean changing go-evtx, which is out
of scope for this repository.

"Did not investigate" is an acceptable recorded outcome. Silence is not.

| Date | Host | Q1 — opens / placement | Q2 — description | Notes |
|---|---|---|---|---|
| | | | | |
````

- [ ] **Step 2: Run the protocol on winvm and fill the table**

Execute every step above against winvm (Windows Server 2025 Datacenter) and
write the observed results into the table row. Do not fill it from
expectation — the whole point of this section is that nothing automated can
answer these two questions.

- [ ] **Step 3: Verify the docs build**

```bash
cd /Users/fjacquet/Projects/cee-exporter && rtk make docs
```

Expected: exit 0. `mkdocs build --strict` fails on any broken internal link.

- [ ] **Step 4: Commit**

```bash
rtk git add docs/windows-verification.md
rtk git commit -m "docs: saved-log verification protocol, with its trap stated

evtx-readback proves Get-WinEvent reads the file. It does not open Event
Viewer and does not assert on .Message or .LogName. Section 5 covers the rest,
and records the result of running it.

The prerequisite is the reason this section exists in this shape: on a host
where PowerStore-CEPA is not registered, every record reads back with a null
message and an empty LogName. Measured on a fresh winvm on 2026-08-09, that
looked exactly like a rendering defect and was not one. Registering the source
first turns a false finding into a real test."
```

---

## Task 5: Documentation and the operator callout

**Files:**
- Modify: `CHANGELOG.md` — new `[5.1.0]` section under `## [Unreleased]`
- Modify: `docs/PROMISES.md:42` — the OUT-06 row
- Modify: `docs/requirements.md:148` — the OUT-06 row
- Modify: `docs/PRD.md:114`, `docs/PRD.md:118-125`, `docs/PRD.md:200-201`
- Modify: `docs/operator-guide.md` — callout after the `evtx` output section

**Interfaces:**
- Consumes: the job names `evtx-oracle` and `evtx-readback` from Task 3; the dated table row from Task 4.
- Produces: the release-ready tree.

- [ ] **Step 1: Add the operator callout to the operator guide**

After the `type = "evtx"` configuration block around `docs/operator-guide.md:182`, insert:

```markdown
!!! danger "Every `.evtx` written before v5.1.0 is unreadable"

    On Windows, `Get-WinEvent -Path` on a `.evtx` this exporter produced
    before v5.1.0 returns:

    ```text
    The event log file is corrupted
    ```

    Event Viewer refuses them for the same reason. The cause was in the EVTX
    encoder (go-evtx before v0.7.0), which wrote a byte combination that
    occurs nowhere in real Windows event logs.

    **These files cannot be repaired.** The bytes are wrong, the source
    events are gone, and no conversion exists. If you have been archiving
    them, that archive is not readable and upgrading does not change it —
    only files written by v5.1.0 and later are.

    **What is unaffected:** the `gelf`, `syslog` and `beats` outputs, and the
    Windows-native `evtx` output which uses the Win32 Event Log API rather
    than writing files. Only the non-Windows `.evtx` file output is involved.
```

- [ ] **Step 2: Re-attribute OUT-06 in `docs/PROMISES.md`**

Replace line 42's row with:

```markdown
| Generated `.evtx` files open correctly in Windows Event Viewer and parse with forensics tools | `docs/PRD.md` OUT-06, Success Metrics | The `evtx-readback` job in `.github/workflows/ci.yml` downloads a `.evtx` generated by the Linux `evtx-oracle` job and reads it on `windows-latest` with `Get-WinEvent -Path`, asserting 3 records, the event ID set {4660, 4663, 4670}, `ToXml()` per record, and `ObjectName` in the rendered XML. `evtx-oracle` additionally checks the file with python-evtx (`tools/evtx-debug/verify_evtx.py`) — an independent parser, which is the "forensics tools" half, though it is measurably lenient: it parses the pre-v0.7.0 files `Get-WinEvent` rejects as `The event log file is corrupted`. The Event Viewer GUI itself is manual and dated in [docs/windows-verification.md](windows-verification.md) section 5 | **Verified** — `Get-WinEvent` read-back is CI-gated on every push; the GUI rendering and channel placement are manual, dated 2026-08-09 |
```

- [ ] **Step 3: Update `docs/requirements.md`**

Replace line 148's row with:

```markdown
| OUT-06 | Generated `.evtx` files open correctly in Windows Event Viewer and parse with forensics tools | **Verified** | `evtx-readback` job (`Get-WinEvent -Path` on `windows-latest`, reading the artifact from `evtx-oracle`); `evtx-oracle` job (python-evtx via `tools/evtx-debug/verify_evtx.py`); Event Viewer GUI manual, `docs/windows-verification.md` section 5 |
```

- [ ] **Step 4: Update `docs/PRD.md`**

Line 114 — replace the OUT-06 row's note:

```markdown
| OUT-06 | Generated `.evtx` opens in Windows Event Viewer | 07 | ADR-009 — verified since v5.1.0 by the `evtx-readback` CI job; see [docs/PROMISES.md](PROMISES.md) |
```

Lines 118–125 — replace the whole **Verification status** paragraph. Its last
sentence currently reads "depends on go-evtx v0.7.0, not yet released", which
this release makes false:

```markdown
**Verification status:** see [docs/requirements.md](requirements.md) for the
per-requirement traceability and [docs/PROMISES.md](PROMISES.md) for every
user-facing claim's verifying job. Since v5.0, DEPLOY-03 and DEPLOY-04
(Windows Service install/uninstall via the real SCM start/stop lifecycle) are
verified by the `windows` CI job. Since v5.1.0, OUT-06 is verified by the
`evtx-readback` job, which reads a Linux-generated `.evtx` on a Windows runner
with `Get-WinEvent`. DEPLOY-05 (crash auto-restart) remains unverified —
nothing simulates a crash to exercise the SCM's recovery action.
```

Lines 200–201 — replace the success-metric bullet:

```markdown
- `.evtx` files generated on Linux open correctly in Windows Event Viewer —
  **verified** since v5.1.0 (`evtx-readback` CI job reads them back with
  `Get-WinEvent`; the GUI itself is covered by the dated manual protocol in
  `docs/windows-verification.md` section 5)
```

- [ ] **Step 5: Add the CHANGELOG entry**

Insert under `## [Unreleased]`:

```markdown
## [5.1.0] - 2026-08-09

Windows can read the `.evtx` files this exporter writes. It never could
before.

### Fixed

- **Every `.evtx` written on Linux before this release is unreadable by
  Windows.** `Get-WinEvent -Path` returns `The event log file is corrupted`,
  and Event Viewer refuses them for the same reason. The cause was in the
  EVTX encoder: go-evtx before v0.7.0 wrote `NULL` both in an
  `OptionalSubstitution`'s token and in its substitution-array entry, a
  combination that occurs zero times in 27 million structural observations
  across 320 398 real Windows records.

    These files cannot be repaired — the bytes are wrong and the source events
    are gone. Only files written by v5.1.0 and later are readable. The `gelf`,
    `syslog` and `beats` outputs are unaffected, as is the Windows-native
    `evtx` output, which uses the Win32 API rather than writing files.

    This shipped in every release since v2. Nothing caught it because nothing
    in this repository had ever read one of these files back.

### Added

- `evtx-readback` CI job. Downloads a `.evtx` generated by the new Linux
  `evtx-oracle` job and reads it on `windows-latest` with `Get-WinEvent
  -Path`, asserting 3 records, the event ID set {4660, 4663, 4670}, `ToXml()`
  per record, and `ObjectName` in the rendered XML. This is the proof behind
  OUT-06, which had been marked Unverified since v2.
- `evtx-oracle` CI job and `tools/evtx-debug/verify_evtx.py`. Checks the same
  file with python-evtx, an independent parser, on Linux where it is produced.
  Its blind spot is stated in the script and the README: it parses the
  pre-v0.7.0 files Windows rejects, so it is a structural second opinion, not
  the Windows proof.
- `docs/windows-verification.md` section 5 — the dated manual Event Viewer
  protocol, including the prerequisite that cost a false finding: on a host
  where the event source is unregistered, every record reads back with a null
  message, which looks exactly like a rendering defect and is not one.

### Changed

- `github.com/fjacquet/go-evtx` v0.6.0 → v0.7.0. The library's BREAKING change
  removes `Reader.ReadRecord()` and the `Record` struct; this project uses
  only the writer API, so nothing here changed.
- OUT-06 moves from **Unverified** to **Verified** in `docs/PROMISES.md`,
  `docs/requirements.md` and `docs/PRD.md`.
```

- [ ] **Step 6: Run every gate**

```bash
cd /Users/fjacquet/Projects/cee-exporter
rtk make ci
rtk make docs
```

Expected: `0 issues.`, `No vulnerabilities found.`, docs exit 0.

- [ ] **Step 7: Run the docs-lint guards locally**

The CI guard rejects a PROMISES citation naming a `Test[A-Z]…` that is not a real func, and a Status cell outside its vocabulary. Run both before pushing; note the allowlist, or `TestBinaryEvtxWriter_Rotate` produces a false alarm.

```bash
cd /Users/fjacquet/Projects/cee-exporter
fail=0; allowlist=" TestBinaryEvtxWriter_Rotate "
for f in docs/PROMISES.md docs/requirements.md; do
  for name in $(rtk grep -oE '\bTest[A-Z][A-Za-z0-9_]*' "$f" | sort -u); do
    case "$allowlist" in *" $name "*) continue ;; esac
    rtk grep -rq "func ${name}(" --include='*_test.go' . || { echo "MISSING $name"; fail=1; }
  done
done
echo "citation guard exit=$fail"
```

Expected: `citation guard exit=0`.

- [ ] **Step 8: Commit and open the PR**

```bash
rtk git add CHANGELOG.md docs/
rtk git commit -m "docs: OUT-06 verified, and tell operators their old files are dead

OUT-06 has read Unverified since v2. It was not merely unverified — it was
false, and now that there is a job proving the current output readable, the
honest release note is not 'now verified' but 'it never worked and your
archive is unreadable'.

Both the CHANGELOG and the operator guide carry that as its own callout,
quoting the exact string an operator will have searched for — 'The event log
file is corrupted' — rather than go-evtx's changelog wording. Both also state
what is unaffected (gelf, syslog, beats, and the Windows-native evtx writer),
because a callout that overstates the damage gets discounted.

PROMISES, requirements and the PRD move OUT-06 to Verified, naming the
evtx-readback job for the machine-checkable half and the dated manual
protocol for the GUI."

rtk git push -u origin feat/v5.1-evtx-readback
rtk gh pr create --base main --title "feat: verify Windows can read our .evtx (v5.1.0)" --body "..."
```

The PR body should lead with the measured before/after, not the version bump:

```text
v060.evtx : Get-WinEvent FAILED -> The event log file is corrupted
v070.evtx : 3 records, Ids 4670,4660,4663 — ToXml OK on all three
```

- [ ] **Step 9: Confirm the new jobs are green on the PR, and that they ran**

```bash
rtk gh pr checks <N>
```

Expected: `evtx-oracle` and `evtx-readback` both present and passing. A job that is configured but never triggered is not evidence — confirm both appear in the check list, not just that the run is green.

---

## Self-Review

**Spec coverage.** Every spec section maps to a task: the bump and its three
anti-aliasing checks → Task 1; `verify_evtx.py` and the `tools/evtx-debug`
promotion → Task 2; both CI jobs, the artifact hop, the separate-job rationale
→ Task 3; the manual protocol and its registration prerequisite → Task 4; all
five documentation targets and the operator callout → Task 5. Success criteria
1–4 are covered by Task 1 step 5, Task 2 steps 4–5 and Task 3 step 4, Task 4
step 2, and Task 5 step 7 respectively.

**Deliberately not done.** No ADR — ADR-014 already records that go-evtx owns
the binary format and that format bugs are fixed there and consumed by version
bump. This is that process working, not a new decision.

**Known soft spot.** Task 3 step 4 and Task 4 step 2 both need `ssh winvm`. If
that host is unavailable, Task 3's assertion-5 mutation can be skipped — the
winvm measurement of 2026-08-09 already covers assertion 1, the load-bearing
one — but Task 4 cannot, and its table row must then read "not run" with a
date rather than being quietly left empty.
