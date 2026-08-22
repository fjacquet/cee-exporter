//go:build !windows

// BinaryEvtxWriter is the non-Windows writer — writer_evtx_notwindows.go
// carries the same constraint — so this test cannot compile on Windows, where
// the Win32 writer takes its place and carries the client address in its own
// message text.
package evtx_test

import (
	"context"
	"os"
	"testing"
	"time"

	goevtx "github.com/fjacquet/go-evtx"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
)

// TestBinaryEvtxWriter_WritesClientAddress asserts on the bytes that reach the
// file, not on the field map.
//
// The first attempt at this fix added an "IpAddress" key to
// windowsEventToFields and tested windowsEventToFields. That test passed and
// the value never reached the file: go-evtx's EventData schema was closed at
// twelve fields and WriteRecord ignored the key in silence. Reading the record
// back is the only assertion that could have caught it, so it is the one made
// here.
func TestBinaryEvtxWriter_WritesClientAddress(t *testing.T) {
	path := t.TempDir() + "/audit.evtx"

	w, err := evtx.NewBinaryEvtxWriter(path, goevtx.RotationConfig{})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	err = w.WriteEvent(context.Background(), evtx.WindowsEvent{
		EventID:      4663,
		ProviderName: evtx.DefaultProviderName,
		Computer:     "nas01",
		Channel:      "Security",
		TimeCreated:  time.Date(2026, 8, 14, 20, 35, 21, 0, time.UTC),
		ObjectName:   `\\nas01.diab.local\CHECK$\FS01\pstest.txt`,
		ClientAddr:   "10.26.1.222",
	})
	if err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}
	r, err := goevtx.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()

	ev, err := r.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}

	var got string
	var names []string
	for _, d := range ev.EventData {
		names = append(names, d.Name)
		if d.Name == "IpAddress" {
			got = d.Value.String()
		}
	}
	if got != "10.26.1.222" {
		t.Errorf("IpAddress in written record = %q, want %q; EventData names were %v",
			got, "10.26.1.222", names)
	}
}
