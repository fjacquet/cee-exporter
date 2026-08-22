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

	// eventsByType and eventsByServer break the scalar event counter down by
	// what happened and where. Two maps rather than one keyed on all three
	// labels: event_type x protocol x server would multiply out, and the two
	// questions are asked separately.
	breakdownMu    sync.RWMutex
	eventsByType   map[eventTypeKey]*atomic.Int64
	eventsByServer map[string]*atomic.Int64

	// eventLabelsDropped counts increments discarded because MaxEventLabels
	// was reached, so a truncated breakdown is visible rather than silent —
	// the same contract peersDropped provides for the publisher label.
	eventLabelsDropped atomic.Int64
}

// MaxEventLabels bounds each of the two event-breakdown maps.
//
// event_type x protocol is naturally bounded — CEE has 19 event names and four
// protocols — but server is only bounded by the estate, and "bounded in
// practice" is not a guarantee. An exporter that grows a label set without
// limit takes down the Prometheus scraping it, so the cap is enforced and
// hitting it increments eventLabelsDropped.
const MaxEventLabels = 128

// eventTypeKey is the composite key for the by-type breakdown. A struct rather
// than a joined string so neither field can be confused with a separator that
// appears inside the other.
type eventTypeKey struct {
	EventType string
	Protocol  string
}

// EventTypeStat is an immutable copy of one (event_type, protocol) count.
type EventTypeStat struct {
	EventType string
	Protocol  string
	Count     int64
}

// RecordEvent counts one event against both breakdowns.
//
// Called once per event, on the receive path, so it must not block: the maps
// take a read lock for the common case of an already-seen key and only escalate
// to a write lock on first sight of one.
func (s *Store) RecordEvent(eventType, protocol, server string) {
	s.recordBreakdown(eventTypeKey{eventType, protocol}, server)
}

func (s *Store) recordBreakdown(k eventTypeKey, server string) {
	// Fast path: both counters already exist, so only their atomics are
	// touched. Also covers the cap case — once a map is full an absent key is
	// never inserted, so escalating to the write lock for it would take an
	// exclusive lock forever, on every event, purely to bump a counter that is
	// already atomic. Measured at 13x slowdown of the healthy recorder and 2x
	// of the scrape under a single flooding publisher, which is precisely the
	// condition MaxEventLabels exists to survive.
	s.breakdownMu.RLock()
	byType, typeFull := s.eventsByType[k], len(s.eventsByType) >= MaxEventLabels
	byServer, serverFull := s.eventsByServer[server], len(s.eventsByServer) >= MaxEventLabels
	s.breakdownMu.RUnlock()

	wantServer := server != ""
	if (byType != nil || typeFull) && (!wantServer || byServer != nil || serverFull) {
		var dropped int64
		if byType != nil {
			byType.Add(1)
		} else {
			dropped++
		}
		if wantServer {
			if byServer != nil {
				byServer.Add(1)
			} else {
				dropped++
			}
		}
		if dropped > 0 {
			s.eventLabelsDropped.Add(dropped)
		}
		return
	}

	s.breakdownMu.Lock()
	defer s.breakdownMu.Unlock()
	if s.eventsByType == nil {
		s.eventsByType = make(map[eventTypeKey]*atomic.Int64, MaxEventLabels)
		s.eventsByServer = make(map[string]*atomic.Int64, MaxEventLabels)
	}
	var dropped int64
	if bumpLabel(s.eventsByType, k) {
		dropped++
	}
	// An empty server is not a label value: it would merge every event whose
	// origin the array did not report into one indistinguishable series.
	if wantServer && bumpLabel(s.eventsByServer, server) {
		dropped++
	}
	if dropped > 0 {
		s.eventLabelsDropped.Add(dropped)
	}
}

// bumpLabel increments m[k], creating the counter on first sight, and reports
// whether the increment was discarded because the map was already at
// MaxEventLabels. The caller holds the write lock.
//
// Both breakdowns need the identical create-or-increment-under-a-cap dance;
// writing it twice inline meant the cap check, the create and the increment
// each existed in two places that had to change together.
func bumpLabel[K comparable](m map[K]*atomic.Int64, k K) bool {
	c := m[k]
	if c == nil {
		if len(m) >= MaxEventLabels {
			return true
		}
		c = &atomic.Int64{}
		m[k] = c
	}
	c.Add(1)
	return false
}

// EventTypeSnapshot returns an immutable copy of the by-type breakdown.
func (s *Store) EventTypeSnapshot() []EventTypeStat {
	s.breakdownMu.RLock()
	defer s.breakdownMu.RUnlock()
	out := make([]EventTypeStat, 0, len(s.eventsByType))
	for k, c := range s.eventsByType {
		out = append(out, EventTypeStat{k.EventType, k.Protocol, c.Load()})
	}
	return out
}

// EventServerStat is an immutable copy of one server's event count.
type EventServerStat struct {
	Server string
	Count  int64
}

// EventServerSnapshot returns an immutable copy of the by-server breakdown.
//
// A slice, matching EventTypeSnapshot: the only consumer ranges over it once
// per scrape, and a map costs 2.4x the time and 2x the allocations to build
// while holding the read lock that long.
func (s *Store) EventServerSnapshot() []EventServerStat {
	s.breakdownMu.RLock()
	defer s.breakdownMu.RUnlock()
	out := make([]EventServerStat, 0, len(s.eventsByServer))
	for k, c := range s.eventsByServer {
		out = append(out, EventServerStat{k, c.Load()})
	}
	return out
}

// EventLabelsDropped returns the number of increments discarded at the cap.
func (s *Store) EventLabelsDropped() int64 { return s.eventLabelsDropped.Load() }

// ResetEventBreakdown clears both breakdowns. Test support, mirroring
// ResetPeers.
func (s *Store) ResetEventBreakdown() {
	s.breakdownMu.Lock()
	defer s.breakdownMu.Unlock()
	s.eventsByType = nil
	s.eventsByServer = nil
	s.eventLabelsDropped.Store(0)
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
	heartbeats      atomic.Int64
}

// PeerStat is an immutable point-in-time copy of one publisher's activity.
type PeerStat struct {
	LastRequestUnix int64
	Registrations   int64
	Heartbeats      int64
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

// RecordPeerHeartbeat counts a CEPA liveness exchange from host — CEE's
// <HeartBeatRequest /> probe, or the OneFS CheckFileRequest whose action marks
// it a heartbeat rather than an event.
//
// Kept apart from RecordPeerRegistration because conflating the two made
// cee_cepa_registrations_total report a registration rate for publishers that
// had never registered. Like registrations it never creates a peer; that is
// RecordPeerRequestAt's job.
func (s *Store) RecordPeerHeartbeat(host string) {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	if p := s.peers[host]; p != nil {
		p.heartbeats.Add(1)
	}
}

// LastEventUnix returns the time of the last processed event as Unix seconds,
// or 0 when none has been processed. Zero rather than time.Unix(0, 0) so an
// exporter that has never seen an event does not report one at the epoch,
// matching LastFsyncUnix's convention.
func (s *Store) LastEventUnix() int64 {
	t := s.LastEventAt()
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// PeerSnapshot returns an immutable copy of the peer table. One call per
// scrape keeps the per-peer series mutually consistent.
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
			Heartbeats:      p.heartbeats.Load(),
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
