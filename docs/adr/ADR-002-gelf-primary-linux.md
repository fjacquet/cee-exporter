# ADR-002: Use GELF as the primary Linux output format

**Status:** accepted

## Context

On Linux, the natural output for a SIEM integration is GELF (Graylog Extended Log Format)
over UDP/TCP to a Graylog instance. The alternative is to produce binary .evtx files that
Winlogbeat can forward, but this requires implementing the Windows BinXML binary format.
Most Linux-based security operations teams use Graylog or an ELK stack with GELF/Syslog
inputs rather than Winlogbeat. GELF is a well-supported, JSON-based wire protocol with
UDP and TCP transport options.

## Decision

We will use GELF 1.1 over UDP (default) or TCP (configurable) as the primary Linux
output format. The Win32 EventLog writer will be the primary Windows output.

## Consequences

GELF implementation is straightforward: serialize a JSON object and send over UDP/TCP.
No binary format to implement. Operators with Graylog can receive events immediately.
Operators without Graylog can use the TCP GELF receiver in other tools (Logstash, Vector).
The downside is that native Windows Event Log consumers cannot consume GELF directly;
for Windows consumption, the Win32 writer (or a future Beats writer) is needed. UDP GELF
is fire-and-forget and can lose packets at high volume — TCP is recommended for production
(documented in README). See ADR-004 for the binary EVTX deferral decision.
