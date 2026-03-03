# Phase 6: SIEM Writers — Research

**Researched:** 2026-03-03
**Domain:** Go syslog RFC 5424 writer + Lumberjack v2 (Beats) client, CGO_ENABLED=0, cross-platform
**Confidence:** HIGH

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| OUT-01 | Operator can configure BeatsWriter to forward events to Logstash or Graylog Beats Input via Lumberjack v2 protocol | `github.com/elastic/go-lumber/client/v2` SyncClient.Send, reconnect pattern from GELFWriter |
| OUT-02 | BeatsWriter supports TLS for encrypted Beats transport | `SyncDialWith` + `tls.Dial` custom dialer; no built-in TLS Option in go-lumber — inject via dialer |
| OUT-03 | Operator can configure SyslogWriter to forward RFC 5424 structured syslog events over UDP | `github.com/crewjam/rfc5424` Message.WriteTo(udpConn); UDP: plain write, no framing |
| OUT-04 | Operator can configure SyslogWriter to forward RFC 5424 structured syslog events over TCP | RFC 6587 octet-counting framing: `fmt.Fprintf(conn, "%d ", len(msg)); conn.Write(msg)` |
</phase_requirements>

---

## Summary

Phase 6 adds two new writer backends to `pkg/evtx`: a **SyslogWriter** that emits RFC 5424 messages over UDP or TCP, and a **BeatsWriter** that publishes Lumberjack v2 frames to Logstash or Graylog Beats Input (with optional TLS). Both writers implement the existing `evtx.Writer` interface (`WriteEvent(ctx, WindowsEvent) error` / `Close() error`) and follow the pattern established by `GELFWriter`: lazy reconnect on send failure under a `sync.Mutex`.

The key technical decision already recorded in STATE.md is confirmed: use `github.com/crewjam/rfc5424` for syslog message construction (bypasses the `log/syslog` stdlib package which is excluded from Windows builds and not RFC 5424 compliant), and use `github.com/elastic/go-lumber/client/v2` for Beats/Lumberjack v2. TLS for BeatsWriter is achieved by injecting a `tls.Dial`-compatible function via `SyncDialWith` — go-lumber has no built-in TLS Option; it exposes a `dial func(network, address string) (net.Conn, error)` parameter instead. RFC 6587 octet-counting (`"<len> <msg>"`) is the required TCP syslog framing — the crewjam library produces raw bytes via `MarshalBinary()`, which the writer prepends with the length.

Neither library uses CGO — both are pure Go, satisfying the project's `CGO_ENABLED=0` constraint. Neither is present in go.sum yet; both need `go get`. The config struct in `main.go` needs new fields (`SyslogHost`, `SyslogPort`, `SyslogProtocol`, `BeatsHost`, `BeatsPort`, `BeatsTLS`), and the `buildWriter` switch must handle `"syslog"` and `"beats"` type tokens.

**Primary recommendation:** Implement SyslogWriter first (simpler — pure stdlib net + crewjam), then BeatsWriter (requires go-lumber SyncClient reconnect + TLS dialer injection), each with its own config struct, writer file, and test file. Add both cases to the `buildWriter` factory in `main.go`.

---

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/crewjam/rfc5424` | v0.1.0 (May 2020) | Build RFC 5424 syslog messages with structured data | Only maintained pure-Go RFC 5424 serializer; zero CGO; `WriteTo(io.Writer)` works with any `net.Conn` |
| `github.com/elastic/go-lumber/client/v2` | v0.1.1 (Jan 2021) | Lumberjack v2 SyncClient — sends ACK-ed batches to Logstash/Graylog Beats Input | Official Elastic implementation; no CGO; used by Filebeat/Winlogbeat itself |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `crypto/tls` (stdlib) | Go 1.24 | TLS dialer for BeatsWriter | Always — injected into `SyncDialWith` via custom dial function |
| `net` (stdlib) | Go 1.24 | UDP/TCP connections for SyslogWriter | Always — `net.DialTimeout("udp", ...)`, `net.DialTimeout("tcp", ...)` |
| `fmt` (stdlib) | Go 1.24 | RFC 6587 octet-counting prefix for TCP | Always — `fmt.Fprintf(conn, "%d ", n)` before writing message bytes |
| `sync` (stdlib) | Go 1.24 | Mutex protecting conn in both writers | Always — matches GELFWriter pattern |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `github.com/crewjam/rfc5424` | `log/syslog` (stdlib) | stdlib excluded from Windows builds (`//go:build !windows && !plan9`); not RFC 5424 compliant (missing VERSION field); REJECTED |
| `github.com/crewjam/rfc5424` | `github.com/nathanaelle/syslog5424/v2` | More complex API, actively maintained but heavier; crewjam is simpler for write-only use case |
| `github.com/elastic/go-lumber/client/v2` | Custom TCP framing | Lumberjack v2 protocol is non-trivial (window ACK, compression, framing); go-lumber is the reference implementation; DO NOT hand-roll |

**Installation:**
```bash
go get github.com/crewjam/rfc5424@v0.1.0
go get github.com/elastic/go-lumber@v0.1.1
```

---

## Architecture Patterns

### Recommended Project Structure
```
pkg/evtx/
├── writer_syslog.go          # SyslogWriter struct + NewSyslogWriter + WriteEvent + Close
├── writer_syslog_test.go     # Table-driven tests: buildSyslog5424(), TCP framing, UDP send
├── writer_beats.go           # BeatsWriter struct + NewBeatsWriter + WriteEvent + Close
└── writer_beats_test.go      # Table-driven tests: buildBeatsEvent(), TLS dialer injection

cmd/cee-exporter/
└── main.go                   # Add SyslogConfig + BeatsConfig to OutputConfig; extend buildWriter()
```

### Pattern 1: Writer Struct with Mutex-Protected Reconnect

This is the established pattern from `GELFWriter`. Both new writers must follow it exactly.

**What:** Writer holds a `net.Conn` (or `*SyncClient`) protected by `sync.Mutex`. On send failure, attempt one reconnect before returning error.
**When to use:** Any writer over a stateful network connection — TCP syslog, Beats.
**Note:** UDP syslog uses `net.Conn` but reconnect is a no-op (UDP is connectionless); still guard with mutex for goroutine safety.

```go
// Source: pkg/evtx/writer_gelf.go (established project pattern)
type SyslogWriter struct {
    cfg  SyslogConfig
    mu   sync.Mutex
    conn net.Conn
}

func (w *SyslogWriter) WriteEvent(ctx context.Context, e WindowsEvent) error {
    payload, err := buildSyslog5424(e, w.cfg.AppName)
    if err != nil {
        return fmt.Errorf("syslog build: %w", err)
    }
    w.mu.Lock()
    defer w.mu.Unlock()
    if err := w.send(payload); err != nil {
        slog.Warn("syslog_reconnect", "reason", err)
        if rerr := w.connect(); rerr != nil {
            return fmt.Errorf("syslog send+reconnect: %w / %w", err, rerr)
        }
        if err2 := w.send(payload); err2 != nil {
            return fmt.Errorf("syslog send after reconnect: %w", err2)
        }
    }
    return nil
}
```

### Pattern 2: RFC 5424 Message Construction with crewjam/rfc5424

**What:** Build a `rfc5424.Message` with priority, timestamp, hostname, app-name, and structured-data SD-ELEMENT containing audit fields.
**When to use:** Every syslog event write.

```go
// Source: pkg.go.dev/github.com/crewjam/rfc5424
import "github.com/crewjam/rfc5424"

func buildSyslog5424(e evtx.WindowsEvent, appName string) ([]byte, error) {
    m := rfc5424.Message{
        Priority:  rfc5424.Daemon | rfc5424.Info,  // facility=1, severity=6
        Timestamp: e.TimeCreated,
        Hostname:  e.Computer,
        AppName:   appName,                         // e.g. "cee-exporter"
        ProcessID: fmt.Sprintf("%d", e.ProcessID),
        MessageID: fmt.Sprintf("%d", e.EventID),
        Message:   []byte(fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName)),
    }
    // SD-ID uses private enterprise number format: "name@PEN"
    // Use IANA example PEN 32473 in tests; operators can configure their PEN.
    sdID := "audit@32473"
    m.AddDatum(sdID, "EventID",    fmt.Sprintf("%d", e.EventID))
    m.AddDatum(sdID, "User",       e.SubjectUsername)
    m.AddDatum(sdID, "Domain",     e.SubjectDomain)
    m.AddDatum(sdID, "Object",     e.ObjectName)
    m.AddDatum(sdID, "AccessMask", e.AccessMask)
    m.AddDatum(sdID, "ClientAddr", e.ClientAddr)
    m.AddDatum(sdID, "CEPAType",   e.CEPAEventType)
    return m.MarshalBinary()
}
```

**Note:** `AddDatum(sdID, name, value)` — the same SD-ID string must be used for all params in one SD-ELEMENT. The `sdID` format for private use is `name@<private-enterprise-number>`. RFC 5424 reserves IANA-registered IDs (no `@`) — do not use bare names. IANA PEN 32473 is designated for documentation/examples per RFC 5612.

### Pattern 3: RFC 6587 TCP Syslog Framing (Octet-Counting)

**What:** TCP syslog requires each message to be prefixed with its byte length and a space, per RFC 6587 section 3.4.1. UDP requires no framing.
**When to use:** `syslog_protocol = "tcp"` only.

```go
// Source: RFC 6587 §3.4.1 octet-counting method
func (w *SyslogWriter) send(payload []byte) error {
    switch w.cfg.Protocol {
    case "tcp":
        // Octet-counting: "<MSG-LEN> <SYSLOG-MSG>"
        if _, err := fmt.Fprintf(w.conn, "%d ", len(payload)); err != nil {
            return err
        }
        _, err := w.conn.Write(payload)
        return err
    default: // udp — single datagram, no framing
        _, err := w.conn.Write(payload)
        return err
    }
}
```

### Pattern 4: BeatsWriter with TLS via SyncDialWith Custom Dialer

**What:** go-lumber's `SyncDial` uses plain TCP. For TLS, use `SyncDialWith` and supply a `tls.DialWithDialer`-based custom function.
**When to use:** `beats_tls = true`.

```go
// Source: pkg.go.dev/github.com/elastic/go-lumber/client/v2 + crypto/tls (stdlib)
import (
    lumberv2 "github.com/elastic/go-lumber/client/v2"
    "crypto/tls"
    "net"
    "time"
)

func dialBeats(cfg BeatsConfig) (*lumberv2.SyncClient, error) {
    addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
    if cfg.TLS {
        dialer := &tls.Dialer{
            NetDialer: &net.Dialer{Timeout: 5 * time.Second},
            Config:    &tls.Config{MinVersion: tls.VersionTLS12},
        }
        return lumberv2.SyncDialWith(
            func(network, address string) (net.Conn, error) {
                return dialer.DialContext(context.Background(), network, address)
            },
            addr,
            lumberv2.Timeout(30*time.Second),
        )
    }
    return lumberv2.SyncDial(addr, lumberv2.Timeout(30*time.Second))
}
```

### Pattern 5: BeatsWriter Event Payload

**What:** `SyncClient.Send([]interface{})` accepts a slice of JSON-serialisable values. Each element is JSON-encoded by the library using the configured encoder (default: `json.Marshal`). Graylog/Logstash Beats Input expects `map[string]interface{}` with a `"message"` field and custom fields.
**When to use:** Every `WriteEvent` call on BeatsWriter.

```go
func buildBeatsEvent(e evtx.WindowsEvent) map[string]interface{} {
    return map[string]interface{}{
        "@timestamp":      e.TimeCreated.UTC().Format(time.RFC3339Nano),
        "message":         fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName),
        "event_id":        e.EventID,
        "provider":        e.ProviderName,
        "computer":        e.Computer,
        "user":            e.SubjectUsername,
        "domain":          e.SubjectDomain,
        "user_sid":        e.SubjectUserSID,
        "logon_id":        e.SubjectLogonID,
        "object_name":     e.ObjectName,
        "object_type":     e.ObjectType,
        "access_mask":     e.AccessMask,
        "accesses":        e.Accesses,
        "client_address":  e.ClientAddr,
        "cepa_event_type": e.CEPAEventType,
    }
}
```

### Pattern 6: Config Extension in main.go

**What:** `OutputConfig` needs new fields for both writers; `buildWriter` switch needs two new cases.
**When to use:** Wiring new writers into the existing factory.

```go
// Additions to OutputConfig in main.go
type OutputConfig struct {
    // ... existing fields ...
    // Syslog
    SyslogHost     string `toml:"syslog_host"`
    SyslogPort     int    `toml:"syslog_port"`     // default 514
    SyslogProtocol string `toml:"syslog_protocol"` // "udp" | "tcp"
    SyslogAppName  string `toml:"syslog_app_name"` // default "cee-exporter"
    // Beats
    BeatsHost string `toml:"beats_host"`
    BeatsPort int    `toml:"beats_port"` // default 5044
    BeatsTLS  bool   `toml:"beats_tls"`
}

// In buildWriter switch:
case "syslog":
    w, err := evtx.NewSyslogWriter(evtx.SyslogConfig{...})
    addr := net.JoinHostPort(cfg.SyslogHost, fmt.Sprintf("%d", cfg.SyslogPort))
    return w, addr, err
case "beats":
    w, err := evtx.NewBeatsWriter(evtx.BeatsConfig{...})
    addr := net.JoinHostPort(cfg.BeatsHost, fmt.Sprintf("%d", cfg.BeatsPort))
    return w, addr, err
```

### Anti-Patterns to Avoid

- **Using `log/syslog` stdlib:** It has `//go:build !windows && !plan9` — the project builds for Windows, so this breaks cross-compilation. It also generates non-RFC-5424-compliant output (missing VERSION field).
- **Hand-rolling Lumberjack v2 framing:** The protocol has window-size ACK semantics, compression (gzip), and a custom binary framing. go-lumber handles all of this. Do not re-implement.
- **Non-transparent framing for TCP syslog:** Append-newline/NUL framing (RFC 6587 §3.4.2) has documented interoperability problems. Use octet-counting exclusively.
- **Using `SyncClient` without mutex protection:** `SyncClient` is documented as "not thread-safe". The BeatsWriter must serialize calls with `sync.Mutex`, mirroring GELFWriter.
- **Long connection timeout in `connect()`:** Use 5 seconds for initial dial (matches GELFWriter). A 30-second timeout for `lumberv2.Timeout` option controls read/write, not dial.
- **Storing `*SyncClient` without reconnect logic:** Unlike GELFWriter's raw `net.Conn`, go-lumber's `SyncClient` cannot be reused after a send error — close and recreate via `dialBeats()`.
- **Bare SD-ID in syslog without `@PEN`:** Bare names (no `@`) are IANA-reserved. Always use `name@<PEN>` format for private structured data.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Lumberjack v2 wire protocol | Custom binary framing with window ACK | `github.com/elastic/go-lumber/client/v2` | Protocol has gzip compression, window-size negotiation, sequence numbers, ACK tracking — reference implementation is 1000+ LOC |
| RFC 5424 message serialization | Custom string formatter | `github.com/crewjam/rfc5424` | SD-PARAM values require escaping (`"`, `\`, `]`); Priority encoding; timestamp RFC 3339 with micro precision; NILVALUE handling — spec compliance is subtle |
| TLS for Beats | Custom TLS wrapping on SyncClient internals | `SyncDialWith` + `tls.Dialer` | go-lumber accepts a `dial func` parameter exactly for this; inject `tls.Dialer` as shown |
| RFC 6587 TCP framing | Custom length-prefix logic | 2-line `fmt.Fprintf` + `conn.Write` | This one IS trivially hand-rolled (2 lines); no library needed |

**Key insight:** The Lumberjack v2 ACK mechanism is the trap — sending without waiting for ACK risks data loss on connection failure. `SyncClient.Send` blocks until ACK received, which is exactly correct for a reliable audit log writer.

---

## Common Pitfalls

### Pitfall 1: SyncClient is Not Thread-Safe
**What goes wrong:** Multiple goroutines call `BeatsWriter.WriteEvent` concurrently; `SyncClient.Send` corrupts state.
**Why it happens:** go-lumber documents `SyncClient` as not thread-safe; the queue sends events from multiple worker goroutines.
**How to avoid:** Wrap every `SyncClient.Send` call with `sync.Mutex` (same as GELFWriter wraps `conn.Write`).
**Warning signs:** Intermittent `ErrProtocolError` or panics on concurrent writes.

### Pitfall 2: SyncClient Cannot Be Reused After Error
**What goes wrong:** After a `SyncClient.Send` error, subsequent calls also fail even after the remote recovers.
**Why it happens:** go-lumber's `SyncClient` does not internally reconnect. Once the underlying `net.Conn` is broken, the client is dead.
**How to avoid:** On `Send` error: call `w.client.Close()`, call `w.dialBeats()` to get a new `*SyncClient`, then retry send once.
**Warning signs:** Permanent failure after any transient network blip.

### Pitfall 3: RFC 6587 Framing Only for TCP Syslog
**What goes wrong:** Applying octet-counting prefix to UDP datagrams; or omitting it for TCP.
**Why it happens:** The framing requirement is protocol-specific and easy to conflate.
**How to avoid:** Check `cfg.Protocol` in `send()` — use framing only for `"tcp"`.
**Warning signs:** rsyslog/syslog-ng rejects TCP frames or UDP receiver sees malformed messages.

### Pitfall 4: SD-PARAM Value Characters Must Be Escaped
**What goes wrong:** File paths with `]`, `"`, or `\` characters break structured-data parsing in receivers.
**Why it happens:** RFC 5424 §6.3.3 requires escaping `]`, `"`, and `\` in SD-PARAM values.
**How to avoid:** `crewjam/rfc5424` handles this automatically in `AddDatum`. Do not format SD-params manually.
**Warning signs:** Syslog receiver truncates or rejects messages with Windows-style file paths (backslashes).

### Pitfall 5: log/syslog Windows Build Failure
**What goes wrong:** `import "log/syslog"` causes build failure for `GOOS=windows`.
**Why it happens:** stdlib syslog has `//go:build !windows && !plan9`.
**How to avoid:** Do not use `log/syslog` — use `crewjam/rfc5424` + `net.Conn` directly (no build tags needed).
**Warning signs:** `go build -o cee-exporter.exe` fails with "undefined: syslog".

### Pitfall 6: go-lumber Compression vs. Receiver Compatibility
**What goes wrong:** `lumberv2.CompressionLevel(6)` enabled but receiver rejects compressed frames.
**Why it happens:** Not all Beats receivers support gzip compression; Graylog Beats Input typically does but older versions may not.
**How to avoid:** Default to `CompressionLevel(0)` (disabled). Document as optional config. Graylog 5+ supports it; verify with receiver.
**Warning signs:** Receiver logs protocol errors immediately after TLS handshake succeeds.

### Pitfall 7: Syslog Port Default
**What goes wrong:** Default port 514 requires root on Linux for binding; clients sending to 514 work fine.
**Why it happens:** Ports below 1024 need CAP_NET_BIND_SERVICE or root for the *server*. Client connecting to port 514 is always fine.
**How to avoid:** Default `syslog_port = 514` for client config is correct. Receivers run as root or on 514. No issue for cee-exporter (it is the client).

---

## Code Examples

Verified patterns from official sources:

### SyslogWriter Constructor
```go
// Source: established GELFWriter pattern (pkg/evtx/writer_gelf.go) + crewjam/rfc5424 API
type SyslogConfig struct {
    Host     string // syslog receiver host
    Port     int    // default 514
    Protocol string // "udp" | "tcp"
    AppName  string // default "cee-exporter"
}

type SyslogWriter struct {
    cfg  SyslogConfig
    mu   sync.Mutex
    conn net.Conn
}

func NewSyslogWriter(cfg SyslogConfig) (*SyslogWriter, error) {
    if cfg.Port == 0 {
        cfg.Port = 514
    }
    if cfg.Protocol == "" {
        cfg.Protocol = "udp"
    }
    if cfg.AppName == "" {
        cfg.AppName = "cee-exporter"
    }
    w := &SyslogWriter{cfg: cfg}
    if err := w.connect(); err != nil {
        return nil, err
    }
    slog.Info("syslog_writer_ready",
        "host", cfg.Host,
        "port", cfg.Port,
        "protocol", cfg.Protocol,
    )
    return w, nil
}

func (w *SyslogWriter) connect() error {
    addr := net.JoinHostPort(w.cfg.Host, fmt.Sprintf("%d", w.cfg.Port))
    conn, err := net.DialTimeout(w.cfg.Protocol, addr, 5*time.Second)
    if err != nil {
        return fmt.Errorf("syslog connect %s://%s: %w", w.cfg.Protocol, addr, err)
    }
    w.conn = conn
    return nil
}
```

### RFC 5424 Message Build
```go
// Source: pkg.go.dev/github.com/crewjam/rfc5424 (verified API)
import "github.com/crewjam/rfc5424"

func buildSyslog5424(e WindowsEvent, appName string) ([]byte, error) {
    m := rfc5424.Message{
        Priority:  rfc5424.Daemon | rfc5424.Info,
        Timestamp: e.TimeCreated,
        Hostname:  e.Computer,
        AppName:   appName,
        ProcessID: fmt.Sprintf("%d", e.ProcessID),
        MessageID: fmt.Sprintf("%d", e.EventID),
        Message:   []byte(fmt.Sprintf("%s on %s", e.CEPAEventType, e.ObjectName)),
    }
    sdID := "audit@32473"
    m.AddDatum(sdID, "EventID",    fmt.Sprintf("%d", e.EventID))
    m.AddDatum(sdID, "User",       e.SubjectUsername)
    m.AddDatum(sdID, "Domain",     e.SubjectDomain)
    m.AddDatum(sdID, "Object",     e.ObjectName)
    m.AddDatum(sdID, "AccessMask", e.AccessMask)
    m.AddDatum(sdID, "ClientAddr", e.ClientAddr)
    m.AddDatum(sdID, "CEPAType",   e.CEPAEventType)
    return m.MarshalBinary()
}
```

### BeatsWriter Constructor with TLS
```go
// Source: pkg.go.dev/github.com/elastic/go-lumber/client/v2 (verified API)
import lumberv2 "github.com/elastic/go-lumber/client/v2"

type BeatsConfig struct {
    Host string
    Port int  // default 5044
    TLS  bool
}

type BeatsWriter struct {
    cfg    BeatsConfig
    mu     sync.Mutex
    client *lumberv2.SyncClient
}

func NewBeatsWriter(cfg BeatsConfig) (*BeatsWriter, error) {
    if cfg.Port == 0 {
        cfg.Port = 5044
    }
    w := &BeatsWriter{cfg: cfg}
    if err := w.dial(); err != nil {
        return nil, err
    }
    slog.Info("beats_writer_ready",
        "host", cfg.Host,
        "port", cfg.Port,
        "tls", cfg.TLS,
    )
    return w, nil
}

func (w *BeatsWriter) dial() error {
    addr := net.JoinHostPort(w.cfg.Host, fmt.Sprintf("%d", w.cfg.Port))
    var (
        cl  *lumberv2.SyncClient
        err error
    )
    if w.cfg.TLS {
        tlsDialer := &tls.Dialer{
            NetDialer: &net.Dialer{Timeout: 5 * time.Second},
            Config:    &tls.Config{MinVersion: tls.VersionTLS12},
        }
        cl, err = lumberv2.SyncDialWith(
            func(network, address string) (net.Conn, error) {
                return tlsDialer.DialContext(context.Background(), network, address)
            },
            addr,
            lumberv2.Timeout(30*time.Second),
        )
    } else {
        cl, err = lumberv2.SyncDial(addr, lumberv2.Timeout(30*time.Second))
    }
    if err != nil {
        return fmt.Errorf("beats dial %s: %w", addr, err)
    }
    w.client = cl
    return nil
}

func (w *BeatsWriter) WriteEvent(ctx context.Context, e WindowsEvent) error {
    event := buildBeatsEvent(e)
    w.mu.Lock()
    defer w.mu.Unlock()
    _, err := w.client.Send([]interface{}{event})
    if err != nil {
        slog.Warn("beats_reconnect", "reason", err)
        _ = w.client.Close()
        if rerr := w.dial(); rerr != nil {
            return fmt.Errorf("beats send+reconnect: %w / %w", err, rerr)
        }
        if _, err2 := w.client.Send([]interface{}{event}); err2 != nil {
            return fmt.Errorf("beats send after reconnect: %w", err2)
        }
    }
    slog.Debug("beats_event_sent", "event_id", e.EventID, "cepa_event_type", e.CEPAEventType)
    return nil
}

func (w *BeatsWriter) Close() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    if w.client != nil {
        return w.client.Close()
    }
    return nil
}
```

### Test Pattern for SyslogWriter (Stdlib Only)
```go
// Source: CLAUDE.md testing conventions + established project tests
package evtx

import (
    "testing"
    "time"
)

func TestBuildSyslog5424(t *testing.T) {
    e := WindowsEvent{
        EventID:         4663,
        ProviderName:    "PowerStore-CEPA",
        Computer:        "nas01.corp.local",
        TimeCreated:     time.Unix(1700000000, 0),
        SubjectUsername: "testuser",
        SubjectDomain:   "DOMAIN",
        ObjectName:      "/share/file.txt",
        AccessMask:      "0x2",
        ClientAddr:      "10.0.0.5",
        CEPAEventType:   "CEPP_FILE_WRITE",
    }
    payload, err := buildSyslog5424(e, "cee-exporter")
    if err != nil {
        t.Fatalf("buildSyslog5424 returned error: %v", err)
    }
    s := string(payload)
    // RFC 5424 header: <PRI>VERSION SP TIMESTAMP SP HOSTNAME SP APPNAME SP PROCID SP MSGID
    if s[:3] != "<" {
        t.Errorf("expected RFC 5424 PRI start '<', got: %.20s", s)
    }
    // Must contain structured-data block
    for _, field := range []string{"audit@32473", "EventID", "User", "Object"} {
        if !strings.Contains(s, field) {
            t.Errorf("missing field %q in syslog payload: %s", field, s)
        }
    }
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `log/syslog` stdlib | `crewjam/rfc5424` + `net.Conn` | Go stdlib syslog frozen (pre-1.21) | Correct RFC 5424; cross-platform (Windows builds) |
| Logstash-Forwarder (deprecated) | Lumberjack v2 (`go-lumber`) | ~2015 with Beats 1.0 | Beats-compatible; ACK semantics prevent data loss |
| Non-transparent framing (newline) for TCP syslog | Octet-counting (RFC 6587 §3.4.1) | RFC 6587 published 2012 | Reliable framing; no ambiguity with embedded newlines |
| Custom Lumberjack client | `github.com/elastic/go-lumber` | 2016 (Elastic official) | Reference implementation; battle-tested in production |

**Deprecated/outdated:**
- `log/syslog`: Frozen, not RFC 5424 compliant, Windows-excluded — do not use.
- Lumberjack v1: Superseded by v2 (different wire format); `go-lumber/client/v2` implements v2.
- RFC 3164 BSD syslog: Explicitly out of scope per REQUIREMENTS.md.

---

## Open Questions

1. **SD-ID Private Enterprise Number (PEN)**
   - What we know: SD-IDs without `@PEN` are IANA-reserved; `name@32473` is the IANA documentation PEN (safe for examples/testing).
   - What's unclear: Whether to use `32473` (doc PEN) in production or leave configurable. Using `32473` in production is technically a protocol violation (reserved for documentation).
   - Recommendation: Use `audit@32473` for the initial implementation (functionally identical to any other PEN for receivers that don't validate). Add `syslog_sd_id` config field as a follow-up if operators need a registered PEN. This is LOW priority.

2. **go-lumber Compression Default**
   - What we know: `CompressionLevel(0)` = disabled; `CompressionLevel(6)` = gzip. Graylog 5+ supports it; Logstash supports it.
   - What's unclear: Whether Graylog Beats Input in all common versions accepts compressed frames.
   - Recommendation: Default `CompressionLevel(0)` (safe). Add `beats_compression_level` config option in the config struct but default to 0.

3. **BeatsWriter ACK Timeout Under Load**
   - What we know: `lumberv2.Timeout(30*time.Second)` sets read/write deadline. Under high event rates, batching a single event per `Send` call may be slow.
   - What's unclear: Whether per-event `Send` is adequate for the expected throughput (~1000 events/PUT).
   - Recommendation: Start with per-event `Send` (simple, correct). The queue's `Workers=4` means max 4 concurrent sends (serialised to 1 by mutex). REQUIREMENTS.md defers async batching to `OUT-F02`. Do not optimize prematurely.

---

## Sources

### Primary (HIGH confidence)
- `pkg.go.dev/github.com/crewjam/rfc5424` — Full API: Message, Priority, StructuredData, SDParam, AddDatum, MarshalBinary, WriteTo verified
- `pkg.go.dev/github.com/elastic/go-lumber/client/v2` — Full API: SyncClient, SyncDial, SyncDialWith, SyncClient.Send, Client.Close verified
- `github.com/elastic/go-lumber/blob/main/client/v2/sync.go` — SyncDialWith signature verified from source
- `github.com/elastic/go-lumber/blob/main/client/v2/opts.go` — No TLS Option in opts (Timeout, CompressionLevel, JSONEncoder only) — TLS via dialer confirmed
- `github.com/elastic/go-lumber/blob/main/lj/lj.go` — Batch.Events is `[]interface{}` confirmed
- `github.com/elastic/go-lumber/blob/main/go.mod` — No CGO dependency confirmed
- `www.rfc-editor.org/rfc/rfc6587` — Octet-counting format `MSG-LEN SP SYSLOG-MSG` verified
- `pkg/evtx/writer_gelf.go` — Project writer pattern (mutex, reconnect, connect/send split) read directly

### Secondary (MEDIUM confidence)
- `go.dev/src/log/syslog/syslog.go` (via WebSearch) — `//go:build !windows && !plan9` confirmed; stdlib syslog Windows exclusion verified
- `github.com/golang/go/issues/66666` — log/syslog non-RFC-5424-compliance confirmed (frozen package)
- `go2docs.graylog.org/current/setting_up_graylog/secured_graylog_and_beats_input.html` — Graylog Beats Input TLS configuration; port 5044 confirmed
- `elastic.co/guide/en/logstash/current/ls-to-ls-lumberjack.html` — Logstash Beats/Lumberjack event field format verified
- `www.rfc-editor.org/rfc/rfc5424` — SD-ID private enterprise number format `name@PEN` confirmed

### Tertiary (LOW confidence)
- PEN 32473 as documentation/example PEN: referenced from RFC 5612 conventions; functionally safe but technically reserved for examples. Flag for validation if strict RFC compliance required.

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — both libraries verified via pkg.go.dev and source inspection; no CGO confirmed
- Architecture: HIGH — writer pattern copied from existing GELFWriter; library APIs verified
- Pitfalls: HIGH (thread-safety, reconnect, TCP framing) / MEDIUM (SD-ID PEN, compression compatibility)
- TLS approach: HIGH — go-lumber has no TLS Option; `SyncDialWith` + custom dialer is the only way; confirmed from source

**Research date:** 2026-03-03
**Valid until:** 2026-06-03 (stable libraries — crewjam/rfc5424 v0.1.0 released 2020; go-lumber v0.1.1 released 2021; neither has had releases since)
