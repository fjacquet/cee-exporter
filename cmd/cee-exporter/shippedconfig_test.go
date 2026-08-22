package main

import (
	"os"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestShippedConfigsAreIntact guards the two TOML files this repository ships.
//
// Nothing loaded them before. main.go defaults -config to "config.toml", so the
// Windows CI job — which runs `cee-exporter.exe -emit-test-events` from the
// repository root with no -config flag — silently picks that file up. When an
// edit to config.toml deleted its [output] section, the binary fell back to the
// gelf default, wrote nothing to the Windows Event Log, and the job failed one
// step later with "There is not an event provider ... that matches
// PowerStore-CEPA" — a message that points nowhere near the actual cause, on a
// runner, after a full CI cycle.
//
// Asserting on section presence rather than on values: these files are examples
// and their values are meant to be edited, but a section vanishing is always a
// mistake.
func TestShippedConfigsAreIntact(t *testing.T) {
	for _, path := range []string{"../../config.toml", "../../config.toml.example"} {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s is missing: %v", path, err)
			}

			cfg := defaultConfig()
			md, err := toml.DecodeFile(path, &cfg)
			if err != nil {
				t.Fatalf("%s does not parse: %v", path, err)
			}

			present := map[string]bool{}
			for _, k := range md.Keys() {
				present[k[0]] = true
			}
			for _, section := range []string{"listen", "output", "queue", "logging", "metrics"} {
				if !present[section] {
					t.Errorf("%s has no [%s] section; a shipped config that omits one "+
						"silently falls back to defaults", path, section)
				}
			}

			// The listener is what CEE connects to; an example that cannot tell
			// an operator where to point CEE is not an example.
			if cfg.Listen.Addr == "" {
				t.Errorf("%s leaves listen.addr empty", path)
			}
		})
	}
}
