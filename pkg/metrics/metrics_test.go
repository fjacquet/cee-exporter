package metrics

import (
	"fmt"
	"sync"
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

func TestStore_RecordPeerRequestAt(t *testing.T) {
	s := &Store{}

	// No peers recorded yet.
	if got := s.PeerSnapshot(); len(got) != 0 {
		t.Errorf("initial PeerSnapshot has %d entries, want 0", len(got))
	}

	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))

	snap := s.PeerSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PeerSnapshot has %d entries, want 1", len(snap))
	}
	if got := snap["10.0.2.250"].LastRequestUnix; got != 1_700_000_000 {
		t.Errorf("LastRequestUnix = %d, want 1700000000", got)
	}

	// A second request from the same peer updates the stamp in place rather
	// than adding an entry.
	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_010, 0))

	snap = s.PeerSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PeerSnapshot has %d entries after re-stamp, want 1", len(snap))
	}
	if got := snap["10.0.2.250"].LastRequestUnix; got != 1_700_000_010 {
		t.Errorf("LastRequestUnix after re-stamp = %d, want 1700000010", got)
	}
}

func TestStore_RecordPeerRegistration(t *testing.T) {
	s := &Store{}
	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))

	s.RecordPeerRegistration("10.0.2.250")
	s.RecordPeerRegistration("10.0.2.250")

	if got := s.PeerSnapshot()["10.0.2.250"].Registrations; got != 2 {
		t.Errorf("Registrations = %d, want 2", got)
	}
}

// TestStore_RecordPeerRegistrationUnknownPeer confirms a registration for a
// peer that was never stamped — or was rejected at the cap — neither creates
// an entry nor panics. Creating one here would be a second, uncapped path
// into the map.
func TestStore_RecordPeerRegistrationUnknownPeer(t *testing.T) {
	s := &Store{}

	s.RecordPeerRegistration("192.0.2.99")

	if got := s.PeerSnapshot(); len(got) != 0 {
		t.Errorf("PeerSnapshot has %d entries, want 0 — registration must not create a peer", len(got))
	}
}

// TestStore_PeerCap is the cardinality guard. The remote label is bounded
// only by MaxPeers; without this the registry grows with every distinct
// source IP that ever sends a PUT.
func TestStore_PeerCap(t *testing.T) {
	s := &Store{}
	at := time.Unix(1_700_000_000, 0)

	for i := 0; i < MaxPeers; i++ {
		s.RecordPeerRequestAt(fmt.Sprintf("10.0.0.%d", i), at)
	}

	if got := len(s.PeerSnapshot()); got != MaxPeers {
		t.Fatalf("PeerSnapshot has %d entries, want %d", got, MaxPeers)
	}
	if got := s.PeersDropped(); got != 0 {
		t.Errorf("PeersDropped = %d before exceeding the cap, want 0", got)
	}

	// One past the cap.
	s.RecordPeerRequestAt("10.9.9.9", at)

	if got := len(s.PeerSnapshot()); got != MaxPeers {
		t.Errorf("PeerSnapshot has %d entries after exceeding the cap, want %d", got, MaxPeers)
	}
	if _, ok := s.PeerSnapshot()["10.9.9.9"]; ok {
		t.Error("peer past the cap was recorded; the cap does not hold")
	}
	if got := s.PeersDropped(); got != 1 {
		t.Errorf("PeersDropped = %d, want 1 — exceeding the cap must be visible, not silent", got)
	}

	// A peer already in the map is still served after the cap is reached.
	s.RecordPeerRequestAt("10.0.0.0", time.Unix(1_700_000_030, 0))
	if got := s.PeerSnapshot()["10.0.0.0"].LastRequestUnix; got != 1_700_000_030 {
		t.Errorf("existing peer LastRequestUnix = %d after cap reached, want 1700000030", got)
	}
}

func TestStore_ResetPeers(t *testing.T) {
	s := &Store{}
	s.RecordPeerRequestAt("10.0.2.250", time.Unix(1_700_000_000, 0))
	for i := 0; i <= MaxPeers; i++ {
		s.RecordPeerRequestAt(fmt.Sprintf("10.1.0.%d", i), time.Unix(1_700_000_000, 0))
	}

	s.ResetPeers()

	if got := len(s.PeerSnapshot()); got != 0 {
		t.Errorf("PeerSnapshot has %d entries after ResetPeers, want 0", got)
	}
	if got := s.PeersDropped(); got != 0 {
		t.Errorf("PeersDropped = %d after ResetPeers, want 0", got)
	}
}

// TestStore_PeerConcurrency is the -race guard. CEE's NumberOfThreads
// defaults to 20, so concurrent requests from one host are the normal case,
// not an edge case.
func TestStore_PeerConcurrency(t *testing.T) {
	s := &Store{}
	at := time.Unix(1_700_000_000, 0)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			s.RecordPeerRequestAt(fmt.Sprintf("10.0.1.%d", i%4), at)
			s.RecordPeerRegistration(fmt.Sprintf("10.0.1.%d", i%4))
		}(i)
		go func() {
			defer wg.Done()
			_ = s.PeerSnapshot()
		}()
	}
	wg.Wait()

	if got := len(s.PeerSnapshot()); got != 4 {
		t.Errorf("PeerSnapshot has %d entries, want 4", got)
	}
}
