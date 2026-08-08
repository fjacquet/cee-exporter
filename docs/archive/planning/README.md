# Archived Planning Documents

The phase-driven planning process that produced these documents is retired.
They are kept for provenance, not as current guidance. **Nothing here is
maintained, and some of it has been disproven** — in particular the pitfalls
catalogue predates the 2026-08-08 audit and did not anticipate any of its
findings.

Where the durable content went:

| Topic | Now lives in |
|---|---|
| Requirement IDs and status | [`docs/requirements.md`](../../requirements.md) |
| Architecture, features, pitfalls, stack | [`docs/research/`](../../research/) — expanded versions of the files under `research/` here |
| EVTX binary format, BinXML, chunk layout, rotation design | The [`go-evtx`](https://github.com/fjacquet/go-evtx) repository, which owns the format |
| Design decisions | [`docs/adr/`](../../adr/) |

For the shape of the current process, see `docs/superpowers/specs/` and
`docs/superpowers/plans/`.

## Handover to go-evtx

The five phase `RESEARCH.md` files below covered EVTX binary-format design
(BinXML encoding, chunk layout, CRC ordering, the open-handle/incremental-flush
model, and file-rotation design) rather than CEPA/operational concerns. That
knowledge belongs to the module that owns the format, not to this repo's
archive, so full copies were staged here for the go-evtx maintainer to pull in
as reference documentation before this repository stopped tracking them as
live guidance:

| Staged as | Original path (this repo, pre-archive) |
|---|---|
| [`handover-to-go-evtx/07-binaryevtxwriter-RESEARCH.md`](handover-to-go-evtx/07-binaryevtxwriter-RESEARCH.md) | `.planning/milestones/v3.0-phases/07-binaryevtxwriter/07-RESEARCH.md` |
| [`handover-to-go-evtx/08.5-go-evtx-oss-module-extraction-RESEARCH.md`](handover-to-go-evtx/08.5-go-evtx-oss-module-extraction-RESEARCH.md) | `.planning/milestones/v4.0-phases/08.5-go-evtx-oss-module-extraction/08.5-RESEARCH.md` |
| [`handover-to-go-evtx/09-goroutine-scaffolding-and-fsync-RESEARCH.md`](handover-to-go-evtx/09-goroutine-scaffolding-and-fsync-RESEARCH.md) | `.planning/milestones/v4.0-phases/09-goroutine-scaffolding-and-fsync/09-RESEARCH.md` |
| [`handover-to-go-evtx/10-open-handle-incremental-flush-RESEARCH.md`](handover-to-go-evtx/10-open-handle-incremental-flush-RESEARCH.md) | `.planning/milestones/v4.0-phases/10-open-handle-incremental-flush/10-RESEARCH.md` |
| [`handover-to-go-evtx/11-file-rotation-RESEARCH.md`](handover-to-go-evtx/11-file-rotation-RESEARCH.md) | `.planning/milestones/v4.0-phases/11-file-rotation/11-RESEARCH.md` |

This is tracked from the go-evtx side in that repository's spec, under
"Inherited knowledge from cee-exporter" — this directory only confirms the
material was copied out and is not lost when this archive is excluded from
the published docs site. The originals remain below, unmodified, as part of
this repo's own historical record.

The other eight phase `RESEARCH.md` files (`01-quality`, `02-build`,
`03-documentation`, `04-observability-linux-service`, `05-windows-service`,
`06-siem-writers`, `08-tls-certificate-automation-with-let-s-encrypt`,
`12-config-validation-prometheus-and-docs`) are CEPA-protocol and operational
in nature — TLS, Windows service, deployment, build, docs process — and stay
archived here only; nothing to hand elsewhere.

`.planning/research/*.md` (`ARCHITECTURE.md`, `FEATURES.md`, `PITFALLS.md`,
`STACK.md`, `SUMMARY.md`, now under `research/` in this archive) were already
superseded before this move: `docs/research/` holds larger, expanded versions
of the same material (e.g. `pitfalls.md` is roughly 4x the line count of the
version here).
