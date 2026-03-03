# Domain Pitfalls

**Domain:** Go daemon — v2 feature additions (ops + output writers)
**Project:** cee-exporter
**Researched:** 2026-03-03
**Overall Confidence:** HIGH (architecture-specific pitfalls from code inspection + MEDIUM for external library specifics)

---

## Critical Pitfalls

Mistakes that cause rewrites, corrupt data, or silently break the deployment contract.

---

### Pitfall 1: Prometheus /metrics — HTTP port and mux collision

**What goes wrong:**
The CEPA listener owns `0.0.0.0:12228` and uses a dedicated `*http.ServeMux`.
If `promhttp.Handler()` is registered on `http.DefaultServeMux` and metrics served
on the same port as CEPA, a second `ListenAndServe` call races at startup and one
server wins randomly. Alternatively, if `/metrics` is accidentally registered on
the CEPA mux, Prometheus scrape requests can trigger the `PUT`-only check in
`server.ServeHTTP` and the scraper receives 405 errors indefinitely.

**Why it happens:**
`prometheus.MustRegister` and `promhttp.Handler()` target `DefaultRegisterer`/
`DefaultServeMux` by default. Developers add a `http.Handle("/metrics", …)` call
without specifying which mux, polluting the default mux that the CEPA mux does
NOT use.

**Consequences:**
Prometheus scrapes fail silently (404 or 405). If metrics port equals CEPA port,
one listener never binds and the daemon exits or logs a panic at startup.

**Prevention:**

- Use a **separate port** (e.g., `9101`) for metrics with its own `*http.ServeMux`.
- Create a private `prometheus.NewRegistry()` and pass it explicitly to
  `promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})`.
- Never call `http.Handle` without a mux target in a daemon that has multiple
  HTTP servers.

**Detection:**
`curl -s http://localhost:9101/metrics` returns 404 or 405 on first deploy.

**Phase:** Prometheus metrics phase.

---

### Pitfall 2: Prometheus /metrics — Custom Collector blocking Collect()

**What goes wrong:**
The existing `pkg/metrics/Store` uses `sync/atomic` — perfectly fast. But if the
new Prometheus Collector's `Collect(ch chan<- Metric)` method acquires a mutex,
calls `metrics.M.Snapshot()` through a slow path, or does any I/O, it blocks the
entire Prometheus scrape goroutine. Because the library serializes all registered
Collectors per scrape, one blocking Collector delays all metrics, potentially
causing a Prometheus timeout and a "scrape duration exceeded" alert.

**Why it happens:**
Developers copy patterns from blog posts where the service has only one metric.
With `WriterErrorsTotal`, `EventsDroppedTotal`, `EventsReceivedTotal`, and
`QueueDepth` all gated behind a single `Collect` implementation, any added lock
propagates.

**Consequences:**
Prometheus scrape timeouts. `rate()` gaps in dashboards. Potential goroutine
pile-up if scrapes queue faster than they complete.

**Prevention:**

- Read directly from `metrics.M` atomic fields inside `Collect`; no mutex needed.
- `Collect` must be concurrency-safe (two scrapes can overlap); atomic reads
  are already safe.
- Implement `Describe` with static `*prometheus.Desc` values created at
  registration time, not dynamically inside `Describe`.

**Detection:**
`go test -race ./pkg/metrics/...` will catch any data race introduced around the
Collector. Prometheus scrape histogram `scrape_duration_seconds` spikes.

**Phase:** Prometheus metrics phase.

---

### Pitfall 3: Prometheus /metrics — Cardinality explosion on writer labels

**What goes wrong:**
If any label on a counter or gauge takes a value derived from the event stream
(file path, username, CEPA event type string, client IP), cardinality explodes.
A single NAS with thousands of files generates thousands of unique label
combinations; Prometheus memory usage grows without bound and eventually OOMs.

**Why it happens:**
The `WindowsEvent` struct contains `ObjectName` (file path) and
`SubjectUsername`. It is tempting to attach these as labels on
`cee_events_received_total` to enable per-file dashboards. This pattern is
correct for low-cardinality dimensions like `event_id` (5 values) or
`writer_type` (4 values), but catastrophic for paths.

**Consequences:**
Prometheus server memory exhaustion. The client binary's own memory grows because
`client_golang` caches all label combinations in the registry.

**Prevention:**

- Labels must be **bounded and low-cardinality**. Allowable label values for
  cee-exporter: `event_id` (4663, 4660, 4670), `writer` (gelf, evtx, beats,
  syslog), `status` (ok, error).
- Never label by: file path, username, client IP, SID, CEPA event string.
- Use `cee_events_received_total{event_id="4663"}` counters, not per-file
  counters.

**Detection:**
`curl -s http://localhost:9101/metrics | wc -l` grows unboundedly during an
active audit session — immediate sign of cardinality explosion.

**Phase:** Prometheus metrics phase.

---

### Pitfall 4: Windows Service — SCM 30-second startup timeout

**What goes wrong:**
The Go runtime initializes package-level variables, sets up goroutines, and runs
`init()` functions before `main()` executes. On a heavily loaded system at boot
(especially a VM with many services starting concurrently), the delay between
`CreateProcess` and the Go `main()` entry can exceed 26 seconds (documented in
golang/go#23479). If `main()` then performs TLS certificate loading, config
parsing, and network listener binding before calling `svc.Run()`, the total time
easily breaches the SCM's 30-second window, generating Event ID 7009/7000 and
killing the process.

**Why it happens:**
Go executables are heavier than simple C++ services at startup. The SCM clock
starts at `CreateProcess`, not at `main()`.

**Consequences:**
Service fails to start on every system reboot. Manual starts succeed (system is
idle), masking the bug during development.

**Prevention:**

1. In `Execute()`, send `svc.Status{State: svc.StartPending}` as the **very
   first action** before any config loading or listener binding.
2. Use `StartType: mgr.StartAutoDelayed` (Automatic Delayed Start, which gives
   120 seconds) for the service configuration rather than `mgr.StartAutomatic`.
3. Keep `init()` functions and package-level variable initialization in
   `cmd/cee-exporter/main.go` minimal — defer config loading into `main()`.
4. The `Execute` implementation must send `Running` status within 30 seconds of
   the `StartPending` send.

**Detection:**
Windows Event Log → System channel → Event ID 7009 after a clean reboot.

**Phase:** Windows Service phase.

---

### Pitfall 5: Windows Service — os/signal SIGTERM conflict with SCM

**What goes wrong:**
The current `main.go` likely uses `os/signal.Notify(c, syscall.SIGTERM,
os.Interrupt)` for graceful shutdown on Linux. On Windows as a managed service,
the SCM sends SERVICE_CONTROL_STOP, not SIGTERM. The `golang.org/x/sys/windows/svc`
package installs its own Windows control handler. If `os/signal.Notify` also
registers for `syscall.SIGTERM`, the two handlers fight over the same underlying
Win32 `SetConsoleCtrlHandler` API. The SCM STOP command may be swallowed by one
handler and never reach the other, causing the service to appear "stuck stopping"
and the SCM to forcefully kill it after the stop timeout.

**Why it happens:**
Cross-platform `main.go` files share signal handling code between the service and
non-service paths. The `svc.IsWindowsService()` check is easy to forget before
registering OS signals.

**Consequences:**
`sc stop cee-exporter` hangs for 20 seconds then force-kills the process. The
queue is not drained; in-flight events are lost.

**Prevention:**

- Gate `os/signal.Notify` behind `!svc.IsWindowsService()`.
- In service mode, all shutdown must flow through the `<-r` channel in the
  `Execute` method, which receives `svc.ChangeRequest{Cmd: svc.Stop}`.
- The service `Execute` must write `svc.StopPending` to `s` before returning, so
  the SCM knows shutdown is in progress.
- Add build-tag-guarded `cmd/cee-exporter/service_windows.go` and
  `service_notwindows.go` rather than mixing the paths in `main.go`.

**Detection:**
`sc stop cee-exporter` hangs >5 seconds in testing. Check with `sc query
cee-exporter` during the hang — state is STOP_PENDING indefinitely.

**Phase:** Windows Service phase.

---

### Pitfall 6: Windows Service — panic in Execute() crashes SCM registration

**What goes wrong:**
Any unrecovered panic in the goroutine that calls `svc.Run()` (which internally
calls the `Execute` method) will crash the entire process. The SCM will then log
Event ID 7031 ("service terminated unexpectedly") and may restart per the
configured recovery action. However, if the panic happens **before** the service
has sent `Running` status, the SCM marks the service as failed immediately and
increments the failure counter, potentially reaching the "take no action" state
after 3 resets — meaning the service never auto-restarts again.

**Why it happens:**
Writers like `BinaryEvtxWriter` or `BeatsWriter` are constructed inside
`Execute()`. A nil-pointer or failed initialization in a new writer will panic
rather than return an error because the factory pattern used in the existing code
(e.g., `NewGELFWriter`) returns an error, but code calling it might not check
the error before deferring to the writer.

**Consequences:**
Service permanently disabled by SCM after 3 crashes. Requires manual
`sc failure cee-exporter reset= 0` and `sc start`.

**Prevention:**

- Wrap the body of `Execute()` in `defer func() { if r := recover(); r != nil {
  slog.Error("service_panic", "panic", r); } }()`.
- All writer construction errors must be returned as errors, never trigger
  panics. Use `prometheus.MustRegister` only in `init()` or at program start —
  not inside `Execute()` where a registration conflict panics at service start.
- Validate all config before calling `svc.Run()`.

**Detection:**
Windows Event Log → Application channel, look for unhandled exception or go
runtime crash entries alongside Event ID 7031.

**Phase:** Windows Service phase.

---

### Pitfall 7: BinaryEvtxWriter — Dual chunk checksum fields

**What goes wrong:**
The EVTX chunk header contains **two** CRC32 fields that must be computed
separately and in the correct order:

1. **Event records checksum** (offset 52): CRC32 of all event record bytes in
   the chunk data region.
2. **Header checksum** (offset 124): CRC32 of the first 120 bytes of the chunk
   header **plus** bytes 128-512 of the chunk header (the string offset array
   and template pointer array).

If both checksums are computed with the same input, or if checksum 1 is omitted,
Event Viewer rejects the file with no useful error message.

**Why it happens:**
The libevtx format spec describes both checksums but the distinction is subtle.
Read-only parsers (like `golang-evtx`) often skip validation and work fine with
wrong checksums. Writers discovered this only when testing against Event Viewer.

**Consequences:**
Event Viewer silently shows an empty log or returns "The event log file is
corrupted." `wevtutil` may partially parse but report individual record errors.

**Prevention:**

- Use RFC 1952 CRC32 (polynomial 0xEDB88320, initial value 0) for both.
- Compute event records checksum **first**, write it at offset 52, then compute
  the header checksum covering the already-updated offset-52 value.
- Write a unit test that round-trips via `wevtutil qe <file>` or the Rust
  `evtx` crate to verify checksum correctness before integrating with the Writer
  interface.

**Detection:**
Event Viewer → "The event log file is corrupted" on open. `wevtutil qe
output.evtx` returns error with no events.

**Phase:** BinaryEvtxWriter phase.

---

### Pitfall 8: BinaryEvtxWriter — Record size field must appear at both start and end

**What goes wrong:**
Each EVTX event record has the record size (4 bytes) at **offset 4** (after the
magic `0x2A2A0000`) AND a **copy of the size** as the final 4 bytes of the
record. If the two values disagree, Event Viewer marks the record as corrupt and
skips it. If the trailing size copy is missing entirely, every subsequent record
offset is wrong and the entire chunk is unreadable.

**Why it happens:**
Pure-Go writers naively write the header size field as a placeholder, fill in
the BinXML content, then forget to append the trailing copy. The format mirrors
the NT file record structure and is non-obvious from partial documentation.

**Consequences:**
Corrupt individual records or entire chunk. Event Viewer skips all records after
the first bad one.

**Prevention:**

- Build records in a `bytes.Buffer`, compute final size, then write:
  `[magic][size][record_id][timestamp][binxml][size_copy]`.
- Assert `len(record) == size` before writing to chunk.
- Test with records of varying BinXML content length.

**Detection:**
`evtx` (Rust crate, `omerbenamram/evtx`) reports "size mismatch between record
header and record copy" per record.

**Phase:** BinaryEvtxWriter phase.

---

### Pitfall 9: BinaryEvtxWriter — String table and template offset management

**What goes wrong:**
The BinXML encoding uses an in-chunk **string table** (up to 64 entries in the
common string offset array at chunk header offset 128-384) and a **template
pointer array** (32 entries at 384-512). Offsets in these arrays are relative to
the start of the **chunk**, not the start of the file. If a writer computes
offsets from the start of the file, every string reference is wrong. Worse, if
the string table is not populated (all zeros), Event Viewer falls back to
inline string encoding — which is 3-5x larger — but will reject records that
reference string table entries by offset if the table is empty.

**Why it happens:**
The offset coordinate system switches between file-relative (file header) and
chunk-relative (chunk header and BinXML token stream). This is not consistently
spelled out in informal documentation.

**Consequences:**
Bloated .evtx files if string table is skipped. Corrupt records if wrong offset
base is used for string references.

**Prevention:**

- Maintain a `map[string]uint32` string intern table per chunk, reset at each
  new chunk boundary (every 64 KB).
- When serializing BinXML `Value` tokens that reference the string table, use
  the chunk-relative offset, not an absolute file offset.
- Template pointers (the 32-entry array) should be populated for any template
  used more than once in the chunk; compute their chunk-relative offsets after
  the template bytes are written.

**Detection:**
Parsed XML from `evtx` crate shows empty `<Data>` elements where values should
appear — classic symptom of a broken string table reference.

**Phase:** BinaryEvtxWriter phase.

---

### Pitfall 10: BinaryEvtxWriter — build tag vs. stub coexistence

**What goes wrong:**
`writer_evtx_stub.go` already uses `//go:build !windows`. If the new real
`BinaryEvtxWriter` is placed in a file without a build tag, both the stub and
the real implementation are compiled simultaneously on non-Windows, producing a
duplicate symbol error. If the real implementation also uses `!windows`, the
stub must be removed or renamed so only one `BinaryEvtxWriter` type exists per
target.

**Why it happens:**
The stub was intentionally compiled on all non-Windows platforms to satisfy the
`Writer` interface. Adding the real implementation requires coordinating two files
that previously coexisted without conflict.

**Consequences:**
`go build` fails with "BinaryEvtxWriter redeclared in this block" on Linux.

**Prevention:**

- Remove `writer_evtx_stub.go` when the real implementation is complete.
- During incremental development, rename the stub to
  `writer_evtx_stub_placeholder.go` with a temporary build tag
  `//go:build ignore` so it is excluded.
- Follow the existing naming convention: the real writer file should be
  `writer_evtx_notwindows.go` with `//go:build !windows`.

**Detection:**
`go build ./...` on Linux immediately after adding the new file.

**Phase:** BinaryEvtxWriter phase.

---

### Pitfall 11: BeatsWriter — AsyncClient callback deadlock

**What goes wrong:**
`go-lumber`'s `AsyncClient.Send(cb, data)` is safe for concurrent calls.
However, the `cb` callback (called when the server ACKs the batch) **must not
block**. If the callback attempts to re-enqueue events by calling
`queue.Enqueue()` — for example, to retry unacknowledged events — it calls back
into the same `AsyncClient.Send()` path, which acquires an internal semaphore.
If the inflight window is full, `Send` blocks, and since `Send` is waiting for
`cb` to return (which is waiting for `Send` to unblock), the program deadlocks.

**Why it happens:**
The go-lumber documentation explicitly warns: "The callback MUST NOT BLOCK. In
case the callback is trying to republish not ACKed events, care must be taken
not to deadlock the AsyncClient when calling Send." This warning is easy to miss
because most examples show simple logging callbacks.

**Consequences:**
The queue worker goroutine hangs permanently on `WriteEvent`. The buffered event
channel fills, Enqueue starts dropping events, and the
`cee_events_dropped_total` counter climbs without bound — but no error is logged
because the worker never returns an error.

**Prevention:**

- Use `SyncClient` for the initial implementation. It is simpler and thread-safe
  for the single-worker-per-writer pattern that the existing queue already
  provides (N queue workers each owning one writer call).
- If `AsyncClient` is used, the callback must only signal a separate goroutine
  (e.g., by sending to a channel) and return immediately.
- Never call `AsyncClient.Send()` or `queue.Enqueue()` from inside the
  AsyncClient callback.

**Detection:**
`goroutine dump (kill -SIGQUIT on Linux)` shows all queue workers blocked in
`go-lumber.(*AsyncClient).Send` → waiting on semaphore → callback goroutine
blocked on the same semaphore.

**Phase:** BeatsWriter phase.

---

### Pitfall 12: BeatsWriter — SyncClient is not goroutine-safe

**What goes wrong:**
`go-lumber`'s `SyncClient` is documented as **not thread-safe**. The existing
queue runs N worker goroutines (default: configurable via `workers` in config),
each calling `writer.WriteEvent()` concurrently. If a single `SyncClient` is
shared across all workers without a mutex, concurrent `Send()` calls corrupt the
internal framing state and produce a malformed Lumberjack v2 byte stream that
the receiving Logstash/Graylog Beats input cannot parse.

**Why it happens:**
The existing `GELFWriter` uses a `sync.Mutex` around the connection, making it
safe for concurrent queue workers. The BeatsWriter will look superficially
similar but the underlying `SyncClient` has no internal locking.

**Consequences:**
Logstash/Graylog Beats input reports framing errors and drops connections. Events
are silently lost (no ACK, no retry in SyncClient).

**Prevention:**

- Wrap `SyncClient` calls in a `sync.Mutex` inside `BeatsWriter.WriteEvent()`.
- Or, create one `SyncClient` **per queue worker** — but this requires
  architectural support (the Writer interface currently has no per-worker
  initialization hook).
- The mutex approach is simpler and consistent with `GELFWriter`'s existing
  pattern.

**Detection:**
Graylog/Logstash logs show "connection reset by peer" or "framing error" from
the Beats input. `-race` test on `BeatsWriter` immediately flags the race.

**Phase:** BeatsWriter phase.

---

### Pitfall 13: BeatsWriter — Missing TLS custom dialer path

**What goes wrong:**
`go-lumber`'s `SyncDial(address, opts...)` only accepts a plain TCP connection.
TLS is not a built-in option. To use TLS with Logstash/Graylog Beats TLS input,
the caller must use `NewSyncClientWithConn(tlsConn, opts...)` where `tlsConn` is
a `*tls.Conn`. If the reconnect logic in `BeatsWriter` calls `SyncDial` instead
of the TLS-aware constructor after a connection failure, subsequent reconnections
are plaintext, silently downgrading security.

**Why it happens:**
The TLS path is not documented in the `go-lumber` README. It is only discoverable
by reading the API. The natural reconnect pattern (`SyncDial` on error) bypasses
TLS.

**Consequences:**
Beats events sent in plaintext to a TLS-only input get rejected with a TLS
handshake error. If the server accepts both, data transits unencrypted — an audit
data confidentiality violation.

**Prevention:**

- Implement a `dialFunc` that always wraps `net.Dial` with `tls.Client()` when
  TLS is configured.
- Use `SyncDialWith(dialFunc, address, opts...)` for the initial connection.
- Use the same `dialFunc` in the reconnect path to ensure TLS is never dropped.
- Mirror the TLS pattern from `GELFWriter.connect()` which already handles TLS
  correctly.

**Detection:**
Wireshark on the Beats port shows plaintext frames when TLS was configured. OR:
`openssl s_client -connect host:5044` succeeds but Logstash logs show
`ssl_error_rx_record_too_long` (plaintext data sent to TLS socket).

**Phase:** BeatsWriter phase.

---

### Pitfall 14: SyslogWriter — log/syslog package unavailable on Windows

**What goes wrong:**
Go's stdlib `log/syslog` package has `//go:build !windows && !plan9`. On Windows
(with or without `CGO_ENABLED=0`), importing it causes a build failure:
"build constraints exclude all Go files in …/log/syslog". Since cee-exporter
must cross-compile from Linux to Windows, any `SyslogWriter` that imports
`log/syslog` will break the Windows build.

**Why it happens:**
The natural instinct is to use stdlib syslog since no external dependency is
needed. The build constraint is correct — syslog is a Unix protocol — but the
error only surfaces during cross-compilation, not on the development Linux host.

**Consequences:**
`GOOS=windows go build ./...` fails in CI. The Windows binary cannot be built.

**Prevention:**

- Use a third-party pure-Go RFC 5424 library that has no Windows build
  constraint, such as `github.com/RackSec/srslog` or write directly to a UDP/TCP
  socket with manual RFC 5424 message formatting.
- Gate `SyslogWriter` with `//go:build !windows` and provide a stub with the
  same build tag pattern as `writer_evtx_stub.go`.
- Verify cross-compilation passes in the `Makefile` `cross-compile` target before
  merging.

**Detection:**
`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...` fails with "build
constraints exclude all Go files" referencing `log/syslog`.

**Phase:** SyslogWriter phase.

---

### Pitfall 15: SyslogWriter — stdlib log/syslog is not RFC 5424 compliant

**What goes wrong:**
Even on Linux, Go's `log/syslog` package generates a non-standard hybrid format:
`<PRI>RFC3339Timestamp HOSTNAME APP-NAME[PID]: MESSAGE` — mixing RFC 3164
structure with RFC 5424 timestamp. The VERSION field (the digit `1`) required by
RFC 5424 is missing. Many syslog servers that enforce strict RFC 5424 parsing
(rsyslog with `$ActionFileDefaultTemplate RSYSLOG_SyslogProtocol23Format`) reject
these messages or misparsed the PRI.

**Why it happens:**
This is a known Go stdlib deficiency (golang/go#66666, filed 2024). The stdlib
has not been patched.

**Consequences:**
STRUCTURED-DATA elements encoded in the message are never parsed by the syslog
server. Windows event fields sent as structured data are lost.

**Prevention:**

- Use `github.com/nathanaelle/syslog5424/v2` or `github.com/leavengood/rfc5424`
  for proper RFC 5424 message construction.
- These libraries correctly handle: VERSION field, STRUCTURED-DATA encoding,
  required escaping of `]`, `"`, `\` in parameter values, and consecutive
  SD-ELEMENT placement without spaces.
- Write a unit test that validates the output of `SyslogWriter` against the RFC
  5424 ABNF grammar rather than eyeballing it.

**Detection:**
Capture output with `nc -l -u 514` and check whether the byte stream starts
with `<PRI>1` (note the `1` and space = RFC 5424 VERSION). If it starts with
`<PRI>2024-...` (a timestamp after PRI), it is non-compliant.

**Phase:** SyslogWriter phase.

---

### Pitfall 16: SyslogWriter — nil pointer on closed UDP connection

**What goes wrong:**
The existing `GELFWriter` uses a `sync.Mutex` and reconnects on write failure.
A naive `SyslogWriter` over UDP that closes the `net.Conn` in one goroutine
(during shutdown or reconnect) while another queue worker goroutine is mid-write
will panic with a nil pointer dereference if `w.conn` is set to `nil` after the
lock is released but before the write call.

**Why it happens:**
The close-then-nil pattern (`w.conn.Close(); w.conn = nil`) is not safe if the
mutex is released between the close and the nil assignment, or if code checks
`w.conn != nil` outside the mutex.

**Consequences:**
Process panic. On Linux as a systemd service, systemd restarts the daemon (if
`Restart=on-failure`). Events in the queue are lost on restart.

**Prevention:**

- Always hold the mutex for the **entire** close-and-nil sequence.
- Check `w.conn != nil` **inside** the mutex before every write.
- Mirror the exact pattern from `GELFWriter.WriteEvent()`: lock, nil-check,
  write, reconnect-on-error — no gap between the check and the write.
- Add a `closing` atomic bool to suppress reconnect attempts during `Close()`.

**Detection:**
Race detector: `go test -race ./pkg/evtx/...` with concurrent Close and WriteEvent
calls in the test.

**Phase:** SyslogWriter phase.

---

### Pitfall 17: SyslogWriter — RFC 5424 STRUCTURED-DATA encoding of WindowsEvent fields

**What goes wrong:**
RFC 5424 STRUCTURED-DATA requires that parameter values escape three characters:
`]` → `\]`, `"` → `\"`, `\` → `\\`. Windows file paths contain backslashes
(`C:\Share\File.txt`). CEPA file paths from PowerStore NAS are Unix paths but
may contain characters that require escaping. SIDs contain hyphens but no
escapable characters. If the writer does raw string insertion without escaping,
the syslog server cannot parse the STRUCTURED-DATA block.

Additionally, the RFC prohibits the same SD-ID appearing more than once per
message. A writer that emits multiple SD-ELEMENTs for the same group (e.g., two
`[cee-exporter@xxx ...]` blocks) will be rejected by strict parsers.

**Why it happens:**
String formatting shortcuts like `fmt.Sprintf("[%s %s=\"%s\"]", id, key, value)`
do not escape the value. The escaping requirement is easy to overlook.

**Consequences:**
Syslog server logs parse errors. Fields are silently dropped. Windows file paths
with backslashes corrupt the entire STRUCTURED-DATA section.

**Prevention:**

- Delegate STRUCTURED-DATA construction to a validated library
  (`nathanaelle/syslog5424` handles escaping automatically).
- If hand-coding, write a `escapeSyslogParam(s string) string` helper that
  applies `strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "]", "\\]")` and
  test it with path strings like `C:\Users\Admin\Report.xlsx`.
- Use a single SD-ID (e.g., `cee@32473`) with all WindowsEvent fields as
  parameters, rather than multiple SD-ELEMENTs.

**Detection:**
Parse the emitted syslog message with `github.com/influxdata/go-syslog` in
a unit test. An invalid STRUCTURED-DATA section causes a parse error.

**Phase:** SyslogWriter phase.

---

### Pitfall 18: Systemd unit file — missing After= and Wants= for network

**What goes wrong:**
If the unit file does not include `After=network-online.target` and
`Wants=network-online.target`, systemd may start cee-exporter before the network
interface that serves CEPA traffic is up. The CEPA listener will bind to
`0.0.0.0:12228` successfully (because the kernel accepts binds to wildcard even
without routes), but the GELF writer's `connect()` call (which resolves a
hostname and opens a TCP connection) will fail. Depending on the error handling,
the daemon exits at startup or runs with a broken writer.

**Why it happens:**
`After=network.target` only means the `network.target` systemd milestone is
reached, which does not guarantee routable network interfaces. The
`network-online.target` is the correct dependency for services that need an
actual routable connection at startup.

**Consequences:**
GELF writer fails to connect at startup. Daemon exits (if the connection error
is fatal) or runs without a working output writer (if the error is swallowed).

**Prevention:**

```ini
[Unit]
After=network-online.target
Wants=network-online.target
```

- For services that reconnect automatically on write failure (as GELFWriter does),
  `After=network.target` is tolerable but `network-online.target` is safer.
- Add `Restart=on-failure` and `RestartSec=5s` so a startup network failure
  triggers a restart rather than leaving the daemon in a failed state.

**Detection:**
`systemctl status cee-exporter` shows "active (running)" but GELF writer logs
"gelf connect tcp://...: dial tcp: i/o timeout" in `journalctl -u cee-exporter`.

**Phase:** Systemd unit file phase.

---

### Pitfall 19: Systemd unit file — ExecStart path and binary location

**What goes wrong:**
Systemd unit files require **absolute paths** in `ExecStart`. A relative path or
a path using shell expansion (`~/bin/cee-exporter`) causes the unit to fail with
"Executable path is not absolute." Additionally, if the binary is installed to
`/usr/local/bin` but the unit file references `/usr/bin`, the service fails with
`ENOENT` and journald logs only "Failed to execute command" without the actual
missing file.

**Why it happens:**
Makefile installs are often configurable (`PREFIX=/usr/local`), but the unit
file is written at development time with a hardcoded path. The install and unit
paths fall out of sync.

**Consequences:**
`systemctl start cee-exporter` fails with "failed to execute" error.

**Prevention:**

- Parameterize the install path: use `make install DESTDIR=/usr/local/bin` and
  generate the unit file from a template (`cee-exporter.service.in`) during
  `make install`, substituting `@BINDIR@`.
- Default to `/usr/local/bin/cee-exporter` (matches standard Go binary install
  convention).
- Run `systemd-analyze verify cee-exporter.service` as part of CI.

**Detection:**
`systemctl start cee-exporter && systemctl status cee-exporter` immediately; if
`ExecStart` path is wrong, status shows `code=exited, status=203/EXEC` within 1
second.

**Phase:** Systemd unit file phase.

---

### Pitfall 20: Systemd unit file — Type= mismatch blocks start

**What goes wrong:**
Go daemons do **not** fork. They run in the foreground. Using `Type=forking` for
a non-forking Go binary causes systemd to wait indefinitely for a child PID file
that never appears, eventually timing out and killing the process.

If `Type=notify` is used without sending `sd_notify READY=1` from the application,
systemd waits for the notification until `TimeoutStartSec` expires and kills the
process.

**Why it happens:**
Copy-paste from init script templates or systemd units for Java/Python services
that do daemonize.

**Consequences:**
`systemctl start cee-exporter` times out. Service is marked as failed.

**Prevention:**

- Use `Type=simple` (safe and correct for any Go daemon that does not call
  `daemon.SdNotify(false, daemon.SdNotifyReady)`).
- OR use `Type=notify` if the daemon sends `READY=1` via `coreos/go-systemd`
  after the CEPA listener is bound and writers are initialized.
- `Type=notify` is preferred because systemd then knows the daemon is truly
  ready before marking it active, which matters for dependent services.
- If using `Type=notify`, add `coreos/go-systemd/v22` as a dependency and send
  `daemon.SdNotify(false, daemon.SdNotifyReady)` after `listener.Accept()` loop
  is started.

**Detection:**
`systemctl start cee-exporter` hangs for `TimeoutStartSec` seconds (default 90s)
before failing, OR starts immediately but `ss -tlnp | grep 12228` shows the port
not yet bound when systemd declared the service active.

**Phase:** Systemd unit file phase.

---

## Moderate Pitfalls

Mistakes that cause incorrect behavior or maintenance burden but do not require a full rewrite.

---

### Pitfall 21: CGO_ENABLED=0 and make test -race incompatibility

**What goes wrong:**
The existing `Makefile` omits `-race` because the race detector requires CGO.
If a new `make test-race` target is added for v2 development (which is valuable
for catching concurrency bugs in `BeatsWriter`, `SyslogWriter`), developers on
Linux will need to temporarily enable CGO for that target only. If the Makefile
silently succeeds with `CGO_ENABLED=0 go test -race ./...`, the race detector
reports nothing (it is disabled) but the command exits 0, creating a false sense
of safety.

**Prevention:**

- Add a `test-race` target with `CGO_ENABLED=1 go test -race ./...`.
- Document in the Makefile comment that `-race` requires CGO and cannot be used
  for the production cross-compiled binary.
- The production binary (`make build`) stays `CGO_ENABLED=0`.

**Phase:** All writer phases (run race tests during writer development).

---

### Pitfall 22: MultiWriter error aggregation masks individual writer failures

**What goes wrong:**
`MultiWriter.WriteEvent` calls all writers and joins errors. If `BeatsWriter`
fails (connection refused, ACK timeout), the error is joined and returned to the
queue worker, which increments `WriterErrorsTotal` and logs it. However, the
event is still written to other writers (GELF, syslog). The log shows a generic
"writer_error" with a joined error string that includes all writer names — making
it hard to distinguish "GELF OK, Beats failed" from "Beats OK, GELF failed."

**Prevention:**

- Extend the error type to carry per-writer status: a `WriterError` struct with
  `WriterName string` and `Err error`.
- Or add per-writer error counters to `pkg/metrics` (e.g.,
  `cee_writer_errors_total{writer="beats"}`).
- At minimum, log the writer name alongside each error in the queue worker's
  error path.

**Phase:** BeatsWriter and SyslogWriter phases.

---

### Pitfall 23: Prometheus metric name conflicts with existing metrics.Store field names

**What goes wrong:**
`pkg/metrics/Store` exposes `EventsReceivedTotal`, `EventsWrittenTotal`,
`EventsDroppedTotal`, `WriterErrorsTotal` as public `atomic.Int64` fields.
When adding Prometheus counters, developers may introduce a separate
`prometheus.Counter` named `cee_events_received_total` that is incremented
independently — resulting in two parallel counters that drift apart (e.g., one
counts only parsed events, the other counts raw HTTP bodies).

**Prevention:**

- The Prometheus Collector should **read from** `metrics.M` atomics, not
  maintain a separate counter. This ensures a single source of truth.
- Implement a custom `Collector` that wraps `metrics.M.Snapshot()` and emits
  `prometheus.NewConstMetric` for each field.
- This eliminates drift and avoids double-counting.

**Phase:** Prometheus metrics phase.

---

### Pitfall 24: Windows Service installer path — NSSM vs. native svc.Run()

**What goes wrong:**
NSSM (Non-Sucking Service Manager) wraps any executable as a Windows service.
It is popular but introduces an extra binary dependency, requires the NSSM binary
to be present on the target machine, and does not integrate with Go's `svc.Run()`
for proper SCM protocol (status reporting, service control handler). The service
appears as a generic NSSM service, not a native Go service, and SCM-based
management tools may show incorrect state.

**Prevention:**

- Use native `golang.org/x/sys/windows/svc` with `svc.Run()` for proper SCM
  integration. This is already a dependency in `writer_windows.go`.
- Ship a self-install/uninstall subcommand: `cee-exporter install` / `cee-exporter
  uninstall` using `svc/mgr` to register the service.
- Avoid NSSM for production deployments; document it only as a dev convenience.

**Phase:** Windows Service phase.

---

## Minor Pitfalls

---

### Pitfall 25: EVTX file size — 64 KB chunk boundary not aligned

**What goes wrong:**
EVTX chunks are exactly 65536 bytes (0x10000). If the writer appends records to
a chunk without checking whether the next record fits, a record can span a chunk
boundary. This is invalid — records must be entirely within one chunk.

**Prevention:**

- Track `bytesUsed` per chunk. Before appending a record, check if
  `bytesUsed + recordSize > 65536 - 512` (reserving the 512-byte header).
  If so, finalize the current chunk and start a new one.
- Unit test: write 100 events with 500-byte BinXML payloads and verify all chunk
  sizes are exactly 65536 bytes.

**Phase:** BinaryEvtxWriter phase.

---

### Pitfall 26: Beats connection — missing read timeout causes goroutine leak

**What goes wrong:**
`go-lumber` `SyncClient.Send()` waits for an ACK from the server. If the server
stalls (e.g., Logstash GC pause, Graylog Beats input overload), the `Send` call
blocks indefinitely. The queue worker goroutine holding the write lock (or mutex)
is stuck, preventing any other events from being written.

**Prevention:**

- Always set `go-lumber`'s `Timeout` option to a value shorter than the CEPA
  heartbeat timeout (e.g., 5 seconds): `v2.Timeout(5 * time.Second)`.
- The queue channel will back up if `WriteEvent` stalls, but at least the mutex
  is released within 5 seconds and other writers in `MultiWriter` continue.

**Phase:** BeatsWriter phase.

---

### Pitfall 27: Systemd unit — running as root

**What goes wrong:**
Without a `User=` directive, systemd runs the service as root. A bug in the HTTP
handler or writer code that leads to arbitrary file writes would have
unrestricted filesystem access.

**Prevention:**

- Add `User=cee-exporter` and `Group=cee-exporter` with a dedicated system
  account created by the install script/package.
- Use `CapabilityBoundingSet=` to drop all unnecessary capabilities.
- Add `ProtectSystem=strict` and `PrivateTmp=true` for defense in depth.
- The binary only needs `CAP_NET_BIND_SERVICE` if listening on port < 1024;
  port 12228 does not require it.

**Phase:** Systemd unit file phase.

---

## Phase-Specific Warnings

| Phase Topic | Likely Pitfall | Mitigation |
|-------------|---------------|------------|
| Prometheus /metrics | Mux collision with CEPA listener on same port | Dedicated port + explicit registry |
| Prometheus /metrics | Blocking Collect() | Use atomics from metrics.M directly |
| Prometheus /metrics | File path / username labels | Labels must be bounded: event_id, writer, status only |
| Prometheus /metrics | Separate counter from metrics.Store | Wrap metrics.M in custom Collector, don't duplicate |
| Systemd unit | `Type=forking` for Go binary | Use `Type=simple` or `Type=notify` |
| Systemd unit | Path drift between Makefile install and unit file | Generate unit from template at `make install` time |
| Systemd unit | Missing `network-online.target` | Add `After=` and `Wants=` for network-online |
| Systemd unit | Running as root | Add `User=cee-exporter`, drop capabilities |
| Windows Service | 30-second SCM startup timeout | Send `StartPending` before any config/network init |
| Windows Service | SIGTERM vs SCM conflict | Gate `os/signal.Notify` behind `!svc.IsWindowsService()` |
| Windows Service | Panic in Execute() kills SCM registration | Wrap Execute body in `defer recover()` |
| Windows Service | NSSM dependency | Use native `svc.Run()` with self-install subcommand |
| BinaryEvtxWriter | Dual checksum fields confused | Compute both CRC32s in correct order, correct range |
| BinaryEvtxWriter | Record trailing size copy missing | Buffer record, append size copy before writing |
| BinaryEvtxWriter | Offset base confusion (file vs chunk) | String table offsets always chunk-relative |
| BinaryEvtxWriter | Stub/real type name conflict | Remove stub file when real implementation added |
| BinaryEvtxWriter | Chunk boundary overflow | Check `bytesUsed` before appending each record |
| BeatsWriter | AsyncClient callback deadlock | Use SyncClient with mutex; never call Send from callback |
| BeatsWriter | SyncClient not thread-safe | Protect with sync.Mutex; mirrors GELFWriter pattern |
| BeatsWriter | TLS not preserved on reconnect | Use custom dialFunc for initial + reconnect path |
| BeatsWriter | Missing read timeout goroutine leak | Set `v2.Timeout(5s)` option |
| SyslogWriter | log/syslog Windows build failure | Use third-party RFC 5424 library; add `!windows` build tag |
| SyslogWriter | stdlib syslog not RFC 5424 compliant | Use nathanaelle/syslog5424 or leavengood/rfc5424 |
| SyslogWriter | Nil pointer on closed UDP conn | Hold mutex for close-and-nil sequence; mirror GELFWriter |
| SyslogWriter | Unescaped backslash in Windows paths | Use library with built-in escaping for structured data |
| All writers | CGO_ENABLED=0 + -race incompatibility | Separate `make test-race` target with CGO_ENABLED=1 |
| All writers | MultiWriter obscures per-writer failures | Add per-writer error metric label |

---

## Sources

- [libevtx EVTX format specification](https://github.com/libyal/libevtx/blob/main/documentation/Windows%20XML%20Event%20Log%20(EVTX).asciidoc) — chunk header layout, checksums, string tables (HIGH confidence)
- [golang-evtx chunk.go](https://github.com/0xrawsec/golang-evtx/blob/master/evtx/chunk.go) — Go ChunkHeader struct and validation constraints (HIGH confidence)
- [prometheus/client_golang Collector interface](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus) — Describe/Collect contract, blocking behavior, MustRegister (HIGH confidence)
- [prometheus cardinality explosion discussion](https://github.com/prometheus/client_golang/discussions/970) — cardinality limits, design tradeoffs (HIGH confidence)
- [go-lumber v2 client API](https://pkg.go.dev/github.com/elastic/go-lumber/client/v2) — SyncClient, AsyncClient, thread safety, TLS, callback deadlock warning (HIGH confidence)
- [golang/go#23479 Windows service startup timeout](https://github.com/golang/go/issues/23479) — 30-second SCM timeout root cause and workarounds (HIGH confidence)
- [golang.org/x/sys/windows/svc package](https://pkg.go.dev/golang.org/x/sys/windows/svc) — Execute method, service state machine, SCM protocol (HIGH confidence)
- [golang/go#66666 log/syslog RFC compliance](https://github.com/golang/go/issues/66666) — missing VERSION field in stdlib syslog (HIGH confidence)
- [RFC 5424 syslog protocol](https://datatracker.ietf.org/doc/html/rfc5424) — STRUCTURED-DATA encoding, escaping rules, SD-ID constraints (HIGH confidence)
- [coreos/go-systemd sd_notify](https://pkg.go.dev/github.com/coreos/go-systemd/daemon) — READY=1 notification, Type=notify integration (HIGH confidence)
- [systemd.service man page](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html) — ExecStart, Type=, After=, network-online.target (HIGH confidence)
- [golang/go#40157 windows/svc shutdown handling](https://github.com/golang/go/issues/40157) — SCM stop signal handling issues (MEDIUM confidence)
- Codebase inspection: `pkg/evtx/writer.go`, `pkg/evtx/writer_gelf.go`, `pkg/queue/queue.go`, `pkg/metrics/metrics.go`, `pkg/server/server.go` — integration-specific pitfalls derived from actual implementation (HIGH confidence)
