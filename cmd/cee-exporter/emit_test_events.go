package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
	"github.com/fjacquet/cee-exporter/pkg/mapper"
)

// emitTestEvents writes one event per mapped Windows event ID. It exists so an
// operator (and the Windows CI job) can confirm the event source is registered
// against a binary carrying the message resource: if any event renders as "The
// description for Event ID N ... cannot be found", registration is wrong.
//
// The events must be shaped like real mapped events, not merely plausible.
// Until v5.1.0 they left ProviderName, Computer and TimeCreated at their zero
// values, which no event from pkg/mapper ever does. That was not cosmetic: a
// record with an empty ProviderName renders as <Provider></Provider> with no
// Name attribute, and Get-WinEvent throws a NullReferenceException on it —
// measured on Windows Server 2025 against go-evtx v0.7.1, where the same file
// with ProviderName set read back all three records. The fixture was broken in
// a way real traffic is not, so it would have failed CI for a defect no
// operator could ever hit.
func emitTestEvents(w evtx.Writer, hostname string) error {
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []int{4660, 4663, 4670} {
		e := evtx.WindowsEvent{
			EventID:         id,
			ProviderName:    mapper.ProviderName,
			Computer:        hostname,
			TimeCreated:     now,
			CEPAEventType:   "TEST_EVENT",
			ObjectName:      `C:\test\emit-test-events.txt`,
			ObjectType:      "File",
			SubjectUsername: "test-user",
			SubjectDomain:   "TEST",
			Accesses:        "ReadData",
			AccessMask:      "0x1",
			ClientAddr:      "127.0.0.1",
		}
		if err := w.WriteEvent(ctx, e); err != nil {
			return fmt.Errorf("emit test event %d: %w", id, err)
		}
	}
	slog.Info("test_events_emitted", "count", 3, "provider", mapper.ProviderName, "computer", hostname)
	return nil
}
