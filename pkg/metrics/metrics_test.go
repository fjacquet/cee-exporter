package metrics

import (
	"testing"
	"time"
)

func TestStore_LastFsyncUnix(t *testing.T) {
	s := &Store{}

	// Zero value: no fsync yet.
	if got := s.LastFsyncUnix(); got != 0 {
		t.Errorf("initial LastFsyncUnix = %d, want 0", got)
	}

	// Record a known time and verify round-trip.
	knownTime := time.Unix(1_700_000_000, 0)
	s.RecordFsyncAt(knownTime)
	if got := s.LastFsyncUnix(); got != 1_700_000_000 {
		t.Errorf("LastFsyncUnix = %d, want 1700000000", got)
	}

	// Overwrite with a later time.
	laterTime := time.Unix(1_700_000_015, 0)
	s.RecordFsyncAt(laterTime)
	if got := s.LastFsyncUnix(); got != 1_700_000_015 {
		t.Errorf("LastFsyncUnix after update = %d, want 1700000015", got)
	}
}
