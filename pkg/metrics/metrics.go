// Package metrics tracks in-process counters using atomic int64s.
// No external dependency is required; a future Prometheus /metrics endpoint
// can read these values directly.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// M is the singleton metrics store.  It is safe for concurrent use.
var M = &Store{}

// Store holds all application counters and gauges.
type Store struct {
	EventsReceivedTotal atomic.Int64
	EventsWrittenTotal  atomic.Int64
	EventsDroppedTotal  atomic.Int64
	WriterErrorsTotal   atomic.Int64

	// EventsTruncatedTotal counts events with at least one field capped before
	// handing off to the EVTX writer. An oversized field would otherwise reach
	// go-evtx's record-size limit.
	EventsTruncatedTotal atomic.Int64

	// Current queue depth — set, not incremented
	queueDepth atomic.Int64

	// Timestamp of the last successfully processed event
	lastEventAt atomic.Int64 // Unix nanoseconds

	// lastFsyncAt records when the EVTX writer last successfully called f.Sync().
	// Stored as Unix seconds (not nanoseconds) to match Prometheus convention.
	lastFsyncAt atomic.Int64 // Unix seconds

	// peers tracks CEPA publishers — CEE servers, not NAS Data Movers — by
	// host. Guarded by peersMu rather than being an atomic, because the map
	// itself is mutated on first sight of a peer. Lazily initialised: a
	// zero-value Store must be usable.
	peersMu sync.RWMutex
	peers   map[string]*peerStat

	// peersDropped counts peers rejected because MaxPeers was reached, so
	// that hitting the cap is visible rather than silent.
	peersDropped atomic.Int64
}

// SetQueueDepth records the current queue depth.
func (s *Store) SetQueueDepth(n int) {
	s.queueDepth.Store(int64(n))
}

// QueueDepth returns the current queue depth.
func (s *Store) QueueDepth() int64 {
	return s.queueDepth.Load()
}

// RecordEventAt stamps the last-event-at timestamp as now.
func (s *Store) RecordEventAt() {
	s.lastEventAt.Store(time.Now().UnixNano())
}

// LastEventAt returns the time of the last processed event (zero if none).
func (s *Store) LastEventAt() time.Time {
	ns := s.lastEventAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// RecordFsyncAt records the time of the last successful fsync.
// Called from the go-evtx OnFsync callback in buildWriter().
func (s *Store) RecordFsyncAt(t time.Time) {
	s.lastFsyncAt.Store(t.Unix())
}

// LastFsyncUnix returns the Unix timestamp (seconds) of the last fsync.
// Returns 0 if no fsync has occurred yet.
func (s *Store) LastFsyncUnix() int64 {
	return s.lastFsyncAt.Load()
}

// Snapshot returns an immutable point-in-time copy of the counters.
type Snapshot struct {
	EventsReceivedTotal  int64
	EventsWrittenTotal   int64
	EventsDroppedTotal   int64
	WriterErrorsTotal    int64
	EventsTruncatedTotal int64
	QueueDepth           int64
	LastEventAt          time.Time
	LastFsyncUnix        int64
}

// Snapshot captures the current metrics.
func (s *Store) Snapshot() Snapshot {
	return Snapshot{
		EventsReceivedTotal:  s.EventsReceivedTotal.Load(),
		EventsWrittenTotal:   s.EventsWrittenTotal.Load(),
		EventsDroppedTotal:   s.EventsDroppedTotal.Load(),
		WriterErrorsTotal:    s.WriterErrorsTotal.Load(),
		EventsTruncatedTotal: s.EventsTruncatedTotal.Load(),
		QueueDepth:           s.QueueDepth(),
		LastEventAt:          s.LastEventAt(),
		LastFsyncUnix:        s.LastFsyncUnix(),
	}
}

// MaxPeers bounds the cardinality of the remote label on the CEPA peer
// metrics. Publishers are CEE servers — a handful in any real deployment —
// so this is far above the real ceiling and well below anything that would
// strain the registry. Without it, a port scanner or misconfigured client
// would grow the map without limit.
const MaxPeers = 64

// peerStat is the mutable per-publisher state. The map entry is a pointer so
// that stamping an existing peer needs only a read lock on the map.
type peerStat struct {
	lastRequestUnix atomic.Int64
	registrations   atomic.Int64
}

// PeerStat is an immutable point-in-time copy of one publisher's activity.
type PeerStat struct {
	LastRequestUnix int64
	Registrations   int64
}

// RecordPeerRequestAt stamps the time of the most recent CEPA request from
// host. Called on every PUT — handshake, event batch, or failure — because
// the question it answers is whether the publisher is still talking to us.
//
// If host is not already known and the store is at MaxPeers, the peer is not
// recorded and peersDropped is incremented instead.
func (s *Store) RecordPeerRequestAt(host string, t time.Time) {
	unix := t.Unix()

	s.peersMu.RLock()
	p := s.peers[host]
	s.peersMu.RUnlock()
	if p != nil {
		p.lastRequestUnix.Store(unix)
		return
	}

	s.peersMu.Lock()
	defer s.peersMu.Unlock()

	// Re-check under the write lock: another goroutine may have created this
	// peer between the RUnlock above and this Lock.
	if p := s.peers[host]; p != nil {
		p.lastRequestUnix.Store(unix)
		return
	}
	if len(s.peers) >= MaxPeers {
		s.peersDropped.Add(1)
		return
	}
	if s.peers == nil {
		s.peers = make(map[string]*peerStat, MaxPeers)
	}
	fresh := &peerStat{}
	fresh.lastRequestUnix.Store(unix)
	s.peers[host] = fresh
}

// RecordPeerRegistration counts a CEPA handshake from host, in either dialect:
// PowerStore's <RegisterRequest> or PowerScale's <CheckFileRequest> with
// action 9.
//
// Both are sent per heartbeat, not once per connection, so this is a heartbeat
// rate and not a count of distinct registrations — measured against three live
// CEE 9.2.0.0 publishers on 2026-08-11, where it incremented once per
// heartbeat. A OneFS event (action 11) is not a handshake and is not counted.
//
// It never creates a peer: a peer is created only by RecordPeerRequestAt,
// which enforces MaxPeers. A registration for an unknown host — or for one
// rejected at the cap — is a no-op.
func (s *Store) RecordPeerRegistration(host string) {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	if p := s.peers[host]; p != nil {
		p.registrations.Add(1)
	}
}

// PeerSnapshot returns an immutable copy of the peer table. One call per
// scrape keeps the two per-peer series mutually consistent.
func (s *Store) PeerSnapshot() map[string]PeerStat {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	if len(s.peers) == 0 {
		return nil
	}
	out := make(map[string]PeerStat, len(s.peers))
	for host, p := range s.peers {
		out[host] = PeerStat{
			LastRequestUnix: p.lastRequestUnix.Load(),
			Registrations:   p.registrations.Load(),
		}
	}
	return out
}

// PeersDropped returns the number of peers rejected because MaxPeers was
// reached.
func (s *Store) PeersDropped() int64 {
	return s.peersDropped.Load()
}

// ResetPeers clears the peer table and the drop counter. Provided for test
// isolation, matching the singleton-reset convention the other tests use.
func (s *Store) ResetPeers() {
	s.peersMu.Lock()
	defer s.peersMu.Unlock()
	s.peers = nil
	s.peersDropped.Store(0)
}
