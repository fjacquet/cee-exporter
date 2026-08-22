package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

// configSections returns the toml tag of every struct-typed field on Config —
// i.e. every [section] a shipped config is expected to carry.
func configSections() []string {
	var out []string
	t := reflect.TypeOf(Config{})
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Type.Kind() != reflect.Struct {
			continue
		}
		if tag := f.Tag.Get("toml"); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

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
			// Derived from Config's own struct-typed toml tags rather than
			// listed, so a section added tomorrow is guarded the day it is
			// added. The hand-written list omitted [cepa] — the one section
			// whose loss is both silent and total, since pkg/server/register.go
			// states its defaults "DO NOT WORK" against real CEE: falling back
			// to them means no registration, 0x16 CEPP_NOT_FOUND, and every
			// observable on this side green.
			for _, section := range configSections() {
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
