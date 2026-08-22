package evtx

import (
	"context"
	"errors"
	"testing"
)

// errSerialEvent is the injected per-event failure. Matched with errors.Is,
// never ==: .golangci.yml runs errorlint with comparison: true, and the joined
// error returned by writeBatchSerially is never the sentinel itself.
var errSerialEvent = errors.New("injected per-event failure")

// serialStub is a Writer whose WriteEvent fails for the events named in
// failAt (1-based, matching writeBatchSerially's own "event i/n" wording) and
// records every event it was asked to write, failures included.
type serialStub struct {
	failAt   map[int]bool
	seen     []int
	attempts int
}

func (s *serialStub) WriteEvent(_ context.Context, e WindowsEvent) error {
	s.attempts++
	if s.failAt[s.attempts] {
		return errSerialEvent
	}
	s.seen = append(s.seen, e.EventID)
	return nil
}

func (s *serialStub) WriteBatch(ctx context.Context, events []WindowsEvent) error {
	return writeBatchSerially(ctx, s, events)
}

func (s *serialStub) Close() error { return nil }

// TestWriteBatchSeriallyContinuesPastFailure is the regression guard for the
// semantic change batching introduced on the Win32 and EVTX paths.
//
// Before batching, pkg/queue looped WriteEvent per event and continued past a
// failure: one bad event lost one record. writeBatchSerially returning on the
// first error turned that into losing the whole suffix — up to max_batch (500)
// audit records per transient rejection — because its caller, queue.writeBatch,
// does not re-send. The batch must still report failed, and the good records
// must still land.
func TestWriteBatchSeriallyContinuesPastFailure(t *testing.T) {
	batch := []WindowsEvent{
		{EventID: 1, CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 2, CEPAEventType: "CEPP_FILE_WRITE"},
		{EventID: 3, CEPAEventType: "CEPP_FILE_WRITE"},
	}

	cases := []struct {
		name     string
		failAt   map[int]bool
		wantSeen []int
		wantErr  bool
	}{
		{
			name:     "first event fails, the rest still land",
			failAt:   map[int]bool{1: true},
			wantSeen: []int{2, 3},
			wantErr:  true,
		},
		{
			name:     "middle event fails, prefix and suffix both land",
			failAt:   map[int]bool{2: true},
			wantSeen: []int{1, 3},
			wantErr:  true,
		},
		{
			name:     "every event fails",
			failAt:   map[int]bool{1: true, 2: true, 3: true},
			wantSeen: nil,
			wantErr:  true,
		},
		{
			name:     "no failure",
			failAt:   nil,
			wantSeen: []int{1, 2, 3},
			wantErr:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &serialStub{failAt: tc.failAt}

			err := writeBatchSerially(context.Background(), s, batch)

			if tc.wantErr {
				if err == nil {
					t.Fatal("writeBatchSerially returned nil; a failed batch must report failed")
				}
				if !errors.Is(err, errSerialEvent) {
					t.Errorf("error %v does not wrap the injected failure", err)
				}
			} else if err != nil {
				t.Fatalf("writeBatchSerially: %v", err)
			}

			// Every event must be attempted regardless of earlier failures.
			if s.attempts != len(batch) {
				t.Errorf("attempted %d events, want %d — the batch stopped early",
					s.attempts, len(batch))
			}

			if len(s.seen) != len(tc.wantSeen) {
				t.Fatalf("wrote %v, want %v", s.seen, tc.wantSeen)
			}
			for i := range tc.wantSeen {
				if s.seen[i] != tc.wantSeen[i] {
					t.Fatalf("wrote %v, want %v", s.seen, tc.wantSeen)
				}
			}
		})
	}
}
