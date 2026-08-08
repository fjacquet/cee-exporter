// version_test.go — the build-stamped version must not be a hardcoded literal.
//
// White-box: package main. stdlib only.
package main

import "testing"

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
