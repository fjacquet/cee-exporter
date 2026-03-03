# Feature Landscape — cee-exporter v2.0

**Domain:** Go audit-event daemon — operations and output expansion
**Researched:** 2026-03-03
**Scope:** Six new features added to existing v1.0 daemon

---

## Context: What v1.0 Already Provides

The existing daemon ships a `Writer` interface, a `pkg/metrics` atomic-counter store
(`EventsReceivedTotal`, `EventsDroppedTotal`, `queueDepth`), a `pkg/server` HTTP mux,
and a `MultiWriter` fan-out. Each new feature slots in cleanly because the interfaces
are already defined. No architectural rewrites are needed.

---

## Table Stakes

Features that operators expect from a production daemon of this type. Missing = the
daemon cannot be deployed in a managed environment.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Prometheus `/metrics` endpoint | Standard ops contract for any Go daemon in 2026; every Kubernetes/VM monitoring stack scrapes it | Low | Atomic counters already exist in `pkg/metrics`; just need to wrap with `client_golang` and mount `/metrics` on the HTTP mux |
| Systemd unit file | Linux production deployment without a unit file is not repeatable; ops teams will not manage bare binaries | Low | Pure text artifact; no code changes required, but must use `Type=notify` for correctness |
| Windows Service registration | Windows deployments expect SCM-managed services; NSSM is a workaround, not a solution | Medium | `golang.org/x/sys/windows/svc` + `svc/mgr` already pulled in for Win32 EventLog; same module handles service lifecycle |
| SyslogWriter (RFC 5424) | Every SIEM and log aggregator speaks syslog; needed for environments that do not run Graylog or Elastic | Medium | Field mapping from `WindowsEvent` is well-defined; structured data (SD-ELEMENT) carries audit metadata |

---

## Differentiators

Features beyond table stakes that expand the SIEM target matrix and justify v2.0.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| BeatsWriter (Lumberjack v2) | Enables direct ingestion by Logstash Beats Input and Graylog Beats Input without deploying a separate agent | Medium | `github.com/elastic/go-lumber/client/v2` provides the client; events are `[]map[string]interface{}` — same shape as GELF fields |
| BinaryEvtxWriter (pure Go BinXML) | Produces native `.evtx` files on Linux, readable by Event Viewer and any forensics tool; closes the only gap in cross-platform EVTX fidelity | High | No existing pure-Go writer library found; must implement EVTX file header, chunk header, BinXML token stream from scratch per [MS-EVEN6] |

---

## Anti-Features

Features to explicitly NOT build in v2.0.

| Anti-Feature | Why Avoid | What to Do Instead |
|--------------|-----------|-------------------|
| MSI installer for Windows Service | Scope explosion; 80% of the value is the service binary + `install`/`uninstall` subcommand | Ship `cee-exporter.exe install` / `uninstall` CLI subcommands using `svc/mgr` |
| NSSM dependency | External binary, not reproducible, breaks single-artifact deploy contract | Use native `svc/mgr` — same module already imported |
| Prometheus push gateway | Pull model is correct for a long-running daemon; push adds complexity and a new dependency | Use standard scrape endpoint at `/metrics` |
| RFC 3164 (legacy BSD syslog) | Unstructured; cannot carry audit fields reliably | RFC 5424 structured data covers all fields |
| EVTX chunked streaming | EVTX chunks are 65 536 bytes; streaming individual events mid-chunk is not how the format works | Write per-event records, flush chunk on Close or on chunk-full |
| Alerting / Grafana dashboards | Out of scope for the daemon itself; operational concern | Document recommended PromQL queries in docs |

---

## Feature-by-Feature Detail

### Feature 1: Prometheus `/metrics` Endpoint

**What operators expect:**

- Standard Prometheus text exposition format (OpenMetrics-compatible) at `GET /metrics`
- Metrics scrape in < 50 ms with no side effects
- Counters follow the `_total` naming convention (Prometheus convention, not optional)
- A gauge for queue depth (can go up and down — not a counter)

**Standard patterns (HIGH confidence — official Prometheus docs):**

```
cee_events_received_total   counter  — events accepted by the HTTP handler
cee_events_dropped_total    counter  — events that could not be enqueued (queue full)
cee_queue_depth             gauge    — current async queue occupancy
cee_writer_errors_total     counter  — write failures across all backends
```

`EventsReceivedTotal` and `EventsDroppedTotal` in `pkg/metrics` are `atomic.Int64` —
wrap them with `prometheus.NewCounterFunc` (reads the atomic, returns float64). Queue
depth is `prometheus.NewGaugeFunc` calling `metrics.M.QueueDepth()`. This avoids
duplicating state; Prometheus reads from the existing atomics.

**Minimal correct implementation:**

1. Add `github.com/prometheus/client_golang` to `go.mod`.
2. Create `pkg/prommetrics/prommetrics.go`: register three metrics on a custom
   `prometheus.Registry`, return an `http.Handler` via `promhttp.HandlerFor`.
3. In `cmd/cee-exporter/main.go`: mount `/metrics` on the existing `http.ServeMux`.

**Integration constraint:** The existing mux already handles `/health` and the CEPA
PUT handler. Mount `/metrics` as a fourth route. Do NOT open a second port unless
config requests it — the operator scrapes the same port they've already opened.

**Complexity assessment:** Low — 60-80 LOC new code. The atomic counters already
exist and are instrumented at every event-path boundary.

---

### Feature 2: Systemd Unit File

**What operators expect:**

- Drop into `/etc/systemd/system/cee-exporter.service`
- `systemctl enable --now cee-exporter` starts the daemon at boot
- Automatic restart on crash (`Restart=on-failure`)
- Proper shutdown on `systemctl stop` (SIGTERM → daemon exits cleanly)
- No root required (dedicated `cee-exporter` system user)

**Standard patterns (MEDIUM confidence — verified against current systemd docs):**

```ini
[Unit]
Description=Dell CEPA Audit Event Exporter
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=cee-exporter
Group=cee-exporter
ExecStart=/usr/local/bin/cee-exporter -config /etc/cee-exporter/config.toml
Restart=on-failure
RestartSec=5s
WatchdogSec=30s

# Security hardening
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
ReadWritePaths=/var/log/cee-exporter

[Install]
WantedBy=multi-user.target
```

**Key constraint — `Type=notify`:** With `Type=notify`, systemd waits until the
daemon calls `sd_notify(0, "READY=1")` before marking the service active. Go daemons
must import `github.com/coreos/go-systemd/daemon` and call
`daemon.SdNotify(false, daemon.SdNotifyReady)` once the HTTP server is listening.
Without this, `systemctl start` hangs until timeout. If `sd_notify` is not
implemented, fall back to `Type=simple` — less informative but functional.

**WatchdogSec:** If configured, the daemon must call `sd_notify(0, "WATCHDOG=1")`
periodically. Missed pings → systemd restarts the daemon. Optional for v2.0.

**Complexity assessment:** Low — unit file is a text artifact. If adding `sd_notify`
calls to the daemon, add ~20 LOC to `main.go` plus `go-systemd` dependency. Can be
shipped as a separate file in `deploy/linux/` without touching Go source at all if
`Type=simple` is acceptable.

---

### Feature 3: Windows Native Service

**What operators expect:**

- `cee-exporter.exe install` registers the service in SCM, sets auto-start
- `cee-exporter.exe uninstall` removes the service
- `cee-exporter.exe start` / `stop` / `status` for manual control
- `net start cee-exporter` and `sc start cee-exporter` work as expected
- Event Viewer shows service start/stop events in the System log
- Automatic restart on unexpected exit (Recovery Actions)

**Standard patterns (HIGH confidence — `golang.org/x/sys/windows/svc` official docs):**

The `svc` package is already imported for `ReportEvent`. The same module provides
everything needed:

```go
// Service handler — runs inside SCM context
type ceeService struct{}

func (s *ceeService) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
    status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
    for c := range r {
        switch c.Cmd {
        case svc.Stop, svc.Shutdown:
            // trigger shutdown
            status <- svc.Status{State: svc.StopPending}
            return false, 0
        }
    }
    return false, 0
}

// In main.go: detect SCM context
isService, _ := svc.IsWindowsService()
if isService {
    svc.Run("cee-exporter", &ceeService{})
    return
}
// otherwise: run as console app
```

**Installation using `svc/mgr`:**

```go
m, _ := mgr.Connect()
defer m.Disconnect()
s, _ := m.CreateService("cee-exporter", exePath, mgr.Config{
    StartType:   mgr.StartAutomatic,
    DisplayName: "Dell CEPA Audit Event Exporter",
    Description: "Converts CEPA events to SIEM-compatible formats",
})
s.SetRecoveryActions([]mgr.RecoveryAction{
    {Type: mgr.ServiceRestart, Delay: 5 * time.Second},
}, 0)
```

**CLI subcommands:** Add `install`, `uninstall`, `start`, `stop`, `status` to the
main binary using a simple `os.Args[1]` switch. This is the pattern from the
official `golang.org/x/sys/windows/svc/example` package.

**Complexity assessment:** Medium — ~150-200 LOC. The `svc` package is already
a dependency. Main risk: Windows-only compilation. All SCM code goes in
`_windows.go` build-tag files. The `isService` detection must happen before any
`log/slog` output to avoid corrupting the console output if run interactively.

---

### Feature 4: BinaryEvtxWriter (Pure-Go BinXML on Linux)

**What this means:**
Produces standards-compliant `.evtx` files that Windows Event Viewer, Splunk,
Elastic Agent, and forensics tools (Plaso, Autopsy) can open natively, without
requiring a Windows host.

**Why it is High complexity (LOW-MEDIUM confidence — no turnkey Go writer library found):**

No existing pure-Go EVTX *writer* library was found. All Go EVTX projects
(0xrawsec/golang-evtx, Velocidex/evtx) are parsers/readers only. The implementation
must be built from scratch against the format specification.

**Format constraints — EVTX binary layout:**

```
File header    4 096 bytes
  Signature:   "ElfFile\x00"      (8 bytes)
  FirstChunk:  uint64 LE
  LastChunk:   uint64 LE
  NextRecordID: uint64 LE
  HeaderSize:  uint32 = 128
  MinorVersion: uint16 = 1
  MajorVersion: uint16 = 3
  HeaderBlockSize: uint16 = 4096
  NumberOfChunks: uint16
  FileFlags:   uint32
  Checksum:    CRC32 of first 120 bytes

Chunk (65 536 bytes each)
  Chunk header  512 bytes:
    Signature:  "ElfChnk\x00"    (8 bytes)
    FirstEventRecordNumber: uint64 LE
    LastEventRecordNumber:  uint64 LE
    FirstEventRecordID:     uint64 LE
    LastEventRecordID:      uint64 LE
    HeaderSize:  uint32 = 128
    LastEventRecordDataOffset: uint32
    FreeSpaceOffset: uint32
    EventRecordsChecksum:    CRC32
    StringTable: 64 x uint32 offsets
    TemplatePointers: 32 x uint32
    Checksum:    CRC32 of first 120 bytes

  Event records (variable length):
    Signature:       0x2a2a0000 (uint32 LE)
    Length:          uint32 LE (includes signature and trailing length copy)
    EventRecordID:   uint64 LE (globally monotonic)
    TimeCreated:     FILETIME (uint64 LE, 100ns intervals since 1601-01-01)
    BinXML data:     variable
    Length copy:     uint32 LE (same as Length, for backward scan)
```

**BinXML token vocabulary (from [MS-EVEN6], informative):**

| Byte | Token | Meaning |
|------|-------|---------|
| 0x00 | EOF | End of BinXML fragment |
| 0x01 | OpenStartElement | `<TagName` begins |
| 0x02 | CloseStartElement | `>` (ends open element, no attributes) |
| 0x03 | CloseEmptyElement | `/>` |
| 0x04 | EndElement | `</TagName>` |
| 0x05 | Value | Text value node |
| 0x06 | Attribute | Attribute in start element |
| 0x0F | PITarget | Processing instruction target |
| 0x0A | TemplateInstance | Reference to a named template |
| 0x0D | NormalSubstitution | Substitute value from substitution array |
| 0x0E | OptionalSubstitution | Substitute or omit if null |

**Minimum viable BinXML for a Security audit event (EventID 4663):**

The SystemXML skeleton that Event Viewer expects:

```xml
<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event">
  <System>
    <Provider Name="Microsoft-Windows-Security-Auditing" />
    <EventID>4663</EventID>
    <Version>0</Version>
    <Level>0</Level>
    <Task>12800</Task>
    <Opcode>0</Opcode>
    <Keywords>0x8020000000000000</Keywords>
    <TimeCreated SystemTime="..." />
    <EventRecordID>N</EventRecordID>
    <Channel>Security</Channel>
    <Computer>hostname</Computer>
  </System>
  <EventData>
    <Data Name="SubjectUserSid">S-1-5-21-...</Data>
    <Data Name="SubjectUserName">username</Data>
    <Data Name="SubjectDomainName">domain</Data>
    <Data Name="SubjectLogonId">0x3e7</Data>
    <Data Name="ObjectServer">Security</Data>
    <Data Name="ObjectType">File</Data>
    <Data Name="ObjectName">\\server\share\path</Data>
    <Data Name="HandleId">0x0</Data>
    <Data Name="AccessList">%%4416</Data>
    <Data Name="AccessMask">0x1</Data>
    <Data Name="ProcessId">0x4</Data>
    <Data Name="ProcessName">System</Data>
    <Data Name="ResourceAttributes">-</Data>
  </EventData>
</Event>
```

**Implementation strategy for v2.0:**

A full template-based BinXML system (with shared string tables and template caching
across events in a chunk) is 800-1500 LOC. A simpler correct approach for v2.0:
emit each event as a self-contained BinXML fragment with no cross-event sharing.
This is larger on disk but 100% specification-compliant and verifiable with
libevtx / 0xrawsec/golang-evtx in the test suite.

**File management:** Since the daemon runs indefinitely, the writer must rotate
`.evtx` files. A new file starts when either (a) the current file's last chunk is
full (65 536 - 512 = 65 024 bytes of event data) or (b) a configurable rotation
period elapses. Rotation is not in the EVTX spec — it is an operational concern.

**Complexity assessment:** High — 600-1200 LOC estimated. Requires deep format
knowledge. Must be tested by round-tripping written files through a parser.
Recommend implementing in its own `pkg/evtx/binxml/` subpackage.

**Reference documents:**

- [MS-EVEN6] BinXml: `https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-even6/e6fc7c72-b8c0-475b-aef7-25eaf1a64530`
- libevtx format documentation: `https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc`
- Go EVTX parser for validation: `github.com/0xrawsec/golang-evtx`

---

### Feature 5: BeatsWriter (Lumberjack v2 Protocol)

**What operators expect:**

- cee-exporter connects to a Logstash Beats Input or Graylog Beats Input
- Events arrive in Logstash as structured JSON documents
- TLS mutual authentication supported (Beats typically requires it)
- Reconnection on disconnect (same pattern as GELFWriter)
- Backpressure: Lumberjack's window-size ACK mechanism prevents overload

**Protocol framing (MEDIUM confidence — go-lumber source + protocol docs):**

Lumberjack v2 frame types (all use big-endian byte order):

| Type byte | Code | Description |
|-----------|------|-------------|
| `'2'`     | Version | Protocol version = 2 |
| `'W'`     | WindowSize | `[1B version][1B 'W'][4B uint32 window]` |
| `'J'`     | JSONDataFrame | `[1B version][1B 'J'][4B seq][4B len][JSON bytes]` |
| `'C'`     | Compressed | `[1B version][1B 'C'][4B payload_len][zlib data]` |
| `'A'`     | ACK | `[1B version][1B 'A'][4B seq]` (server → client) |

**go-lumber client API (`github.com/elastic/go-lumber/client/v2`):**

Three client types:

- `SyncClient.Send([]interface{}) (int, error)` — blocks until all ACKed; simplest
  for our use case since WriteEvent is called per-event
- `AsyncClient.Send(cb, []interface{}) error` — non-blocking with callback; better
  throughput if batching
- `Client` — raw wire, manual ACK management

**JSON payload per event** is `[]interface{}` where each element is a
`map[string]interface{}`. The field names should match ECS (Elastic Common Schema)
for native Logstash/Kibana compatibility, or use the same `_`-prefixed fields as
the GELF writer for simplicity. ECS-aligned field names:

```
@timestamp          → WindowsEvent.TimeCreated (RFC3339)
host.name           → WindowsEvent.Computer
event.id            → WindowsEvent.EventID (string)
event.category      → ["file"]
event.type          → ["access"]
event.outcome       → "success"
user.name           → WindowsEvent.SubjectUsername
user.domain         → WindowsEvent.SubjectDomain
user.id             → WindowsEvent.SubjectUserSID
file.path           → WindowsEvent.ObjectName
network.client.ip   → WindowsEvent.ClientAddr
process.pid         → WindowsEvent.ProcessID
winlog.event_id     → WindowsEvent.EventID (int)
winlog.channel      → WindowsEvent.Channel
```

**Connection configuration:**

```go
type BeatsConfig struct {
    Host             string
    Port             int    // Default 5044
    TLS              bool
    TLSCACert        string
    TLSClientCert    string
    TLSClientKey     string
    WindowSize       int    // Default 10
    CompressionLevel int    // 0-9, default 3
}
```

**TLS note:** go-lumber's `SyncDial` does not expose a TLS option directly. Use
`SyncDialWith` with a custom `tls.Dial` function. This matches the pattern already
used in `GELFWriter`.

**Reconnect pattern:** Mirror `GELFWriter.WriteEvent`: on send error, attempt one
reconnect then retry. Protect with a `sync.Mutex`.

**Complexity assessment:** Medium — 150-200 LOC. The go-lumber client is
well-documented and actively maintained. Main risk: Lumberjack v2 requires TLS
in production Logstash configurations; the writer must support it from day one
or operators cannot use it.

---

### Feature 6: SyslogWriter (RFC 5424)

**What operators expect:**

- Structured syslog messages over UDP (default) or TCP
- RFC 5424 format: PRI, VERSION, TIMESTAMP, HOSTNAME, APPNAME, PROCID, MSGID, SD, MSG
- Structured data (SD-ELEMENT) carries audit metadata without polluting the MSG field
- SIEM tools (Splunk, QRadar, ArcSight) parse SD-PARAMs natively
- Facility: LOG_SECURITY (4) or LOG_LOCAL0-7 (configurable)
- Severity: LOG_NOTICE (5) for file access events

**RFC 5424 message anatomy:**

```
<PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD-ELEMENT] MSG
<38>1 2026-03-03T14:22:01.123456Z nas01 cee-exporter 1234 AUDIT_FILE [win@0 EventID="4663" Subject="user1" Domain="CORP" ObjectName="/vol/data/file.txt" AccessMask="0x2"] CEPP_FILE_WRITE on /vol/data/file.txt
```

**PRI calculation:** `(Facility * 8) + Severity`. Security audit events:

- Facility: `LOG_SECURITY` = 4 → use 13 (LOG_SECURITY in POSIX) or `LOG_LOCAL6` = 22
  (configurable; LOG_SECURITY not universally supported on Linux)
- Severity: `LOG_NOTICE` = 5 for informational audit events
- Default PRI: `(13 * 8) + 5 = 109` → `<109>`

**SD-ELEMENT field mapping from `WindowsEvent`:**

```
SD-ID: win@0 (private enterprise number format, IANA-registered = "timeQuality@0",
               for private use prefix with enterprise ID or use "win@12345")

SD-PARAMs:
  EventID     = strconv.Itoa(e.EventID)        // "4663"
  Channel     = e.Channel                       // "Security"
  Provider    = e.ProviderName
  Subject     = e.SubjectUsername
  Domain      = e.SubjectDomain
  SID         = e.SubjectUserSID
  LogonID     = e.SubjectLogonID
  ObjectType  = e.ObjectType                   // "File"
  ObjectName  = e.ObjectName                   // requires escaping ] \ "
  AccessMask  = e.AccessMask                   // "0x2"
  Accesses    = e.Accesses                     // "WriteData (or AddFile)"
  ClientAddr  = e.ClientAddr
  CEPAType    = e.CEPAEventType
```

**RFC 5424 escaping rules for SD-PARAM values:**
Values must escape: `"` → `\"`, `\` → `\\`, `]` → `\]`. File paths with backslashes
(UNC paths from Windows) and brackets (rare) require careful escaping. The Go
standard library has no RFC 5424 encoder — implement a simple escaper or use
`github.com/juju/rfc/rfc5424` or `github.com/nathanaelle/syslog5424`.

**MSG field:** Human-readable summary: `"${CEPAEventType} on ${ObjectName}"` —
same as the GELF `short_message`. Truncate at 1024 bytes.

**Transport:**

```go
type SyslogConfig struct {
    Host     string   // "" → /dev/log on Linux
    Port     int      // Default 514 UDP, 601 TCP
    Protocol string   // "udp", "tcp", "unixgram"
    TLS      bool     // RFC 5425 — syslog over TLS
    Facility int      // Default 13 (LOG_SECURITY)
}
```

Go stdlib `log/syslog` supports UDP and Unix socket but not RFC 5424 structured
data natively. Use a third-party package or implement the formatter. Recommended:
implement formatter internally (80-100 LOC) and use `net.Dial` for transport
(matches pattern of GELFWriter), avoiding an additional dependency.

**Complexity assessment:** Medium — 200-250 LOC. Field mapping is clear. Main risk
is correct RFC 5424 escaping and proper PRI/facility selection for different SIEM
targets. The Go stdlib `log/syslog` package does not produce RFC 5424 structured
data — do not use it for this writer.

---

## Feature Dependencies

```
pkg/metrics (existing atomics)
    └── Prometheus /metrics endpoint (wraps atomics with client_golang)

pkg/evtx.Writer interface (existing)
    ├── BeatsWriter     (new — implements Writer, uses go-lumber client)
    ├── SyslogWriter    (new — implements Writer, uses net.Dial)
    └── BinaryEvtxWriter (new — implements Writer, writes .evtx file)

pkg/server.Handler (existing HTTP mux)
    └── /metrics route  (mounted alongside /health and CEPA PUT)

cmd/cee-exporter/main.go
    ├── isWindowsService() → svc.Run()  (Windows build tag)
    └── sd_notify READY   (Linux, optional)
```

No dependency between new writers. Systemd unit and Windows Service are purely
operational — they do not depend on any new code unless `sd_notify` or
`isWindowsService` detection is added.

---

## MVP Recommendation

Prioritize in this order for maximal ops impact with least risk:

1. **Prometheus `/metrics`** — Low risk, high ops value, 60-80 LOC. Counters already
   exist; just expose them.
2. **Systemd unit file** — Zero code risk (text artifact). Unlocks Linux managed
   deployment immediately.
3. **Windows Service** — Medium risk; `svc` module already imported. Enables Windows
   production deployment.
4. **SyslogWriter** — Medium risk; broadest SIEM compatibility.
5. **BeatsWriter** — Medium risk; adds `go-lumber` dependency; requires Logstash/
   Graylog Beats Input in the environment to test.
6. **BinaryEvtxWriter** — High risk, high effort; defer to own phase with dedicated
   format research.

**Defer within v2.0:** BinaryEvtxWriter should be its own milestone phase. All other
five features can ship together.

---

## Sources

- Prometheus Go client docs: [pkg.go.dev/github.com/prometheus/client_golang/prometheus](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus)
- Prometheus instrumentation guide: [prometheus.io/docs/guides/go-application/](https://prometheus.io/docs/guides/go-application/)
- systemd unit file best practices: [ctrl.blog/entry/systemd-service-hardening](https://www.ctrl.blog/entry/systemd-service-hardening.html)
- Go + systemd integration: [vincent.bernat.ch/en/blog/2017-systemd-golang](https://vincent.bernat.ch/en/blog/2017-systemd-golang)
- Windows svc package: [pkg.go.dev/golang.org/x/sys/windows/svc](https://pkg.go.dev/golang.org/x/sys/windows/svc)
- Windows svc/mgr package: [pkg.go.dev/golang.org/x/sys/windows/svc/mgr](https://pkg.go.dev/golang.org/x/sys/windows/svc/mgr)
- BinXML MS-EVEN6 spec: [learn.microsoft.com/en-us/openspecs/windows_protocols/ms-even6/e6fc7c72-b8c0-475b-aef7-25eaf1a64530](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-even6/e6fc7c72-b8c0-475b-aef7-25eaf1a64530)
- EVTX format documentation: [github.com/libyal/libevtx/blob/main/documentation](https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc)
- go-lumber client v2: [pkg.go.dev/github.com/elastic/go-lumber/client/v2](https://pkg.go.dev/github.com/elastic/go-lumber/client/v2)
- go-lumber GitHub: [github.com/elastic/go-lumber](https://github.com/elastic/go-lumber)
- RFC 5424 syslog: [rfc-editor.org/rfc/rfc5424](https://www.rfc-editor.org/rfc/rfc5424)
- RFC 5424 structured data overview: [logcentral.io/en/blog/structured-data-syslog-rfc-5424-overview](https://logcentral.io/en/blog/structured-data-syslog-rfc-5424-overview)
- Go syslog RFC 5424 package: [pkg.go.dev/github.com/juju/rfc/rfc5424](https://pkg.go.dev/github.com/juju/rfc/rfc5424)
