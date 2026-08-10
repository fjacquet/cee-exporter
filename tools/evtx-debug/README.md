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
  all twelve of them. Exit 0 or 1.
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
