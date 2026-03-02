// Package metrics tracks in-process counters using atomic int64s.
// No external dependency is required; a future Prometheus /metrics endpoint
// can read these values directly.
package metrics

import (
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

	// Current queue depth — set, not incremented
	queueDepth atomic.Int64

	// Timestamp of the last successfully processed event
	lastEventAt atomic.Int64 // Unix nanoseconds
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

// Snapshot returns an immutable point-in-time copy of the counters.
type Snapshot struct {
	EventsReceivedTotal int64
	EventsWrittenTotal  int64
	EventsDroppedTotal  int64
	WriterErrorsTotal   int64
	QueueDepth          int64
	LastEventAt         time.Time
}

// Snapshot captures the current metrics.
func (s *Store) Snapshot() Snapshot {
	return Snapshot{
		EventsReceivedTotal: s.EventsReceivedTotal.Load(),
		EventsWrittenTotal:  s.EventsWrittenTotal.Load(),
		EventsDroppedTotal:  s.EventsDroppedTotal.Load(),
		WriterErrorsTotal:   s.WriterErrorsTotal.Load(),
		QueueDepth:          s.QueueDepth(),
		LastEventAt:         s.LastEventAt(),
	}
}
