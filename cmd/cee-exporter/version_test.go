// version_test.go — the build-stamped version must not be a hardcoded literal.
//
// White-box: package main. stdlib only.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestVersion_DefaultIsDev verifies the unstamped default. A release build
// overrides this via -ldflags "-X main.version=vX.Y.Z"; an unstamped build
// must say so rather than claiming a specific release.
func TestVersion_DefaultIsDev(t *testing.T) {
	if version != "dev" {
		t.Fatalf("version = %q, want %q — is it hardcoded to a release number?", version, "dev")
	}
}

// TestVersion_NotHardcodedRelease guards against the regression this fixes:
// a literal version string that drifts from the actual release.
func TestVersion_NotHardcodedRelease(t *testing.T) {
	for _, bad := range []string{"1.0.0", "v1.0.0", "4.1.2", "v4.1.2"} {
		if version == bad {
			t.Fatalf("version is hardcoded to %q; it must be set by -ldflags at build time", bad)
		}
	}
}

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
// The output path is overridden onto a temp dir because the recipes' output
// filename is a plain Makefile variable (BINARY_NAME / BINARY_DARWIN /
// BINARY_WINDOWS), not a literal baked into the recipe — a command-line
// assignment redirects it without touching the recipe or its LDFLAGS. What
// this test actually asserts, the -X main.version stamp, still comes from
// the real recipe: same LDFLAGS, same -trimpath, same CGO_ENABLED=0.
//
// `make` is required. GitHub's windows-latest image is not guaranteed to
// have it, so the test skips with a reason rather than failing for something
// unrelated. Windows release artifacts are cross-compiled from Linux by
// goreleaser, where this does run.
func TestVersion_ReleaseBuildStampsTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes make and builds a binary; skipped under -short")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH; the release build entry point cannot be driven here")
	}

	target, binVar := releaseBuildTargetFor(runtime.GOOS)
	if target == "" {
		t.Skipf("no Makefile build target for GOOS=%s", runtime.GOOS)
	}

	probePath := filepath.Join(t.TempDir(), "cee-exporter-probe")
	if runtime.GOOS == "windows" {
		probePath += ".exe"
	}
	// The recipes use the `VAR=value command` prefix form, which is POSIX
	// shell syntax, so make dispatches them through sh even on Windows (via
	// MSYS/Git Bash) rather than cmd.exe. Two hazards follow from that:
	//   - -o $(BINARY_DARWIN) etc. is expanded unquoted, so a path containing
	//     whitespace would be split by the shell and the build would fail for
	//     a reason unrelated to the version stamp. t.TempDir() paths do not
	//     normally contain spaces, but guard it rather than assume.
	//   - on Windows, probePath is a native backslash path, and sh treats a
	//     backslash as an escape character: \U, \A, \L, \T, \0, \c and so on
	//     are consumed, turning C:\Users\... into garbage like C:UsersRUNNER
	//     that make(1) still resolves — relative to the repo root, dropping a
	//     bogus file exactly where Finding A's fix exists to prevent. Go's -o
	//     accepts forward slashes on Windows and MSYS sh leaves them alone, so
	//     the override sent to make is forward-slashed; probePath itself,
	//     used below to run the binary, stays in native form.
	if strings.ContainsAny(probePath, " \t") {
		t.Skip("temp dir path contains whitespace; the Makefile recipe expands -o unquoted")
	}

	const want = "v0.0.0-stamp-probe"

	// Tests run in the package directory; the Makefile lives two levels up.
	repoRoot := filepath.Join("..", "..")
	build := exec.Command("make", target,
		"VERSION="+want,
		binVar+"="+filepath.ToSlash(probePath))
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make %s: %v\n%s", target, err, out)
	}
	if _, err := os.Stat(probePath); err != nil {
		t.Fatalf("make %s exited 0 but did not write %s (is the %s override reaching the recipe?): %v",
			target, probePath, binVar, err)
	}

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

	run := exec.Command(probePath, "-config", cfg, "-emit-test-events")
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
// release artifact for it and the Makefile variable name that target's
// recipe uses for its output path (BINARY_NAME / BINARY_DARWIN /
// BINARY_WINDOWS). Kept beside the test because it encodes the Makefile's
// naming, which the test would otherwise duplicate inline three times.
func releaseBuildTargetFor(goos string) (target, binVar string) {
	switch goos {
	case "linux":
		return "build-linux", "BINARY_NAME"
	case "darwin":
		return "build-darwin", "BINARY_DARWIN"
	case "windows":
		return "build-windows", "BINARY_WINDOWS"
	default:
		return "", ""
	}
}
