package evtx

import (
	"encoding/binary"
	"os"
	"testing"
)

// coffMachineAMD64 is IMAGE_FILE_MACHINE_AMD64 from the PE/COFF spec. A COFF
// object file opens with a 2-byte little-endian machine type.
const coffMachineAMD64 = 0x8664

// TestMessageResourcePresent guards the committed Windows message resource.
//
// The .syso is linked into windows/amd64 builds by filename convention — no
// Go code imports it, so nothing else would notice if it were deleted,
// truncated, or rebuilt for the wrong architecture. Without it, Event Viewer
// falls back to "The description for Event ID N ... cannot be found", which
// is the exact defect this resource exists to fix. This test runs on every
// platform so the Linux CI gate catches it.
func TestMessageResourcePresent(t *testing.T) {
	const path = "rsrc_windows_amd64.syso"

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("message resource missing: %v (regenerate with `make winres`)", err)
	}
	if info.Size() == 0 {
		t.Fatal("message resource is empty (regenerate with `make winres`)")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open message resource: %v", err)
	}
	defer func() { _ = f.Close() }()

	var machine uint16
	if err := binary.Read(f, binary.LittleEndian, &machine); err != nil {
		t.Fatalf("read COFF machine type: %v", err)
	}
	if machine != coffMachineAMD64 {
		t.Fatalf("message resource machine type = %#x, want %#x (amd64); "+
			"a resource built for another architecture is silently ignored by the linker",
			machine, coffMachineAMD64)
	}
}
