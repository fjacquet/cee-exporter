package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
)

// emitTestEvents writes one event per mapped Windows event ID. It exists so an
// operator (and the Windows CI job) can confirm the event source is registered
// against a binary carrying the message resource: if any event renders as "The
// description for Event ID N ... cannot be found", registration is wrong.
func emitTestEvents(w evtx.Writer) error {
	ctx := context.Background()
	for _, id := range []int{4660, 4663, 4670} {
		e := evtx.WindowsEvent{
			EventID:         id,
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
	slog.Info("test_events_emitted", "count", 3)
	return nil
}
