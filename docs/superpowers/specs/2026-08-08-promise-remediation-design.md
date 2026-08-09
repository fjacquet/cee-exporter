# cee-exporter: Promise Remediation

**Date:** 2026-08-08
**Status:** Release 1 (v4.1.3, truth) implemented on `feat/v4.1.3-truth`; Release 2 (v5.0, verified — sections V1-V5 below) not yet started
**Repo:** `github.com/fjacquet/cee-exporter`
**Releases:** v4.1.3 (truth), v5.0 (verified)
**Companion spec:** `go-evtx/docs/superpowers/specs/2026-08-08-durability-and-format-correctness-design.md`

## Problem

An audit of the shipped documentation against the shipped code found that
several of the project's published promises are not delivered, and one of them
is a legal exposure. The project has external OSS consumers.

| Promise | Reality |
|---|---|
| MIT badge linking to `LICENSE` in README | No LICENSE file exists. Commit `c792faa` removed it from the release archive because the file was missing, and never created it. Default copyright is all-rights-reserved: nobody may legally use this. |
| Success metric: `go test ./...` passes on Linux **and Windows** | `ci.yml` has no OS matrix. `writer_windows.go` and `service_windows.go` have never been executed by any test. |
| OUT-06: generated `.evtx` opens in Windows Event Viewer | Never verified. No Windows runner, no Event Viewer check. |
| README "Docker (**recommended**)" → `ghcr.io/fjacquet/cee-exporter:latest` | Not published by CI. `.goreleaser.yaml` has no `dockers:` block; `make docker-push` is local-only. |
| WIN-01/02, and `writer_windows.go:55` "SIEM content packs for 4663 work" | False. `InstallAsEventCreate` points `EventMessageFile` at `EventCreate.exe`, which only carries messages for IDs 1–1000. Every 4663/4660/4670 event renders as "The description for Event ID … cannot be found". |
| DEPLOY-01 systemd unit | Ships, but references a `cee-exporter` user and `/var/log/cee-exporter` that nothing in the repo creates. `systemctl start` fails on a clean host. |
| `docs/PRD.md:84` links `.planning/REQUIREMENTS.md` | File does not exist. |

Alongside these, the documentation set is frozen at 2026-03-03 while the code
shipped through v4.1.2, producing config keys that are documented but rejected
at startup (`type = "binary-evtx"`) or silently ignored (`acme_staging`).

The underlying defect is not the documentation. It is that **no promise in this
project has a verification loop attached.** Docs drifted because nothing failed
when they did.

## Goals

- Every statement published to users is true, or deleted.
- Every remaining promise is backed by a CI job that fails when it regresses.
- The Windows path stops being the least-tested code in a product whose
  primary output format is a Windows format.

## Non-goals

- Rewriting the Linux GELF/syslog/beats path. It is tested and works.
- New output backends.
- Changing the CEPA protocol handling.
- Modifying the shared `fjacquet/ci` reusable workflows. cee-exporter is the
  only fleet repo with Windows-specific code, so its Windows job stays local.

## Release 1 — v4.1.3, truth

Nothing here depends on go-evtx. It starts immediately and runs in parallel
with go-evtx v0.6.0.

### T1. LICENSE (blocking, legal)

Add an MIT `LICENSE` at the repo root matching the README badge, and restore it
to the goreleaser archive `files:` list. This is the highest-priority item in
either spec: without it the project is legally unusable by the external
consumers it is published for.

### T2. Delete the config keys that do not exist

- `type = "binary-evtx"` — `docs/operator-guide.md:124`, `:160`, `:195`, `:199`
  and `docs/index.md:85`, including the full worked example at
  `operator-guide.md:191-199`. `buildWriter` (`main.go:382-440`) accepts only
  `gelf|evtx|multi|syslog|beats`; anything else returns `unknown output type`.
  The correct value is `evtx`, which dispatches by platform through
  `NewNativeEvtxWriter`.
- `acme_staging` — `operator-guide.md:119` and `:362-365`. No such field exists
  in `ListenConfig` (`main.go:57-70`). It silently no-ops, so a developer
  believing they are on Let's Encrypt staging hammers production rate limits.
  Either delete the documentation or implement the field; deleting is correct
  unless staging is actually wanted.

### T3. Real version string

`main.go:203` hardcodes `"version", "1.0.0"`. Every v4.1.2 deployment reports
1.0.0 in its structured logs, which destroys version correlation in the SIEM
that consumes those logs.

Introduce `var version = "dev"` and set it via
`-ldflags "-X main.version=..."` from the goreleaser build. Emit it in
`cee_exporter_starting` and add a `cee_build_info` gauge labelled with version,
commit and Go version, so the running version is visible in Prometheus too.

### T4. Publish the Docker image from CI

`.goreleaser.yaml` has no `dockers:` section. `release.yml` authenticates to
GHCR and then never pushes an image. `CLAUDE.md` claims the release workflow
pushes to GHCR — false.

Add a `dockers:`/`docker_manifests:` block so `:latest` and `:vX.Y.Z` are
published by the tag pipeline. Bump the builder from `golang:1.24-alpine`
(`Dockerfile:5`) to `golang:1.26-alpine`: the current builder is two minor
versions below the `go 1.26.5` directive and only succeeds when the toolchain
auto-downloads at build time, failing outright with `GOTOOLCHAIN=local`.

### T5. Make the systemd unit self-sufficient

`deploy/systemd/cee-exporter.service` references a `cee-exporter` user and
`/var/log/cee-exporter`, neither of which anything creates. `Makefile:104-109`
only echoes a `useradd` reminder, and that reminder appears nowhere in the
operator guide.

Prefer `DynamicUser=yes` with `LogsDirectory=cee-exporter` and
`StateDirectory=cee-exporter` so systemd provisions the identity and
directories itself and the unit works on a clean host with no manual steps. If
a stable UID is required, have `make install-systemd` create the user and
directories rather than printing advice.

Also fix `docs/operator-guide.md:253`, which instructs
`install -m 644 systemd/cee-exporter.service` — the real path is
`deploy/systemd/cee-exporter.service` (`Makefile:15`), so the documented
command fails from the repo root.

### T6. ADR-014 — record the go-evtx adoption

The project's largest design reversal is unrecorded. `ADR-009` is still
`Status: accepted` and states at lines 58-59 that `BinaryEvtxWriter` is
implemented from scratch with "No new production dependencies added to
go.mod". `go.mod` now carries `github.com/fjacquet/go-evtx v0.5.1` as a direct
dependency and `writer_evtx_notwindows.go` is a 76-line adapter. ADR-012 and
ADR-013 assume the library exists without ever deciding on it.

Write ADR-014 covering the extraction into a reusable library, mark ADR-009
superseded, and remove the matching false claim at `docs/PRD.md:166`.

### T7. Documentation completeness

- **README** (`:13`, `:15-23`) describes only GELF and Win32. Add syslog,
  beats, Prometheus metrics, ACME and self-signed TLS modes, and Windows
  Service install. Correct "Requires Go 1.21+" (`:64`).
- **Go version** is stated as 1.21+ (`README.md:64`, `operator-guide.md:37`)
  and 1.24+ (`index.md:75`); `go.mod` says 1.26.5. Make all three agree.
- **Platform table** (`index.md:74`) lists only Linux/amd64 and Windows/amd64.
  goreleaser (`.goreleaser.yaml:15-21`) ships linux, darwin and windows across
  amd64 and arm64. Document what is actually released.
- **`config.toml.example`** (`:53-59`, `:60-98`) documents only gelf, evtx and
  multi. `config.toml` documents syslog and beats. Reconcile the two templates.
- **Operator guide** has two contradicting full-config references:
  `:73-102` documents the deprecated `tls`/`cert_file`/`key_file` fields and
  lists three output types; `:104-161` documents `tls_mode` and five. Collapse
  into one, keeping the `tls_mode` form (`main.go:59-60` marks `TLS bool`
  deprecated).
- **SIGHUP rotation** (`sighup_notwindows.go`) is implemented, Linux-only, and
  documented nowhere.
- **`cee_last_fsync_unix_seconds`** (`handler.go:59-61`) is missing from the
  metrics table at `operator-guide.md:281-287`.
- **`docs/PRD.md:158-164`** dependency versions are stale for
  `kardianos/service`, `elastic/go-lumber` and `golang.org/x/crypto`.
- **`docs/PRD.md:84`** links a non-existent `.planning/REQUIREMENTS.md`.
  Resolved by T8, which synthesises `docs/requirements.md` from the
  per-milestone requirement files; repoint the link there.
- **`CHANGELOG.md`** stops at `[1.0.0] - 2026-03-03`. Reconstruct entries for
  v3.0, v4.0, v4.1, v4.1.1 and v4.1.2 from the tags.
- **`mkdocs.yml`** nav (`:10-24`) omits ADR-010 through ADR-013 and all eleven
  `docs/research/*.md` files, which `index.md:93` links to as the "Research
  Archive". `extra.version` (`:57`) says v4.1.1; latest tag is v4.1.2. Stop
  hand-maintaining it: have `docs.yml` substitute `github.ref_name` into
  `extra.version` at build time so it cannot drift again.

### T8. Consolidate `.planning/`

The phase-driven planning process is retired, but `.planning/` holds 117
tracked files (1.5 MB) containing real knowledge alongside dead process
scaffolding.

Composition:

| Group | Volume | Disposition |
|---|---|---|
| `*-PLAN.md`, `*-SUMMARY.md`, `*-VERIFICATION.md` | 77 files, ~14,700 lines | Process residue. Archive. |
| `*-RESEARCH.md` | 13 files, ~7,200 lines | The durable knowledge. Distil before archiving. |
| `.planning/research/*.md` | 5 files | Already superseded — `docs/research/` versions are larger and expanded (pitfalls: 970 lines vs 365). Archive as-is. |
| Per-milestone `REQUIREMENTS.md` | 3 files | Source material for the missing aggregate. |

Three actions:

1. **Distil the RESEARCH files, splitting by ownership.** A large share of the
   value is EVTX binary-format knowledge — BinXML encoding, chunk layout,
   rotation design, the pitfalls catalogue — which now belongs to **go-evtx**,
   not to its consumer. Move that material into go-evtx's docs. CEPA protocol
   behaviour, VCAPS batching, deployment and operational findings stay here.
   Cross-link rather than duplicate.
2. **Synthesise `docs/requirements.md`** from the three per-milestone
   `REQUIREMENTS.md` files plus the requirement IDs already scattered through
   the PRD. This resolves the broken `docs/PRD.md:84` link recorded in T7 —
   the content always existed, the aggregate never did. Point the PRD at the
   new file.
3. **Archive the remainder in place.** Move what is left to
   `docs/archive/planning/` with an index README stating the process is retired
   and pointing to where each distilled topic now lives. Exclude the archive
   from `mkdocs.yml` nav so it does not appear in the published site, and
   confirm `docs.yml` does not fail on unreferenced files.

Nothing is deleted from the working tree. The cost is 1.5 MB of retired process
documents remaining visible in the repository; the benefit is that no knowledge
depends on someone thinking to read git history.

### T9. Honesty pass

Downgrade every compatibility and status claim that no CI job proves. In
particular the Windows Event Viewer and Velociraptor claims become "not yet
verified" until the go-evtx F6 jobs are green, and the
`writer_windows.go:10-15` comment stops asserting that message DLL
pre-registration happens — it does not.

### T10. Defensive hardening against go-evtx, independent of its release

Both of these land in v4.1.3 regardless of go-evtx's schedule, because they
protect the consumer against the current v0.5.1:

- Guard `BinaryEvtxWriter.Close` (`writer_evtx_notwindows.go:45`) so a double
  close cannot propagate go-evtx's `close of closed channel` panic into the
  daemon.
- Cap field length in `windowsEventToFields` before handing off, so a
  filesystem-controlled `ObjectName` cannot reach the oversized-record
  corruption path.

  The budget must be measured in **encoded** bytes. go-evtx writes UTF-16LE
  with a 2-byte length prefix and terminator, so an ASCII value of *n* bytes
  costs 2*n*+4 on the wire and a non-BMP rune costs four. Budgeting against Go
  string length under-counts by more than half and would pass records that
  go-evtx then rejects.

  Two passes are needed, because a per-field cap does not bound a set: twelve
  fields at 8 KiB each encode to roughly 196 KB against a ~61 KB budget.
  First cap any single value at 8,192 bytes — far above `PATH_MAX` — then, if
  the encoded total still exceeds `64,996 − 4,096` (record capacity less a
  reserve for BinXML template and framing overhead), repeatedly halve the
  longest remaining value until it fits. Halving the longest preserves the
  short fields that carry the event's identity — SID, logon ID, access mask —
  over the one that is merely long.

  Append an explicit `…[truncated]` marker without splitting a multi-byte
  rune, and increment a new `cee_events_truncated_total` counter.

Deferred to the v0.6.0 bump, which is its own small change and does not gate
v4.1.3: surface `ErrRecordTooLarge` as a counted drop via `metrics.M` rather
than letting it pass as a write success.

### v4.1.3 exit criteria

- LICENSE present and in the release archive.
- `grep -r "binary-evtx\|acme_staging" docs/ README.md config.toml*` returns
  nothing.
- A fresh `docker run ghcr.io/fjacquet/cee-exporter:v4.1.3` starts from the
  README quickstart.
- `systemctl start` succeeds on a clean host with only the documented steps.
- Startup log and `/metrics` both report the real version.
- Every remaining claim in README and PRD is either true today or explicitly
  marked unverified.
- `docs/PRD.md` links a `docs/requirements.md` that exists, and `.planning/` no
  longer sits at the repo root.

## Release 2 — v5.0, verified

### V1. Windows message resource

`writer_windows.go:38` registers the event source with
`eventlog.InstallAsEventCreate`, which points `EventMessageFile` at
`EventCreate.exe`. That resource covers event IDs 1–1000; the exporter writes
4663, 4660 and 4670. Windows cannot resolve a description, so Event Viewer and
every forwarder built on the Event Log API render the placeholder text with the
real payload appended as an insertion string.

Author a `.mc` message file defining messages for 4660, 4663 and 4670 whose
single insertion string `%1` carries the formatted body already produced by
`formatWin32Message`.

Compiling it takes **two** steps, not one — `windres` does not read `.mc`:

```sh
# 1. Message compiler: .mc -> .rc + .bin message tables
windmc -h . -r . messages.mc        # GNU binutils; mc.exe on MSVC

# 2. Resource compiler: .rc -> linkable object
windres -i messages.rc -O coff -o rsrc_windows_amd64.syso
```

The `.syso` is committed to the package directory. The Go linker picks up
`.syso` files automatically, so this works under `CGO_ENABLED=0`, adds no
artifact to distribute, and gives an operator no path to get wrong — the
message resource lives inside `cee-exporter.exe`. The filename must carry the
`_windows_amd64` suffix so it is not linked into non-Windows builds.

> **Update (2026-08-09).** An earlier draft of this section required a second
> `_windows_arm64` copy. That target no longer exists: windows/arm64 was
> dropped from the release matrix, because Windows Server does not ship for
> ARM64 and no CI runner exists to execute the artifact. Only the amd64
> `.syso` is needed. Producing an arm64 one is possible — `llvm-windres
> --target=aarch64-w64-mingw32` yields a valid `coff-arm64` object — but it
> would arm a target nothing tests and nobody runs.

Note also that the two-command recipe above is incomplete as written:
`windres` shells out to a preprocessor, so `gcc-mingw-w64-x86-64` must be
installed alongside `binutils-mingw-w64-x86-64` or the step fails with
`x86_64-w64-mingw32-gcc: not found`.

Then replace `InstallAsEventCreate` with:

```go
exe, err := os.Executable()
eventlog.Install(win32SourceName, exe, true, eventlog.Info|eventlog.Warning|eventlog.Error)
```

**Upgrade path.** `eventlog.Install` does not repoint a source that already
exists. Every host that has ever run a previous version has a
`PowerStore-CEPA` source whose `EventMessageFile` points at `EventCreate.exe`,
and it would keep rendering the placeholder text forever. On startup, read the
registered `EventMessageFile` value and, when it does not match the current
executable, remove and reinstall the source. Log the re-registration. This
needs administrator rights, exactly as first-time registration does; when the
rights are absent, log a warning naming the consequence rather than failing to
start.

Update the `writer_windows.go:10-15` comment to describe what the code now
actually does.

### V2. Repo-local Windows CI job

Add a `windows-latest` job to `ci.yml`, alongside the existing
`fjacquet/ci@v1` calls. The shared fleet workflow is not modified.

The job does three things:

1. `go test ./...` on Windows — the first time `writer_windows.go` and
   `service_windows.go` are ever executed by a test. Expect this to surface
   pre-existing breakage.
2. Install the event source, write one event of each mapped ID, and assert that
   `Get-WinEvent` output contains the real field values and **does not**
   contain the string `The description for Event ID`. That assertion is the
   whole point of V1.
3. `cee-exporter.exe install` / `uninstall` round-trip, covering DEPLOY-03 and
   DEPLOY-04, which are also currently unverified.

### V3. Consume go-evtx v0.7.0

Bump the dependency and restore the Event Viewer claim to the README and PRD
**only if** the go-evtx F6 `Get-WinEvent -Path` job is green. If it is not,
v5.0 ships with OUT-06 marked unsupported and the claim deleted. Deleting a
promise is an acceptable outcome; keeping an unproven one is what caused this
work.

Note the division of labour: go-evtx owns the proof that a generated `.evtx`
file parses, because go-evtx is the artifact that must parse. cee-exporter's
Windows job proves only its own message-resource rendering. No duplicated
Windows harness.

### V4. `docs/PROMISES.md`

A table mapping every user-facing claim to the CI job that verifies it. A claim
with no job is either deleted or explicitly labelled unverified. New claims are
expected to arrive with their job.

This is the mechanism that prevents the next five-month drift, and it is the
only durable deliverable in either spec.

### Decided against: `/health` HTTP 503 on degraded

Raised during the v4.1.3 documentation review (2026-08-08) as a candidate V5,
then rejected on the merits by the project owner, not deferred. Record why,
so it isn't re-proposed without the reasoning behind it: `docs/operator-guide.md`
had documented "HTTP 200 = healthy; HTTP 503 = degraded" for years, but
`pkg/server/health.go:48` calls `w.WriteHeader(http.StatusOK)`
unconditionally — degradation is signalled only by the JSON `"status"` field.
That was a documentation defect, fixed on this branch. Actually returning 503
on the degraded path would be a *worse* defect: `"degraded"` here means the
async queue is overflowing, and a Kubernetes readiness probe returning 503
pulls the pod out of the Service entirely — CEPA can no longer reach it at
all, losing *every* event instead of the fraction the queue was already
dropping. A liveness probe returning 503 restarts the container, discarding
the in-memory queue while the actual cause (a slow downstream writer) goes
untouched. Both responses make the failure strictly worse than the condition
they detect, and both contradict the CEPA reliability principle already
proven by `TestServeHTTP_ParseErrorStillACKs`: the handler ACKs 200 even on
malformed input specifically so CEPA is never told this endpoint is
unreachable. `/health` always returning 200 is intentional, not an oversight.
Operators should alert on `cee_queue_depth` and `cee_events_dropped_total`
via `/metrics` for degradation, not on the HTTP status of `/health`.

### v5.0 exit criteria

- Windows CI job green, including the no-placeholder assertion.
- `docs/PROMISES.md` covers every claim in README, PRD and `docs/index.md`.
- No claim anywhere lacks either a verifying job or an explicit "unverified"
  label.

## Risks

**The chunk hash tables may not be sufficient for Event Viewer.** go-evtx F1–F4
are the best available hypothesis, not a certainty. v5.0 must be able to
conclude that Event Viewer is unsupported and delete the claim. Fabricating
evidence, or quietly leaving the claim in place, is the failure mode to guard
against.

**The Windows runner is a new CI surface over never-executed code.** Budget for
`writer_windows.go` and `service_windows.go` failing on first contact. That is
the job working as intended, but it will expand v5.0's scope.

**`windres` availability.** The `.syso` is committed, so ordinary builds need no
Windows resource toolchain. Only regenerating it does. Document the
regeneration command next to the `.mc` file.

## Sequencing

| Track | Work | Depends on |
|---|---|---|
| A | go-evtx v0.6.0 durability | nothing |
| B | cee-exporter v4.1.3 truth | nothing |
| A | go-evtx v0.7.0 format correctness | v0.6.0 |
| B | cee-exporter v5.0 verified | v4.1.3; Event Viewer claim only on v0.7.0 |

A and B run concurrently. The only hard coupling is the Event Viewer claim in
V3.
