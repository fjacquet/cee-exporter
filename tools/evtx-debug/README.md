# evtx-debug

Ad-hoc Python scripts used to validate `BinaryEvtxWriter` output against
the reference [python-evtx](https://github.com/williballenthin/python-evtx)
parser during Phase 7 development. Not part of the shipped product.

## Setup

```bash
cd tools/evtx-debug
uv sync
```

## Scripts

- `debug_evtx.py` — manual chunk/record walk plus python-evtx round-trip.
  Useful when the writer produces a file python-evtx can open but reports
  zero records (means chunk header fields are valid but record scan fails).
- `parse_evtx.py` — quick "does python-evtx return records?" sanity check;
  prints the first record's XML.

Both scripts read `/tmp/audit.evtx`. Generate one by pointing a non-Windows
cee-exporter build at a local file sink:

```toml
[outputs.evtx]
type = "evtx"
path = "/tmp/audit.evtx"
```

then send a few events via `curl -X PUT`.
