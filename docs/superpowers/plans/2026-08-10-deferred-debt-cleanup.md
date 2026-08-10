# Deferred Debt Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clear the deferred-debt list in issue #22 — a verification gap, a CI guard, one correctness fix, and two cleanups — so the list stops being re-read at every release without being executed.

**Architecture:** Two PRs. Task 1 amends the already-open PR #23, rewriting its version-stamp test to drive the real Makefile build entry point. Tasks 2–6 land on a fresh branch cut from a `main` that already has Task 1. Nothing here changes daemon behaviour, so there is no release.

**Tech Stack:** Go 1.26.5, GNU make, GitHub Actions, Python 3.12 via `uv`.

**Spec:** `docs/superpowers/specs/2026-08-10-deferred-debt-cleanup-design.md`

## Global Constraints

- **Never modify `/Users/fjacquet/Projects/go-evtx`.** Standing user constraint. It is a separate repository; do not read it, `cd` into it, or run git against it. If something is needed from it, open a `gh issue` on that repo.
- Prefix every shell command with `rtk` (project convention), including inside `&&` chains.
- `make lint` also checks formatting. Use `make format` to fix, never hand-align.
- `errorlint` runs with `comparison: true` — never `==`/`!=` on errors, including in tests. Use `errors.Is`.
- Tests are stdlib-only, white-box (same package), table-driven with `t.Run`. No testify.
- Never use a `_linux.go` suffix. Non-Windows files use `_notwindows.go` with `//go:build !windows`.
- **No release.** The CHANGELOG entry goes under `## [Unreleased]`. Do not tag.
- Branch protection on `main` is **out of scope** — a repository setting, not a code change, and it stays in #22 for the owner.

### A mutation step ends by asserting the mutation landed

Four checks written during the v5.1 work turned out to be incapable of failing, two of which would have *reported a passing mutation test while mutating nothing*. Wherever this plan says "prove it fails", confirm the mutation actually changed the file before trusting the result.

### Item 7 of issue #22 is already done — do not implement it

`#22` lists "`formatWin32Message`'s doc comment predates the current behaviour". It does not. Read `pkg/evtx/writer_windows.go:150-159`: the comment narrates its own correction ("an earlier version of this comment made it … and no longer does"). The issue text transcribed a ledger line describing the state *before* the fix, which landed in that same plan. Task 7 of this plan closes the item as already-resolved rather than inventing work.

---

## File Structure

| File | Responsibility |
|---|---|
| `cmd/cee-exporter/version_test.go` | Modify — replace the ldflags test with one that drives the real Makefile target |
| `Makefile:43-44` | Modify — add `go mod tidy -diff` to `lint` |
| `cmd/cee-exporter/main.go:240-250` | Modify — call the extracted exit-code function |
| `cmd/cee-exporter/emit_test_events.go` | Modify — add `emitExitCode` |
| `cmd/cee-exporter/emit_test_events_test.go` | **Create** — table test for `emitExitCode` |
| `tools/evtx-debug/verify_evtx.py:108-226` | Modify — narrow the `try` |
| `.github/workflows/ci.yml` | Modify — drop the inert TOML stanzas from `evtx-oracle` |
| `docs/windows-verification.md` | Modify — complete the Cleanup section |
| `CHANGELOG.md` | Modify — `[Unreleased]` entry |

`emitExitCode` goes in `emit_test_events.go` rather than `main.go` because it belongs to that flag's behaviour, and `main.go` is already 470+ lines.

---

## Task 1: Test the version stamp through the real build entry point

**Files:**
- Modify: `cmd/cee-exporter/version_test.go` — replace `TestVersion_LdflagsStampReachesTheBinary`

**Branch:** `test/version-stamp`, which is already checked out as PR #23. Amend it; do not open a new branch.

**Interfaces:**
- Consumes: nothing.
- Produces: nothing other tasks use.

**Why this replaces the existing test.** The current test passes `-ldflags` to `go build` itself, so it proves the Go linker honours `-X main.version` — never in doubt. It cannot detect a Makefile or release script that drops the flag, which is the failure its own commit message claimed to close. Code review on PR #23 caught this.

- [ ] **Step 1: Confirm the Makefile target is drivable**

```bash
cd /Users/fjacquet/Projects/cee-exporter
rtk git checkout test/version-stamp
rtk make -n build-darwin VERSION=v0.0.0-probe
```

Expected output contains, on one line:

```text
go build -trimpath -ldflags="-s -w -X main.version=v0.0.0-probe" -o cee-exporter-darwin ./cmd/cee-exporter
```

If `VERSION=` does not appear in the rendered `-ldflags`, stop and report — the whole task rests on that override working.

- [ ] **Step 2: Replace the test**

In `cmd/cee-exporter/version_test.go`, delete `TestVersion_LdflagsStampReachesTheBinary` and its `runtimeIsWindows` helper entirely, and put this in their place. Keep `TestVersion_DefaultIsDev` and `TestVersion_NotHardcodedRelease` untouched.

```go
// TestVersion_ReleaseBuildStampsTheVersion drives the Makefile target a
// release actually uses and asserts the produced binary reports the version
// it was told to.
//
// The two tests above check the default and that it is not a hardcoded
// literal. Neither proves the stamp arrives. An earlier version of this test
// did not either: it passed -ldflags to `go build` itself, which proves the
// Go linker honours -X main.version — never in doubt — while staying green if
// the Makefile dropped the flag. That is the failure CLAUDE.md warns about
// ("a binary reporting dev means the stamp did not reach it") and the one
// worth catching, so this builds through `make build-<goos>` instead. It
// therefore also covers the target's CGO_ENABLED=0 and -trimpath.
//
// Two costs, accepted deliberately:
//   - The target writes into the repository root, not a temp dir, so this
//     overwrites a developer's own build of the same name. Removed afterwards,
//     including on failure.
//   - `make` is required. GitHub's windows-latest image is not guaranteed to
//     have it, so the test skips with a reason rather than failing for
//     something unrelated. Windows release artifacts are cross-compiled from
//     Linux by goreleaser, where this does run.
func TestVersion_ReleaseBuildStampsTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes make and builds a binary; skipped under -short")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH; the release build entry point cannot be driven here")
	}

	target, artifact := releaseBuildTargetFor(runtime.GOOS)
	if target == "" {
		t.Skipf("no Makefile build target for GOOS=%s", runtime.GOOS)
	}

	// Tests run in the package directory; the Makefile lives two levels up.
	repoRoot := filepath.Join("..", "..")
	artifactPath := filepath.Join(repoRoot, artifact)

	const want = "v0.0.0-stamp-probe"

	build := exec.Command("make", target, "VERSION="+want)
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make %s: %v\n%s", target, err, out)
	}
	t.Cleanup(func() { _ = os.Remove(artifactPath) })

	cfg := filepath.Join(t.TempDir(), "probe.toml")
	// GELF over UDP needs no listener, no file and no privileges: the binary
	// sends into a closed port and exits. An evtx config would route to the
	// Win32 Event Log on Windows and demand Administrator.
	const cfgBody = `
[listen]
addr = "127.0.0.1:0"
[output]
type = "gelf"
gelf_host = "127.0.0.1"
gelf_port = 1
gelf_protocol = "udp"
[metrics]
enabled = false
`
	if err := os.WriteFile(cfg, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	abs, err := filepath.Abs(artifactPath)
	if err != nil {
		t.Fatalf("resolve artifact path: %v", err)
	}
	run := exec.Command(abs, "-config", cfg, "-emit-test-events")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run built binary: %v\n%s", err, out)
	}

	needle := `"version":"` + want + `"`
	if !strings.Contains(string(out), needle) {
		t.Errorf("the release build did not stamp the version.\nwant substring: %s\ngot output:\n%s", needle, out)
	}
}

// releaseBuildTargetFor maps a GOOS to the Makefile target that builds a
// release artifact for it and the filename that target writes. Kept beside
// the test because it encodes the Makefile's naming, which the test would
// otherwise duplicate inline three times.
func releaseBuildTargetFor(goos string) (target, artifact string) {
	switch goos {
	case "linux":
		return "build-linux", "cee-exporter"
	case "darwin":
		return "build-darwin", "cee-exporter-darwin"
	case "windows":
		return "build-windows", "cee-exporter.exe"
	default:
		return "", ""
	}
}
```

- [ ] **Step 3: Fix the imports**

The file's import block must be exactly:

```go
import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)
```

`runtime` is new; nothing else changes.

- [ ] **Step 4: Run it**

```bash
rtk make format
rtk go test ./cmd/cee-exporter/ -run TestVersion -v 2>&1 | tail -20
```

Expected: three tests pass. The new one takes a few seconds — it runs a real build.

- [ ] **Step 5: Prove it fails when the Makefile drops the stamp**

This is the failure the test exists for, so it must be watched.

```bash
cp Makefile /tmp/mk.bak
rtk perl -0pi -e 's/^LDFLAGS        := -s -w -X main\.version=\$\(VERSION\)$/LDFLAGS        := -s -w/m' Makefile
rtk grep -n "^LDFLAGS" Makefile   # confirm the mutation landed
rtk go test ./cmd/cee-exporter/ -run TestVersion_ReleaseBuild 2>&1 | tail -5
cp /tmp/mk.bak Makefile
rtk grep -n "^LDFLAGS" Makefile   # confirm restore
```

Expected: `the release build did not stamp the version`, then the restored `LDFLAGS` line containing `-X main.version=$(VERSION)`.

- [ ] **Step 6: Confirm the working tree is clean**

```bash
rtk git status --short | rtk grep -v "^??"
```

Expected: only `cmd/cee-exporter/version_test.go` modified. If `cee-exporter-darwin` (or the host equivalent) appears, `t.Cleanup` did not fire — investigate before committing; a test that litters the tree will be reported as a defect by the next reviewer.

- [ ] **Step 7: Full gate**

```bash
rtk make ci
```

Expected: `0 issues.` and `No vulnerabilities found.`

- [ ] **Step 8: Commit and push**

```bash
rtk git add cmd/cee-exporter/version_test.go
rtk git commit -m "test: drive the real build entry point, not a copy of it

The test this replaces passed -ldflags to \`go build\` itself. That proves the
Go linker honours -X main.version, which was never in doubt, and stays green
if the Makefile drops the flag — the exact failure its own commit message
claimed to close. Code review on this PR caught it.

It now runs \`make build-<goos> VERSION=...\`, the target a release uses, and
asserts the produced binary reports that version. That covers the Makefile's
LDFLAGS wiring, CGO_ENABLED=0 and -trimpath as a side effect.

Watched failing first: with -X main.version removed from LDFLAGS, the test
reports 'the release build did not stamp the version'.

Two costs taken deliberately and stated in the test: the target writes into
the repository root rather than a temp dir, so the artifact is removed
afterwards including on failure; and \`make\` is required, so the test skips
with a reason where it is absent rather than failing for an unrelated reason."
rtk git push
```

- [ ] **Step 9: Wait for CI, then merge**

```bash
rtk gh pr checks 23
```

Expected: all checks pass. Then merge and sync:

```bash
rtk gh pr merge 23 --merge --delete-branch
rtk git checkout main && rtk git pull
```

---

## Task 2: `make lint` fails on an untidy `go.sum`

**Files:**
- Modify: `Makefile:43-44`

**Branch:** cut a new branch from the updated `main` first — everything from Task 2 onward lives here.

```bash
rtk git checkout main && rtk git pull
rtk git checkout -b chore/deferred-debt-cleanup
```

**Interfaces:**
- Consumes: nothing.
- Produces: nothing other tasks use.

**Why.** Stale `go.sum` lines were introduced and removed **twice** during v5.1.0 because nothing checks. This touches `lint`, one of the `fjacquet/ci` standard-interface targets `CLAUDE.md` says to keep stable — the behaviour stays what the name promises, so this is within the contract, but a future reader will ask and the commit message answers.

- [ ] **Step 1: Confirm the tree is currently tidy**

```bash
rtk go mod tidy -diff && echo "tidy"
```

Expected: `tidy`. If not, stop — fix that first, separately, so this task's mutation test means something.

- [ ] **Step 2: Add the check**

`Makefile`, replace the `lint` target:

```make
lint:
	go mod tidy -diff
	golangci-lint run --timeout=5m
```

- [ ] **Step 3: Prove it fails on an untidy `go.sum`**

```bash
cp go.sum /tmp/gosum.bak
echo 'github.com/fjacquet/go-evtx v0.6.0 h1:XfsFY3XbytbH0FwMgvT0uvRBc1fMhaXE4Exoge+BX24=' >> go.sum
rtk grep -c "go-evtx v0.6.0" go.sum   # confirm the mutation landed: expect 1
rtk make lint 2>&1 | tail -5
cp /tmp/gosum.bak go.sum
rtk grep -c "go-evtx v0.6.0" go.sum || echo "restored: stale line gone"
```

Expected: `make lint` exits non-zero and prints a `go mod tidy -diff` complaint naming the extra line.

- [ ] **Step 4: Confirm it passes again**

```bash
rtk make lint
```

Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
rtk git add Makefile
rtk git commit -m "ci: make lint fail on an untidy go.sum

Stale go.sum lines were introduced and removed twice during v5.1.0 — once by a
\`go get\` that left the previous version's hashes behind, once by the pin
moving again — because nothing checks for them. One line closes it.

This touches lint, one of the fjacquet/ci standard-interface targets CLAUDE.md
says to keep stable. The behaviour stays what the name promises: lint fails on
a repository that is not in order. Recorded here because a future reader will
reasonably ask whether the interface changed.

Watched failing: appending a stale v0.6.0 hash to go.sum makes \`make lint\`
exit non-zero naming that line."
```

---

## Task 3: `-emit-test-events` must not exit 0 when `Close()` fails

**Files:**
- Modify: `cmd/cee-exporter/emit_test_events.go` — add `emitExitCode`
- Modify: `cmd/cee-exporter/main.go:240-250` — call it
- Create: `cmd/cee-exporter/emit_test_events_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func emitExitCode(emitErr, closeErr error) int` in `package main`.

**Why.** `Close()` finalises the `.evtx` chunk. A failure there can leave an unfinalised file, and the `evtx-oracle` CI job checks that exit code before it looks at the artifact. Exiting 0 tells CI the output is good when it may not be.

**Why extract rather than fix in place.** The decision happens in `main`, so testing it directly needs a subprocess plus a portable way to force `Close()` to fail — there is not one. A pure function is table-testable, which is the difference between "fixed" and "provably fixed".

- [ ] **Step 1: Write the failing test**

Create `cmd/cee-exporter/emit_test_events_test.go`:

```go
// emit_test_events_test.go — the -emit-test-events path's exit-code contract.
//
// White-box: package main. stdlib only.
package main

import (
	"errors"
	"testing"
)

// TestEmitExitCode covers all four combinations, because the one that was
// wrong is the one nobody thinks about: emit succeeded, Close failed. Close
// finalises the .evtx chunk, so that combination can leave an unfinalised
// file — and the evtx-oracle CI job trusts this exit code before it parses
// the artifact. It used to return 0.
func TestEmitExitCode(t *testing.T) {
	emitFail := errors.New("emit failed")
	closeFail := errors.New("close failed")

	tests := []struct {
		name      string
		emitErr   error
		closeErr  error
		want      int
	}{
		{"both succeeded", nil, nil, 0},
		{"emit failed", emitFail, nil, 1},
		{"close failed", nil, closeFail, 1},
		{"both failed", emitFail, closeFail, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emitExitCode(tt.emitErr, tt.closeErr); got != tt.want {
				t.Errorf("emitExitCode(%v, %v) = %d, want %d", tt.emitErr, tt.closeErr, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
rtk go test ./cmd/cee-exporter/ -run TestEmitExitCode 2>&1 | tail -5
```

Expected: a build failure — `undefined: emitExitCode`.

- [ ] **Step 3: Add the function**

Append to `cmd/cee-exporter/emit_test_events.go`:

```go
// emitExitCode maps the -emit-test-events path's two failures to a process
// exit code.
//
// A Close() failure is not cosmetic here: Close finalises the .evtx chunk, so
// it can leave a file that is present and non-empty but unfinalised. The
// evtx-oracle CI job reads this exit code before it parses the artifact, so
// exiting 0 tells it the output is good when it may not be. Until v5.1.x a
// Close failure logged a warning and exited 0.
//
// The rule is uniform rather than per-writer. Distinguishing a file writer
// from a socket writer would mean main interrogating the writer's type, a
// coupling the code avoids, and would only matter to someone running this
// diagnostic flag against gelf and expecting a zero — which neither CI nor
// the manual protocol does. On a flag whose whole job is producing verifiable
// output, a false failure costs less than a false success.
func emitExitCode(emitErr, closeErr error) int {
	if emitErr != nil || closeErr != nil {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run the test again**

```bash
rtk go test ./cmd/cee-exporter/ -run TestEmitExitCode -v 2>&1 | grep -E "^(=== RUN|    --- |--- |ok|FAIL)"
```

Expected: four subtests pass.

- [ ] **Step 5: Wire it into `main.go`**

Replace `cmd/cee-exporter/main.go:240-250` — the block from `emitErr := emitTestEvents(...)` through `os.Exit(0)` — with:

```go
		emitErr := emitTestEvents(w, hostname)
		if emitErr != nil {
			slog.Error("emit_test_events_failed", "err", emitErr)
		}
		closeErr := w.Close()
		if closeErr != nil {
			slog.Error("emit_test_events_writer_close_failed", "err", closeErr)
		}
		os.Exit(emitExitCode(emitErr, closeErr))
```

Leave the explanatory comment above it (lines 234-239) unchanged — it explains why `os.Exit` is used rather than `return`, which is still true.

Note the log level moves from `slog.Warn` to `slog.Error` for the close failure: it now fails the process, and a line that fails the build should not be logged as a warning.

- [ ] **Step 6: Verify the flag still works end to end**

```bash
rtk go build ./... && echo "build ok"
cat > /tmp/emitcheck.toml <<'TOML'
[listen]
addr = "127.0.0.1:12969"
[output]
type = "gelf"
gelf_host = "127.0.0.1"
gelf_port = 1
gelf_protocol = "udp"
[metrics]
enabled = false
TOML
rtk go run ./cmd/cee-exporter -config /tmp/emitcheck.toml -emit-test-events
echo "exit=$?"
```

Expected: `"msg":"test_events_emitted","count":3` and `exit=0`.

- [ ] **Step 7: Full gate**

```bash
rtk make ci
```

Expected: `0 issues.`, all tests pass, `No vulnerabilities found.`

- [ ] **Step 8: Commit**

```bash
rtk git add cmd/cee-exporter/emit_test_events.go cmd/cee-exporter/emit_test_events_test.go cmd/cee-exporter/main.go
rtk git commit -m "fix(emit): exit non-zero when Close fails, and make that testable

The -emit-test-events path logged a WARN on a Close() failure and exited 0.
Close finalises the .evtx chunk, so that combination can leave a file that is
present and non-empty but unfinalised — and the evtx-oracle CI job reads this
exit code before it parses the artifact. Exiting 0 told it the output was good
when it may not have been.

The rule is uniform rather than per-writer: distinguishing a file writer from
a socket writer would mean main interrogating the writer's type, a coupling
the code avoids, and would only matter to someone running this diagnostic flag
against gelf and expecting a zero. Neither CI nor the manual protocol does.

Extracted as emitExitCode(emitErr, closeErr) rather than fixed in place. The
decision happens in main, so testing it there would need a subprocess plus a
portable way to force Close() to fail, which does not exist. A pure function
is table-tested across all four combinations — including the one that was
wrong, emit-succeeded-close-failed, which nobody thinks about.

The close failure also moves from slog.Warn to slog.Error: it now fails the
process, and a line that fails the build should not read as a warning."
```

---

## Task 4: Narrow `verify_evtx.py`'s `except`

**Files:**
- Modify: `tools/evtx-debug/verify_evtx.py` — the `try` in `check()`, currently opening at line 108 and caught at line 225

**Interfaces:**
- Consumes: nothing.
- Produces: nothing other tasks use. The CLI contract is unchanged: exit 0 pass, 1 fail, 2 bad usage.

**Why.** The `try` wraps the whole per-record loop, so a bug in the *assertion logic* is reported as `python-evtx could not open the file` — pointing the next reader at the file rather than at the script. The exit code is unaffected, so CI still fails correctly; only the diagnosis misleads. That misdirection already cost one investigation cycle during v5.1.0, when a `Get-WinEvent` failure was read as a library fault.

- [ ] **Step 1: Read the current structure**

```bash
rtk sed -n '97,120p' tools/evtx-debug/verify_evtx.py
rtk sed -n '218,230p' tools/evtx-debug/verify_evtx.py
```

You need to see where the `try` opens, where the `with` block sits, and where the `except` catches.

- [ ] **Step 2: Restructure**

The open and the record list are the only operations that should be guarded. `record.xml()` must still run inside the `with` block, because it reads lazily from an mmap that `Evtx.__exit__` closes — that constraint is why the `try` grew wide in the first place and it has not gone away.

Restructure `check()` so that:

1. A narrow `try` wraps only `evtx.Evtx(path)` and `list(log.records())`, returning the `could not open the file` failure on exception. Keep that message text byte-identical — the README and CI logs quote it.
2. The per-record assertion loop runs inside the same `with` block but **outside** the `try`, so an exception raised by the assertion logic propagates as a traceback naming the real line instead of being relabelled.

Update the `NOTE (deviation from task-2-brief.md …)` comment above the `try`: keep the mmap explanation, which is still the reason the loop lives inside `with`, and add that the `try` is deliberately narrower than the `with` so a script bug is not reported as a file problem.

- [ ] **Step 3: Confirm a good file still passes**

```bash
cd /Users/fjacquet/Projects/cee-exporter
cat > /tmp/vv.toml <<'TOML'
[listen]
addr = "127.0.0.1:12968"
[output]
type = "evtx"
evtx_path = "/tmp/vv.evtx"
[metrics]
addr = "127.0.0.1:19968"
TOML
rm -f /tmp/vv.evtx
rtk go run ./cmd/cee-exporter -config /tmp/vv.toml -emit-test-events >/dev/null 2>&1
(cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/vv.evtx); echo "exit=$?"
```

Expected: `OK: … 3 records, IDs [4660, 4663, 4670], all 12 EventData fields` and `exit=0`.

- [ ] **Step 4: Confirm an unreadable file still reports the open failure**

```bash
head -c 200 /dev/urandom > /tmp/garbage.evtx
(cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/garbage.evtx); echo "exit=$?"
```

Expected: a single `FAIL: python-evtx could not open the file: …` line and `exit=1`. The message must be unchanged from before this task.

- [ ] **Step 5: Confirm the existing mutations still fire with their existing messages**

Narrowing the `try` must not weaken any assertion. Run one field mutation and one System mutation.

```bash
cd /Users/fjacquet/Projects/cee-exporter
cp pkg/evtx/writer_evtx_notwindows.go /tmp/w.bak
rtk perl -0pi -e 's/"ObjectServer":      "Security",/"ObjectServer":      "",/' pkg/evtx/writer_evtx_notwindows.go
rtk grep -c '"ObjectServer":      "",' pkg/evtx/writer_evtx_notwindows.go   # confirm the mutation landed: expect 1
rm -f /tmp/vv.evtx && rtk go run ./cmd/cee-exporter -config /tmp/vv.toml -emit-test-events >/dev/null 2>&1
(cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/vv.evtx 2>&1 | head -2)
cp /tmp/w.bak pkg/evtx/writer_evtx_notwindows.go

rtk perl -0pi -e 's/\t\t"Channel":           clip\(channel\),\n//' pkg/evtx/writer_evtx_notwindows.go
rtk grep -c '"Channel":' pkg/evtx/writer_evtx_notwindows.go   # confirm it landed: expect 0
rm -f /tmp/vv.evtx && rtk go run ./cmd/cee-exporter -config /tmp/vv.toml -emit-test-events >/dev/null 2>&1
(cd tools/evtx-debug && rtk uv run python verify_evtx.py /tmp/vv.evtx 2>&1 | head -2)
cp /tmp/w.bak pkg/evtx/writer_evtx_notwindows.go
```

Expected, in order:

```text
FAIL: record 0: ObjectServer = '', want 'Security'
FAIL: record 1: ObjectServer = '', want 'Security'
FAIL: record 0: System/Channel = '', want 'Security'
FAIL: record 1: System/Channel = '', want 'Security'
```

- [ ] **Step 6: Confirm the Go tree is restored**

```bash
rtk git status --short -- pkg/ cmd/ | rtk grep -v "^??" || echo "Go tree clean"
```

Expected: `Go tree clean`. A `cd` inside a compound command has silently defeated this restore twice during the v5.1 work — check it, do not assume.

- [ ] **Step 7: Commit**

```bash
rtk git add tools/evtx-debug/verify_evtx.py
rtk git commit -m "test(evtx): narrow the oracle's except so a script bug is not a file bug

The try wrapped the whole per-record loop, so an exception raised by the
assertion logic was reported as 'python-evtx could not open the file' —
pointing the next reader at the .evtx rather than at the script. The exit code
was unaffected, so CI still failed correctly; only the diagnosis misled.

That is not hypothetical on this project. During v5.1.0 a Get-WinEvent failure
was read as a library fault and produced an upstream issue that had to be
retracted; the cause was our own fixture. A misdirecting error message costs a
whole investigation cycle.

The try now guards only Evtx(path) and records(); the assertion loop runs
inside the same with block, since record.xml() reads lazily from an mmap that
__exit__ closes, but outside the try. The 'could not open the file' text is
unchanged — the README and CI logs quote it.

Verified the narrowing weakened nothing: a good file still passes, a garbage
file still reports the open failure, and the ObjectServer and System/Channel
mutations still produce their recorded messages."
```

---

## Task 5: Drop the inert TOML stanzas from `evtx-oracle`

**Files:**
- Modify: `.github/workflows/ci.yml` — the heredoc in the `evtx-oracle` job's emit step

**Interfaces:**
- Consumes: nothing.
- Produces: nothing other tasks use.

**Why.** `-emit-test-events` calls `os.Exit` before the HTTP listener or the metrics server binds, so `[listen] addr` and `[metrics] addr` are never used. A reader reasonably assumes the metrics server is exercised by this job. It is not.

- [ ] **Step 1: Read the current heredoc**

```bash
rtk grep -n "evtx-ci.toml" -A 14 .github/workflows/ci.yml
```

- [ ] **Step 2: Reduce it to what the binary reads**

Replace the heredoc body with only the two stanzas that matter, and record why the others went:

```yaml
          # Only [output] is read on this path: -emit-test-events exits before
          # the HTTP listener or the metrics server binds, so a [listen] or
          # [metrics] address here would suggest they are exercised when they
          # are not. evtx_path is required by config validation.
          cat > /tmp/evtx-ci.toml <<'TOML'
          [output]
          type = "evtx"
          evtx_path = "/tmp/audit.evtx"
          TOML
```

- [ ] **Step 3: Verify the reduced config is still accepted**

The binary validates config at startup; a missing required key fails there, not in CI. Prove it locally with the exact file the job now writes.

```bash
cd /Users/fjacquet/Projects/cee-exporter
cat > /tmp/evtx-ci.toml <<'TOML'
[output]
type = "evtx"
evtx_path = "/tmp/audit-ci.evtx"
TOML
rm -f /tmp/audit-ci.evtx
rtk go run ./cmd/cee-exporter -config /tmp/evtx-ci.toml -emit-test-events
echo "exit=$?"
rtk ls -l /tmp/audit-ci.evtx
```

Expected: `"msg":"test_events_emitted","count":3`, `exit=0`, and a 69632-byte file. If validation rejects the reduced config, restore the removed keys and report — the job's correctness outranks tidiness.

- [ ] **Step 4: Confirm the workflow still parses**

```bash
rtk python3 -c "import yaml; d=yaml.safe_load(open('.github/workflows/ci.yml')); print(sorted(d['jobs']))"
```

Expected: `['ci', 'evtx-oracle', 'evtx-readback', 'security', 'windows']`

- [ ] **Step 5: Commit**

```bash
rtk git add .github/workflows/ci.yml
rtk git commit -m "ci: drop the inert listen and metrics stanzas from evtx-oracle's config

-emit-test-events calls os.Exit before the HTTP listener or the metrics server
binds, so those two addresses were never used. Leaving them in suggests the
job exercises the metrics endpoint, which it does not.

Verified the reduced config is still accepted: the same two stanzas the job
now writes produce three events and a 69632-byte file locally, exit 0."
```

---

## Task 6: Complete the Cleanup section of the Windows protocol

**Files:**
- Modify: `docs/windows-verification.md` — the `## Cleanup` section at line 462

**Interfaces:**
- Consumes: nothing.
- Produces: nothing other tasks use.

**Why.** Cleanup removes `C:\Windows\Temp\winverify` and the registry key, but section 5 introduces paths of its own that it never mentions. An operator who follows the protocol to the end leaves a directory and an event-source registration on a shared VM.

- [ ] **Step 1: Find every path section 5 creates**

```bash
rtk grep -nE "C:\\\\[A-Za-z0-9_\\\\.-]+" docs/windows-verification.md | rtk sed -n '/^3[0-9][0-9]:/,$p' | head -20
```

Read the hits and list the distinct directories and files section 5 tells the operator to create. Do not guess from this plan — the section is the source of truth, and it may have moved since this was written.

- [ ] **Step 2: Extend the Cleanup block**

Add each path found in Step 1 to the existing PowerShell block, keeping its shape: a `Remove-Item` per path, then a `Test-Path` per path expecting `False`. The registry-key removal already present covers the source registration section 5 also creates — do not duplicate it.

Keep the section's existing opening sentence, which already states the rule ("must be removed afterward and their absence verified — do not leave … a `PowerStore-CEPA` registration on a shared VM").

- [ ] **Step 3: Verify the docs build**

```bash
rtk make docs
```

Expected: exit 0. `mkdocs build --strict` fails on a single broken internal link.

- [ ] **Step 4: Commit**

```bash
rtk git add docs/windows-verification.md
rtk git commit -m "docs: Cleanup missed the paths section 5 introduces

The Cleanup section removed C:\\Windows\\Temp\\winverify and the registry key —
the artifacts of sections 1 through 4. Section 5, added for the saved-log
protocol, creates its own directory and file and never told the operator to
remove them.

The section's own opening sentence says every file copied to the VM must be
removed and its absence verified. It now lists all of them."
```

---

## Task 7: Close out — CHANGELOG, issue #22, PR

**Files:**
- Modify: `CHANGELOG.md` — under `## [Unreleased]`

**Interfaces:**
- Consumes: the commits from Tasks 2–6.
- Produces: the merged branch.

- [ ] **Step 1: Add the CHANGELOG entry**

Under `## [Unreleased]`, before any existing content there, add:

```markdown
### Changed

- `make lint` now fails on an untidy `go.sum`. Stale hash lines were
  introduced and removed twice during v5.1.0 because nothing checked; one
  `go mod tidy -diff` closes it.
- `-emit-test-events` exits non-zero when the writer's `Close()` fails. It
  logged a warning and exited 0, while `Close()` is what finalises the `.evtx`
  chunk — so the `evtx-oracle` CI job could be told the output was good when
  the file was unfinalised. The decision is extracted as `emitExitCode` and
  table-tested across all four error combinations.
- The `evtx-oracle` job's generated config no longer carries `[listen]` and
  `[metrics]` addresses. `-emit-test-events` exits before either binds, so
  they suggested coverage that does not exist.

### Fixed

- `verify_evtx.py` reported a bug in its own assertion logic as
  `python-evtx could not open the file`, pointing the reader at the `.evtx`
  rather than at the script. The `try` now guards only the file open.
- `docs/windows-verification.md`'s Cleanup section omitted the paths section 5
  introduces, so following the protocol left a directory and an event-source
  registration on the test VM.
```

- [ ] **Step 2: Verify docs and the full gate**

```bash
rtk make docs
rtk make ci
```

Expected: docs exit 0, `0 issues.`, `No vulnerabilities found.`

- [ ] **Step 3: Commit and push**

```bash
rtk git add CHANGELOG.md
rtk git commit -m "docs(changelog): record the deferred-debt cleanup

Five items from #22. None changes what the daemon does — the exit-code fix
touches a diagnostic flag, the rest is CI, tooling and documentation — so this
sits under Unreleased and travels with whatever ships next."
rtk git push -u origin chore/deferred-debt-cleanup
```

- [ ] **Step 4: Open the PR**

```bash
rtk gh pr create --base main --head chore/deferred-debt-cleanup \
  --title "chore: clear the deferred-debt list (#22)" \
  --body "Five of the seven items in #22. Nothing here changes daemon behaviour, so there is no release.

| Item | Watched failing |
|---|---|
| \`make lint\` misses an untidy \`go.sum\` | stale hash appended → lint exits non-zero |
| \`-emit-test-events\` exits 0 on \`Close()\` failure | \`emitExitCode\` table-tested on all four combinations |
| \`verify_evtx.py\`'s \`except\` too wide | good file passes, garbage file still reports the open failure, both existing mutations still fire |
| inert \`[listen]\`/\`[metrics]\` in \`evtx-oracle\`'s config | reduced config verified accepted locally |
| Cleanup section omits section 5's paths | docs build |

Two items do not appear here:

- **Branch protection on \`main\`** — a repository setting, not a code change. Stays in #22 for the owner.
- **\`formatWin32Message\`'s doc comment** — already fixed. The issue transcribed a ledger line describing the state before the fix, which landed in that same plan; the comment now narrates its own correction.

Closes the code half of #22.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

- [ ] **Step 5: Wait for CI**

```bash
rtk gh pr checks <N>
```

Expected: all checks pass, including `evtx-oracle` and `evtx-readback` — Task 5 changed the former's config, so confirm it is green rather than assuming.

- [ ] **Step 6: Update issue #22**

Comment on #22 recording which items closed and which remain. Two remain open: branch protection (owner's action, command already recorded there) and the knowingly-unfalsifiable `System/TimeCreated` assertion, which stays labelled as defence-in-depth rather than counted as a guard. Note that `formatWin32Message` was already fixed and the issue text was wrong.

---

## Self-Review

**Spec coverage.** Spec items 1–6 map to Tasks 1–6; the CHANGELOG and issue close-out are Task 7. Spec item 7 (`formatWin32Message`) is deliberately not implemented — verified already fixed, recorded in the Global Constraints and in the PR body. Branch protection is out of scope in both documents.

**Known soft spot.** Task 6 Step 1 asks the implementer to find section 5's paths rather than listing them here. That is deliberate: the section has been edited three times in two days and a hardcoded list in this plan would go stale. The step names the search and the source of truth.

**Ordering dependency.** Task 1 must merge before Task 2 branches, or the second PR carries the first one's diff. Task 1 Step 9 and Task 2's branch step make that explicit.
