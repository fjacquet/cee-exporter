# ADR-004: Defer binary .evtx file generation to v2

**Status:** accepted

## Context

Windows .evtx files use the BinXML binary format, a complex Microsoft-proprietary
serialization format (~500-1500 LOC to implement correctly in pure Go). A pure-Go
BinaryEvtxWriter would allow Linux hosts to produce .evtx files that Winlogbeat or
Windows Event Viewer can consume directly. However, the primary v1.0 use case is GELF
to Graylog (see ADR-002), which does not require .evtx files. Implementing BinXML
correctly requires extensive testing with actual Event Viewer and Winlogbeat versions.

## Decision

We will ship a stub BinaryEvtxWriter in v1.0 that satisfies the Writer interface but
does not produce output. Full BinXML implementation is deferred to v2.

## Consequences

v1.0 ships on schedule without the complexity of BinXML. The Writer interface and
MultiWriter fan-out are already designed to support the v2 writer without changes to
the pipeline. Linux operators who need .evtx output must wait for v2 or use Winlogbeat
with the GELF output as a bridge. The stub is clearly documented as a placeholder in
the source code. See ADR-002 for the GELF primary output decision.
