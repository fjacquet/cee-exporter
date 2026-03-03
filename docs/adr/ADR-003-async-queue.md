# ADR-003: Use an async worker queue between the HTTP handler and event writers

**Status:** accepted

## Context

The Dell CEPA protocol requires the HTTP handler to respond within 3 seconds or the
publisher treats the server as unreachable and fires SDNAS_CEPP_ALL_SERVERS_UNREACHABLE
alerts. VCAPS mode can deliver batches of thousands of events per PUT request. Writing
events to a GELF or Win32 backend synchronously within the HTTP handler would risk
exceeding the 3-second window under load, and a slow backend would block all incoming
events.

## Decision

We will process events asynchronously via a buffered channel (queue) with a configurable
number of worker goroutines. The HTTP handler enqueues events immediately and returns
HTTP 200 OK. Workers drain the queue and write to the configured backend(s).

## Consequences

The HTTP handler always responds well within the 3-second heartbeat window regardless of
backend latency. Events are processed concurrently by multiple workers. The queue has a
configurable capacity (default 100,000); events are dropped with a WARN log when the
queue is full, preventing unbounded memory growth. Queue depth is exposed via the
/health endpoint for monitoring. The trade-off is that events are not durably queued —
a process crash loses any events in the in-memory queue. Durable queuing (disk, message
broker) is explicitly out of scope for v1.0.
