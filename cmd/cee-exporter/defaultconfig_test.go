package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestDefaultConfig_EVTXRotationEnabled pins the three rotation fields.
//
// Zero means "unlimited"/"disabled" for all three, and TOML decoding leaves a
// field alone when the file omits it — so a config that never mentions
// rotation takes whatever defaultConfig set. These were unset while
// config.toml advertised 100/100/24, which meant the documented defaults did
// not exist at runtime and a long-running deployment grew one .evtx file
// without bound.
func TestDefaultConfig_EVTXRotationEnabled(t *testing.T) {
	cfg := defaultConfig()

	if cfg.Output.MaxFileSizeMB != 100 {
		t.Errorf("MaxFileSizeMB = %d, want 100 (0 means unlimited — no rotation)", cfg.Output.MaxFileSizeMB)
	}
	if cfg.Output.MaxFileCount != 100 {
		t.Errorf("MaxFileCount = %d, want 100 (0 means unlimited — no retention limit)", cfg.Output.MaxFileCount)
	}
	if cfg.Output.RotationIntervalH != 24 {
		t.Errorf("RotationIntervalH = %d, want 24 (0 means disabled)", cfg.Output.RotationIntervalH)
	}
}

// TestDefaultConfig_SurvivesConfigOmittingRotation walks the real load path:
// defaults, then DecodeFile over a config that says nothing about rotation.
// This is the case the bug actually hit — asserting defaultConfig alone would
// not prove the value survives decoding.
func TestDefaultConfig_SurvivesConfigOmittingRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const noRotation = `
[output]
type = "evtx"
evtx_path = "/var/log/cee.evtx"
`
	if err := os.WriteFile(path, []byte(noRotation), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg := defaultConfig()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decoding config: %v", err)
	}

	if cfg.Output.Type != "evtx" {
		t.Fatalf("Type = %q, want evtx — the file was not applied at all", cfg.Output.Type)
	}
	if cfg.Output.MaxFileSizeMB != 100 || cfg.Output.MaxFileCount != 100 || cfg.Output.RotationIntervalH != 24 {
		t.Errorf("rotation after decode = %d MiB / %d files / %d h, want 100/100/24 — a config that omits rotation must keep the defaults",
			cfg.Output.MaxFileSizeMB, cfg.Output.MaxFileCount, cfg.Output.RotationIntervalH)
	}
}

// TestDefaultConfig_ExplicitZeroStillDisables: 0 is a legitimate value meaning
// "no limit". Defaulting must not take that away from an operator who asks for
// it explicitly.
func TestDefaultConfig_ExplicitZeroStillDisables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const explicitZero = `
[output]
type = "evtx"
evtx_path = "/var/log/cee.evtx"
max_file_size_mb = 0
max_file_count = 0
rotation_interval_h = 0
`
	if err := os.WriteFile(path, []byte(explicitZero), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg := defaultConfig()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decoding config: %v", err)
	}

	if cfg.Output.MaxFileSizeMB != 0 || cfg.Output.MaxFileCount != 0 || cfg.Output.RotationIntervalH != 0 {
		t.Errorf("rotation = %d/%d/%d, want 0/0/0 — an explicit 0 must stay 0",
			cfg.Output.MaxFileSizeMB, cfg.Output.MaxFileCount, cfg.Output.RotationIntervalH)
	}
}
