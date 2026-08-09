//go:build windows

package evtx

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

const eventLogKeyPrefix = `SYSTEM\CurrentControlSet\Services\EventLog\Application\`

// TestEnsureEventSource_PointsAtCurrentExecutable is the assertion the whole
// message-resource change exists to satisfy: after registration, the source's
// EventMessageFile must name this executable, not EventCreate.exe. If it
// names EventCreate.exe, Windows cannot resolve IDs 4660/4663/4670 and Event
// Viewer renders the placeholder text.
//
// Requires Administrator rights to write the registry key.
func TestEnsureEventSource_PointsAtCurrentExecutable(t *testing.T) {
	const exe = `C:\test\cee-exporter.exe`

	if err := ensureEventSource(exe); err != nil {
		t.Fatalf("ensureEventSource: %v", err)
	}
	t.Cleanup(func() {
		_ = removeEventSource()
	})

	got, err := readEventMessageFile()
	if err != nil {
		t.Fatalf("read EventMessageFile: %v", err)
	}
	if got != exe {
		t.Fatalf("EventMessageFile = %q, want %q", got, exe)
	}
	if strings.Contains(strings.ToLower(got), "eventcreate") {
		t.Fatalf("EventMessageFile still points at EventCreate.exe: %q", got)
	}
}

// TestEnsureEventSource_RepointsStaleSource covers the upgrade path. Every
// host that ran a previous version has a PowerStore-CEPA source pointing at
// EventCreate.exe. eventlog.Install does not repoint an existing source, so
// without this the placeholder text would persist forever on exactly the
// machines that already have the product installed.
func TestEnsureEventSource_RepointsStaleSource(t *testing.T) {
	const stale = `C:\Windows\System32\EventCreate.exe`
	const current = `C:\test\cee-exporter.exe`

	if err := writeEventMessageFile(stale); err != nil {
		t.Fatalf("seed stale source: %v", err)
	}
	t.Cleanup(func() {
		_ = removeEventSource()
	})

	if err := ensureEventSource(current); err != nil {
		t.Fatalf("ensureEventSource on stale source: %v", err)
	}

	got, err := readEventMessageFile()
	if err != nil {
		t.Fatalf("read EventMessageFile: %v", err)
	}
	if got != current {
		t.Fatalf("stale source was not repointed: EventMessageFile = %q, want %q", got, current)
	}
}

// readEventMessageFile reads the registered EventMessageFile for the source.
func readEventMessageFile() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, eventLogKeyPrefix+win32SourceName, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close() //nolint:errcheck

	v, _, err := k.GetStringValue("EventMessageFile")
	return v, err
}

// writeEventMessageFile seeds a source with a chosen EventMessageFile, so the
// upgrade path can be tested without installing an old build first.
func writeEventMessageFile(path string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, eventLogKeyPrefix+win32SourceName, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close() //nolint:errcheck

	return k.SetStringValue("EventMessageFile", path)
}

// removeEventSource deletes the registry key, so tests do not leak state.
func removeEventSource() error {
	return registry.DeleteKey(registry.LOCAL_MACHINE, eventLogKeyPrefix+win32SourceName)
}
