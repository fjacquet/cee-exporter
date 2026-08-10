// version_test.go — the build-stamped version must not be a hardcoded literal.
//
// White-box: package main. stdlib only.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestVersion_LdflagsStampReachesTheBinary builds the command with
// -ldflags "-X main.version=..." and asserts the running binary reports that
// value.
//
// The two tests above check the *default* and that it is not a hardcoded
// literal. Neither proves the stamp arrives: a Makefile that dropped the
// -ldflags argument, a rename of the `version` variable, or a change of
// package path would all leave them green while every release shipped a
// binary reporting "dev". That gap was found by audit, not by CI, and every
// release up to v5.1.1 was checked by a human reading startup output instead.
//
// GELF over UDP is used as the output because it needs no listener, no file
// and no privileges — the exporter sends into a closed port and exits. An
// evtx-typed config would route to the Win32 Event Log on Windows and require
// Administrator.
func TestVersion_LdflagsStampReachesTheBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary; skipped under -short")
	}

	const want = "v0.0.0-stamp-probe"

	dir := t.TempDir()
	bin := filepath.Join(dir, "cee-exporter-probe")
	if runtimeIsWindows() {
		bin += ".exe"
	}

	build := exec.Command("go", "build", "-ldflags", "-X main.version="+want, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cfg := filepath.Join(dir, "probe.toml")
	// Port 1 is reserved and closed; UDP send succeeds regardless.
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

	run := exec.Command(bin, "-config", cfg, "-emit-test-events")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run stamped binary: %v\n%s", err, out)
	}

	// The startup line is JSON: {"...","msg":"cee_exporter_starting","version":"..."}
	needle := `"version":"` + want + `"`
	if !strings.Contains(string(out), needle) {
		t.Errorf("stamped binary did not report the ldflags version.\nwant substring: %s\ngot output:\n%s", needle, out)
	}
}

// runtimeIsWindows avoids importing runtime just for one constant in a file
// that is otherwise stdlib-light; the build tag cannot help here because this
// test is meant to run on every platform.
func runtimeIsWindows() bool {
	return os.PathSeparator == '\\'
}
