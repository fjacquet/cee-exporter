# Deferred debt cleanup — design

**Date:** 2026-08-10
**Status:** approved, not yet implemented
**Tracking:** [#22](https://github.com/fjacquet/cee-exporter/issues/22)
**Target release:** none — rides with whatever ships next

## Problem

Five releases (v4.1.3 → v5.1.1) each parked findings that were reviewed,
judged non-blocking, and recorded in an SDD ledger. Those ledgers were
git-ignored scratch and have been deleted; their contents were captured in
issue #22 first.

Seven items remain. They are not equivalent, and treating them as one
undifferentiated backlog is how such a list survives another five releases
without being executed. Two of them **prevent a defect from recurring**; the
rest clean up after one.

The decision taken: clear all seven, rather than close the cosmetic ones as
"accepted". A list re-read at every release and never executed is worse than
either finishing it or admitting it will not be.

## Scope

| # | Item | Kind |
|---|---|---|
| 1 | Version stamp is not tested against the real build entry point | Verification gap |
| 2 | `make lint` runs no `go mod tidy -diff` check | Guard |
| 3 | `-emit-test-events` exits 0 when `Close()` fails | Correctness |
| 4 | `verify_evtx.py`'s `except` is too wide | Diagnosability |
| 5 | Inert `[listen]`/`[metrics]` addresses in `evtx-oracle`'s TOML | Cosmetic |
| 6 | `docs/windows-verification.md` Cleanup omits section 5's paths | Docs |
| 7 | `formatWin32Message`'s doc comment predates its behaviour | Docs |

Branch protection on `main` was on the original list and is **out of scope
here**: it is a repository setting, not a change to this codebase, and the
tooling in use cannot make the call. It stays in #22 as an action for the
repository owner, with the command recorded there.

## 1. Test the version stamp against the real build entry point

PR #23 added a test that builds with `-ldflags "-X main.version=..."` and
asserts the binary reports it. **Code review found the test cannot detect the
failure it was written for**, and the review is right.

The test supplies the `-ldflags` itself. It therefore proves the Go toolchain
honours `-X main.version` — which was never in doubt. It proves nothing about
whether the *release build* passes those flags, and "a Makefile that dropped
the `-ldflags` argument" is the exact failure the commit message claimed to
close. The test has that hole.

**Decision: build through the Makefile target, with `VERSION` overridden.**

`VERSION` is a make variable and `LDFLAGS` derives from it, so the real entry
point is directly drivable. Verified:

```console
$ make -n build-darwin VERSION=v0.0.0-probe
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w -X main.version=v0.0.0-probe" -o cee-exporter-darwin ./cmd/cee-exporter
```

The rewritten test picks the host-appropriate target, runs it with a probe
version, executes the artifact, and asserts the reported version. This
exercises the Makefile's `LDFLAGS` wiring, `CGO_ENABLED=0` and `-trimpath` —
the things that actually break — rather than the linker's documented
behaviour.

| `runtime.GOOS` | Target | Artifact |
|---|---|---|
| `linux` | `build-linux` | `cee-exporter` |
| `darwin` | `build-darwin` | `cee-exporter-darwin` |
| `windows` | `build-windows` | `cee-exporter.exe` |

Two limits, stated rather than discovered later:

- **The target writes into the repository root**, not a temp dir. The test
  removes the artifact afterwards, including on failure. A developer's own
  build of the same name is overwritten; that is the cost of testing the real
  entry point instead of a copy of it.
- **`make` is required.** GitHub's `windows-latest` image is not guaranteed to
  have it, so the test skips with an explicit reason when `make` is absent
  rather than failing for something unrelated to the stamp. Windows release
  artifacts are cross-compiled from Linux by goreleaser, where the test does
  run.

The review's other finding — set `CGO_ENABLED=0` on the build — is resolved by
this change rather than applied separately: the Makefile target already sets
it, which is part of why building through the target is the better answer.

PR #23 is amended rather than merged and followed. Merging it as-is would land
a test whose commit message overstates what it proves.

## 2. `go mod tidy -diff` in `make lint`

Stale `go.sum` lines were introduced and removed **twice** during v5.1.0
because nothing checks for them. One line closes it:

```make
lint:
	go mod tidy -diff
	golangci-lint run --timeout=5m
```

**Falsifiable:** add a stale line to `go.sum`, `make lint` must fail; remove
it, it must pass.

This touches `lint`, one of the `fjacquet/ci` standard-interface targets that
`CLAUDE.md` says to keep stable. The behaviour stays what the name promises —
`lint` fails on a repository that is not in order — so this is within the
contract rather than a change to it. Recorded here because a future reader
will reasonably ask.

## 3. `-emit-test-events` must not exit 0 when `Close()` fails

`cmd/cee-exporter/main.go` logs a WARN on a `Close()` failure and exits 0.
`Close()` finalises the `.evtx` chunk, so a failure there can leave an
unfinalised file — and `evtx-oracle` depends on that exit code before it even
looks at the artifact.

**Decision: exit 1 on any `Close()` failure, uniformly**, rather than only for
file-backed writers. `-emit-test-events` exists to produce verifiable output;
if any step of producing it failed, say so. A per-writer distinction would
need `main` to interrogate the writer's type, a coupling the code currently
avoids, and would only matter to someone running the flag with `gelf` and
expecting a zero — which neither CI nor the manual protocol does.

**The design point is making it falsifiable.** The exit happens in `main`, so
testing it directly means a subprocess plus a portable way to force `Close()`
to fail — which there is not. Extract the decision instead:

```go
// emitExitCode maps the -emit-test-events path's two failures to a process
// exit code. Close() finalises the .evtx chunk, so a failure there can leave
// an unfinalised file that evtx-oracle would go on to parse.
func emitExitCode(emitErr, closeErr error) int
```

A pure function, table-tested across all four combinations, with `main`
calling it. This is the difference between "fixed" and "provably fixed", and
it is the only item in this set where the question arises.

## 4. Narrow `verify_evtx.py`'s `except`

The `try` wraps the whole per-record loop, so a bug in the assertion logic is
reported as `python-evtx could not open the file` — pointing the next reader
at the file rather than at the script. The exit code is unaffected, so CI
still fails correctly; only the diagnosis misleads.

Narrow it to the `Evtx(path)` open and the `records()` call. **Verification:**
the existing mutations must still fire with their existing messages — the
`ProviderName`, `Channel`, `Level` and `EventData` value mutations all have
recorded expected output to compare against.

## 5–7. Cosmetic and documentation

- **`evtx-oracle`'s generated TOML** carries `[listen]` and `[metrics]`
  addresses that are never bound: `-emit-test-events` exits first. Remove
  them. Verification: the job still passes in CI.
- **`docs/windows-verification.md`'s Cleanup section** removes
  `C:\Windows\Temp\winverify` and the registry key, but section 5 introduces
  paths of its own that it never mentions. An operator following the protocol
  leaves a directory and an event-source registration behind on a shared VM.
- **`formatWin32Message`'s doc comment** predates the current behaviour, noted
  during the v5.0 work and judged cosmetic then.

## Packaging

Two PRs, not one, because item 1 already has a branch open.

- **PR #23, amended.** Item 1 rewrites the test that PR introduced. Amending
  keeps the change and its review in one place; opening an eighth branch to
  fix a test that has not landed yet would leave a reviewer comparing two PRs
  to see what the test finally asserts.
- **A new branch for items 2–7**, one commit each. Six commits, one review
  pass, each revertable alone. Consistent with this repository's history.

Item 1 lands first: items 2–7 branch from a `main` that already has it, so
neither PR carries the other's diff.

**No release.** Nothing here changes what the daemon does: `emitExitCode`
affects a diagnostic flag, and the rest is CI, tooling and documentation. The
CHANGELOG entry goes under `[Unreleased]` and travels with whatever ships
next.

## Success criteria

1. `make ci` green; `make docs` exit 0.
2. Each of items 1–4 has been **watched failing** before being trusted:
   - version stamp: remove `-X main.version` from `LDFLAGS` in the Makefile,
     the test must fail
   - tidy check: a stale `go.sum` line must fail `make lint`
   - `emitExitCode`: the table test must cover all four error combinations and
     fail if the `closeErr` branch returns 0
   - `verify_evtx.py`: the existing mutations must still produce their
     recorded messages
3. Issue #22 is updated with what closed and what remains — branch protection
   stays open, assigned to the repository owner.

## Out of scope

- Branch protection on `main` — repository setting, owner's action, tracked in
  #22.
- Any change to `/Users/fjacquet/Projects/go-evtx`. Standing constraint.
- The knowingly-unfalsifiable `System/TimeCreated` assertion in
  `verify_evtx.py`. It is labelled as defence-in-depth rather than counted as
  a guard, which is the honest treatment; making it falsifiable would mean
  changing go-evtx.
