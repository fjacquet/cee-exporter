# ADR-012: Flush Ticker Ownership in the Writer Layer

**Status:** Accepted
**Date:** 2026-03-04
**Phase:** 9 — Goroutine Scaffolding and fsync

## Context

Phase 9 adds a periodic checkpoint-write to `BinaryEvtxWriter` to bound the data-loss window when the process is interrupted between graceful shutdown signals. The ticker must fire somewhere in the call stack. Candidate layers are:

1. **Queue layer** (`pkg/queue/queue.go`) — a worker goroutine that calls `writer.Flush()` on a ticker
2. **Adapter layer** (`pkg/evtx/BinaryEvtxWriter`) — a goroutine owned by the adapter
3. **go-evtx library layer** (`github.com/fjacquet/go-evtx Writer`) — a goroutine owned by the library's `Writer` struct

## Decision

The flush ticker lives in the **go-evtx library layer**, inside the `Writer` struct.

## Rationale

### Writer interface stability

The `evtx.Writer` interface (defined in `pkg/evtx/writer.go`) has exactly two methods:

```go
type Writer interface {
    WriteEvent(ctx context.Context, e WindowsEvent) error
    Close() error
}
```

cee-exporter has five Writer backends: GELF, Win32 EventLog, Syslog, Beats, and BinaryEvtx. If the flush ticker lived in the queue layer, it would need to call `writer.Flush()`. This would require adding `Flush()` to the `Writer` interface — which would force GELF, Win32, Syslog, and Beats to implement a method they have no meaningful semantics for (network writers do not buffer; Win32 EventLog is synchronous).

Stub implementations (`func (w *GELFWriter) Flush() error { return nil }`) are technically correct but are a code smell: they mislead readers into thinking flushing is a universal operation, and they create surface area for future bugs when someone calls `Flush()` on the wrong backend expecting durability.

### Concern isolation

Only `BinaryEvtxWriter` has a durability concern that benefits from periodic flushing. GELF and Syslog are network protocols where "flush" has no meaning at the application layer. Win32 EventLog is synchronous by design. Isolating the flush goroutine inside the go-evtx `Writer` keeps the durability concern co-located with the implementation that needs it.

### Library encapsulation

go-evtx is a standalone library (`github.com/fjacquet/go-evtx`). Making the periodic flush a first-class feature of the library's `Writer` means any consumer of go-evtx gets the goroutine lifecycle without having to implement it. cee-exporter benefits from this immediately; future consumers (e.g., forensics tools) benefit automatically.

### Adapter layer alternative rejected

Placing the goroutine in `BinaryEvtxWriter` (the cee-exporter adapter) would duplicate the lifecycle logic (done channel, WaitGroup) in cee-exporter rather than in the library where it belongs. It would also prevent go-evtx from advertising its own flush semantics in its public API.

## Consequences

- `evtx.Writer` interface remains at two methods (`WriteEvent`, `Close`) — no breaking change to GELF, Win32, Syslog, or Beats
- `go-evtx` `Writer.Close()` is responsible for signaling the goroutine and performing the final flush
- cee-exporter passes `FlushIntervalSec` to go-evtx at construction time via `goevtx.RotationConfig`
- Phase 10 (open-handle incremental flush) will add `f.Sync()` inside the goroutine once a persistent file handle is available
- Phase 12 will expose `FlushIntervalSec` validation (reject 0 with a startup error)

## Alternatives Considered

| Alternative | Reason Rejected |
|-------------|-----------------|
| Queue layer ticker calling `writer.Flush()` | Requires adding `Flush()` to Writer interface; forces stubs on all backends |
| Adapter-layer goroutine in BinaryEvtxWriter | Lifecycle code in wrong layer; go-evtx cannot advertise its own flush semantics |
| No goroutine (write-on-close only) | Unacceptable for long-running sessions; Phase 9 goal is bounded durability |
