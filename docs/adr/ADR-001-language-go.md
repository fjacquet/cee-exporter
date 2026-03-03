# ADR-001: Use Go as the implementation language

**Status:** accepted

## Context

The exporter must run as a single binary on both Linux and Windows without requiring a
runtime installation (no Python, JVM, Node.js). It must handle concurrent HTTP requests
within the CEPA 3-second heartbeat timeout. The primary developer has Go expertise. The
project requires cross-compilation from a Linux CI host to a Windows target.

## Decision

We will implement cee-exporter in Go.

## Consequences

Cross-compilation is trivial via GOOS/GOARCH environment variables. The binary has no
external runtime dependencies. Goroutines and channels map naturally to the async queue
pattern required by CEPA timing constraints. Win32 API access is available via
golang.org/x/sys/windows without CGO on the happy path. The trade-off is that pure-Go
binary EVTX generation requires implementing the BinXML binary format (~500-1500 LOC),
which is deferred to v2 (see ADR-004).
