# Requirements

This is the aggregate requirement list `docs/PRD.md` links to. It merges the three
per-milestone `REQUIREMENTS.md` files that used to live under `.planning/`
(archived at [`docs/archive/planning/`](archive/planning/README.md) as of 2026-08-08) with
the requirement IDs already summarised in `docs/PRD.md`'s own Functional
Requirements section.

**The "Verified by" column is the point of this document.** It names a real test
file or CI job, or says **Unverified** with a pointer to what would close the
gap. Nothing here names a test that doesn't exist — every path below was
confirmed present with `ls`/`grep` on 2026-08-08. Several rows below turned up
requirements the original per-milestone documents marked "Complete" that have
no automated test behind them; those are called out explicitly rather than
carried forward as clean.

This document is the canonical source for requirement IDs going forward.
`docs/PRD.md`'s own inline requirement table (below its "Full requirement
list" link) predates this consolidation and uses different, non-canonical
numbers for some v2.0 items (its `OBS-04` and `TLS-03`/`TLS-04` rows) — see
[ID renumbering](#id-renumbering-notes) at the bottom of this page for how
those map to the IDs used here.

---

## v1.0 MVP (shipped 2026-03-03)

### CEPA Protocol

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| CEPA-01 | Listener completes the RegisterRequest handshake with HTTP 200 OK and strictly empty body | Delivered | `pkg/server/server_test.go::TestServeHTTP_RegisterRequest_EmptyBody` (added in `77aec66`) asserts the response body is exactly zero bytes, not merely visually empty, across two payload shapes. Updated 2026-08-08 — this row previously said nothing exercised `Handler.ServeHTTP` end-to-end; that stopped being true when this test landed. |
| CEPA-02 | Listener responds to heartbeat PUT requests within 3 seconds | **Unverified** | Nothing measures response latency. The ACK-before-processing ordering is correct by inspection (`pkg/server/server.go:94-116`) but untested. |
| CEPA-03 | Listener parses single-event CEE XML payloads into CEPAEvent structs | Delivered | `pkg/parser/parser_test.go::TestParse` ("single_event" case) |
| CEPA-04 | Listener parses VCAPS bulk batch XML payloads (EventBatch) | Delivered | `pkg/parser/parser_test.go::TestParse` ("vcaps_batch_two_events" case) |
| CEPA-05 | HTTP handler ACKs immediately and delegates event processing to an async queue | Delivered | `pkg/server/server_test.go::TestServeHTTP_ACKsBeforeQueueWork` (added in `77aec66`) proves `ServeHTTP` returns a written 200 while the queued write is provably still in progress, via a channel-enforced happens-before chain — not the strict "WriteHeader precedes the enqueue loop" ordering, which the test's own doc comment explains is not observable from outside `ServeHTTP` (see `docs/PROMISES.md` for the full wording). `TestServeHTTP_LargeBatchACKsWellUnder3s` additionally covers a 2000-event VCAPS batch. Updated 2026-08-08 — this row previously said no test asserts the response is written before `Enqueue` runs; that stopped being true when these tests landed. |

### Semantic Mapping

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| MAP-01 | CEPP_CREATE_FILE / CEPP_CREATE_DIRECTORY → EventID 4663, WriteData mask | Delivered | `pkg/mapper/mapper_test.go::TestMapEventID` |
| MAP-02 | CEPP_FILE_READ → EventID 4663, ReadData mask | Delivered | `pkg/mapper/mapper_test.go::TestMapEventID` |
| MAP-03 | CEPP_FILE_WRITE → EventID 4663, WriteData mask | Delivered | `pkg/mapper/mapper_test.go::TestMapEventID` |
| MAP-04 | CEPP_DELETE_FILE / CEPP_DELETE_DIRECTORY → EventID 4660, DELETE mask | Delivered | `pkg/mapper/mapper_test.go::TestMapEventID` |
| MAP-05 | CEPP_SETACL_FILE / CEPP_SETACL_DIRECTORY → EventID 4670, WRITE_DAC mask | Delivered | `pkg/mapper/mapper_test.go::TestMapEventID` |
| MAP-06 | CEPP_CLOSE_MODIFIED → EventID 4663 with I/O statistics preserved | Delivered | `pkg/mapper/mapper_test.go::TestMapEventID` + `TestMapFieldPropagation` |

### Output — GELF (cross-platform)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| GELF-01 | Emits valid GELF 1.1 JSON over UDP to a configurable host:port | Delivered | `pkg/evtx/writer_gelf_test.go::TestBuildGELFValidJSON` (payload construction only; no test dials a real UDP socket) |
| GELF-02 | Supports TCP transport | **Unverified** | Nothing dials a real TCP listener for GELF specifically. Unlike syslog (`TestSyslogTCPFraming`, via `net.Pipe`), the GELF TCP dial/write path (`pkg/evtx/writer_gelf.go:87-92`) has no dedicated test. |
| GELF-03 | Payload includes `_event_id`, `_object_name`, `_account_name`, `_account_domain`, `_client_address`, `_access_mask`, `_cepa_event_type` | Delivered | `pkg/evtx/writer_gelf_test.go::TestBuildGELF`, `TestBuildGELFBytesFields` |
| GELF-04 | Reconnects automatically after a lost TCP connection | Delivered | `pkg/evtx/writer_helpers_test.go::TestSendWithRetry_RetrySucceeds` (shared retry helper used by all network writers; not GELF-specific) |

### Output — Win32 (Windows)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| WIN-01 | Writes events to the Windows Application log via ReportEvent API, rendering a real description for event IDs 4660/4663/4670 (not the former placeholder text) | Delivered | `pkg/evtx/writer_windows_test.go::TestEnsureEventSource_PointsAtCurrentExecutable` and `::TestEnsureEventSource_RepointsStaleSource` — the first tests in this repo's history to execute `writer_windows.go` — run in the `windows` CI job (`.github/workflows/ci.yml`), confirmed green in run [31315601780](https://github.com/fjacquet/cee-exporter/actions/runs/31315601780). That job's "Assert Event Viewer renders real text, not the placeholder" step drives the real binary via `-emit-test-events` and, for each of 4660/4663/4670, confirmed in the raw log ("event 4660/4663/4670 renders correctly") that `Get-WinEvent` resolves a non-null message containing the payload and `Get-EventLog` shows no placeholder text. `docs/windows-verification.md` (manual protocol, 2026-08-09, Windows Server 2025) additionally records the actual before text (placeholder, via a source registered the real pre-v5.0 way) and after text (real description) side by side |
| WIN-02 | Registers the "PowerStore-CEPA" event source against the exporter's own executable on first start, and re-registers a source left pointing at `EventCreate.exe` by a pre-v5.0 build | Delivered | Same CI tests as WIN-01. `TestEnsureEventSource_RepointsStaleSource` covers the upgrade case specifically (seeded stale `EventMessageFile`, asserts it gets corrected). `docs/windows-verification.md`'s "upgrade in place" step observed the real path fire against a source this run did not itself create: `win32_source_repointing` logged with `from: "%SystemRoot%\\System32\\EventCreate.exe"`, `to` the real executable path, on Windows Server 2025, 2026-08-09 |

### Output — Multi

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| MULTI-01 | Fans events to all configured backends; one backend's failure doesn't block delivery to others | Delivered | `pkg/evtx/writer_multi_test.go::TestMultiWriter_WriteEvent_AllCalledJoinedErrors` |

### Transport Security

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| TLS-01 | Listener supports HTTPS with a configurable x509 certificate and key | Delivered (partial) | `cmd/cee-exporter/tls_test.go::TestMigrateListenConfig` ("legacy_tls_true_with_cert" case) covers config recognition; no test drives an actual TLS handshake against the listener. |
| TLS-02 | TLS certificate expiry is logged at startup; WARN logged when < 30 days remain | Delivered (partial) | `pkg/server/health_test.go::TestHealth_TLSCertExpiryPopulated` confirms expiry is computed and exposed via `/health`; the startup WARN-log-under-30-days path itself has no test. |

### Observability

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| OBS-01 | GET /health returns JSON with uptime, queue depth, events received/written/dropped, writer type/target | Delivered | `pkg/server/health_test.go::TestHealth_OKWithBaseline`, `TestHealth_DegradedWhenDropped` |
| OBS-02 | Structured JSON logs (slog) include event_type, file_path, client_ip, queue_depth, latency_ms per batch | **Unverified** | Nothing captures/parses slog output to confirm field presence. Correct by inspection (`pkg/server/server.go:87-92,108-113`). |
| OBS-03 | Dropped events (queue overflow) are logged at WARN with running total | Delivered (partial) | `pkg/queue/queue_test.go::TestDropOnFull` verifies the counter increments; the WARN log line itself is not asserted by any test. |

### Quality

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| QUAL-01 | Unit tests cover the CEE XML parser (single event, VCAPS batch, malformed, RegisterRequest detection) | Delivered | `pkg/parser/parser_test.go` |
| QUAL-02 | Unit tests cover the CEPA → WindowsEvent mapper (all 6 event types, field propagation) | Delivered | `pkg/mapper/mapper_test.go` |
| QUAL-03 | Unit tests cover the queue (enqueue, drop on full, drain on stop) | Delivered | `pkg/queue/queue_test.go` |
| QUAL-04 | Unit tests cover GELFWriter payload construction (field presence, GELF 1.1 compliance) | Delivered | `pkg/evtx/writer_gelf_test.go` |
| QUAL-05 | Fix readBody nil ResponseWriter bug (panic on oversized payload) | Delivered | `pkg/server/server_test.go::TestReadBodyOversized` |
| QUAL-06 | `go build ./...` and `go vet`/`golangci-lint` pass with zero warnings on Linux and Windows | Delivered | `make lint` (`golangci-lint run`) + `make build`/`make build-windows`, both run in CI (`.github/workflows/ci.yml`) |

### Build & Distribution

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| BUILD-01 | Makefile with build, build-windows, test, lint, clean targets | Delivered | `Makefile` |
| BUILD-02 | Cross-compiled Windows binary (GOOS=windows GOARCH=amd64) via `make build-windows` | Delivered | `Makefile:build-windows`; cross-built in CI (`.github/workflows/ci.yml`) |

### Documentation

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| DOC-01 | README covers installation, prerequisites, quick-start | Delivered | `README.md`, `docs/operator-guide.md` ("Installation") |
| DOC-02 | README documents all config.toml fields with examples | Delivered | `docs/operator-guide.md` ("Configuration reference") |
| DOC-03 | README covers TLS setup | Delivered | `docs/operator-guide.md` ("TLS / HTTPS setup") |
| DOC-04 | README covers CEPA registration | Delivered | `docs/operator-guide.md` ("Registering with PowerStore CEPA") |

---

## v2.0 Operations & Output Expansion (shipped 2026-03-04)

### Observability (Prometheus)

IDs renumbered `OBS-04`–`OBS-08` — see [ID renumbering](#id-renumbering-notes).

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| OBS-04 | Operator can scrape `cee_events_received_total` from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_AllRequiredMetrics` |
| OBS-05 | Operator can scrape `cee_events_dropped_total` from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_AllRequiredMetrics` |
| OBS-06 | Operator can scrape `cee_queue_depth` from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_AllRequiredMetrics` |
| OBS-07 | Operator can scrape `cee_writer_errors_total` from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_AllRequiredMetrics` |
| OBS-08 | `/metrics` is served on a configurable dedicated port (default 9228) | Delivered (partial) | `cmd/cee-exporter/main.go` (`[metrics] addr`, default `0.0.0.0:9228`); no test exercises a non-default value end-to-end. |
| OBS-10 | Operator can scrape per-publisher CEPA liveness (`cee_cepa_last_request_unix_seconds{remote}`) from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_CEPAPeerMetrics`, `pkg/server/server_test.go::TestServeHTTP_StampsPeerOnEveryPath` |
| OBS-11 | Operator can scrape per-publisher handshake counts (`cee_cepa_registrations_total{remote}`) from `/metrics` | Delivered | `pkg/prometheus/handler_test.go::TestMetricsHandler_CEPAPeerMetrics`, `pkg/server/server_test.go::TestServeHTTP_CountsRegistrationsOnly` |
| OBS-12 | The `remote` label is bounded — port stripped, publishers capped at 64, overflow counted in `cee_cepa_peers_dropped_total` | Delivered | `pkg/metrics/metrics_test.go::TestStore_PeerCap`, `pkg/server/server_test.go::TestServeHTTP_StampsPeerWithoutPort`, `pkg/server/server_test.go::TestPeerHost` |

### Service Deployment

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| DEPLOY-01 | Linux operator is provided a hardened systemd unit file | Delivered | `deploy/systemd/cee-exporter.service` |
| DEPLOY-02 | `systemctl enable --now cee-exporter` auto-starts at boot with auto-restart on failure | Delivered | `deploy/systemd/cee-exporter.service` (`Restart=on-failure`, `RestartSec=5s`, `WantedBy=multi-user.target`); no live-systemd integration test. |
| DEPLOY-03 | `cee-exporter.exe install` registers the daemon with the Service Control Manager | Delivered | The `windows` CI job's "Service install/uninstall round-trip" step, confirmed in run [31318610291](https://github.com/fjacquet/cee-exporter/actions/runs/31318610291)'s raw log: `sc.exe query cee-exporter` immediately after `install` returned `STATE: 1 STOPPED`, and — since the I-3/S-1 fix round — `sc.exe start` followed by polling `Get-Service` reached `Running` before `sc.exe stop` reached `Stopped`. This exercises `svcProgram.Start`/`Stop` in `service_windows.go` for the first time, not just kardianos's SCM registration helpers, and closes the earlier caveat: every native `.exe`/`sc.exe` call in the step is now gated on `$LASTEXITCODE`, so a silent non-zero exit without a terminating PowerShell error would fail the step |
| DEPLOY-04 | `cee-exporter.exe uninstall` removes the daemon from the Service Control Manager | Delivered (partial) | Same step, same run: `.\cee-exporter.exe uninstall` printed `Service "cee-exporter" uninstalled.`, now gated on `$LASTEXITCODE -eq 0` rather than inferred from `$ErrorActionPreference='Stop'` alone. The job still does not re-run `sc.exe query` after uninstall to independently confirm the service is actually gone from the SCM, so removal is confirmed by the tool's own success message and exit code, not by re-querying the SCM afterward |
| DEPLOY-05 | Windows Service auto-restarts after an unexpected crash | **Unverified** | The `windows` CI job now exercises `svcProgram.Start`/`Stop` via `sc.exe start`/`stop` (see DEPLOY-03) — the SCM can start and cleanly stop the service — but nothing simulates a crash or observes the `OnFailure: "restart"` recovery action in `service_windows.go`'s `svcConfig` actually firing. Recovery-action configuration remains correct by inspection only. |

### Output Targets

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| OUT-01 | BeatsWriter forwards events via Lumberjack v2 to Logstash/Graylog | Delivered | `pkg/evtx/writer_beats_test.go::TestBuildBeatsEvent` |
| OUT-02 | BeatsWriter supports TLS | Delivered (partial) | `pkg/evtx/writer_beats_test.go::TestBeatsWriterDialerInjection` exercises the TLS dial path only for an unreachable-address failure; no test confirms a successful TLS handshake. |
| OUT-03 | SyslogWriter forwards RFC 5424 over UDP | Delivered | `pkg/evtx/writer_syslog_test.go::TestBuildSyslog5424` |
| OUT-04 | SyslogWriter forwards RFC 5424 over TCP (octet-counting) | Delivered | `pkg/evtx/writer_syslog_test.go::TestSyslogTCPFraming` (via `net.Pipe`) |
| OUT-05 | BinaryEvtxWriter writes native `.evtx` files on Linux | Delivered | `pkg/evtx/writer_evtx_notwindows_test.go` (`TestBinaryEvtxWriter_WriteClose`, `TestBinaryEvtxWriter_ChunkLayout`, and others) |
| OUT-06 | Generated `.evtx` files open correctly in Windows Event Viewer and parse with forensics tools | **Verified** | `evtx-readback` job (`Get-WinEvent -Path` on `windows-latest`, reading the artifact from `evtx-oracle`) covers opening the file, 3 records, the event ID set {4660, 4663, 4670}, per-record `ToXml()`, and `ObjectName` in the rendered XML; `evtx-oracle` job (python-evtx via `tools/evtx-debug/verify_evtx.py`) is the independent-parser half, and since the `Channel` fix below it also asserts `System/Channel == "Security"` in the generated XML on every push. Description rendering from a saved log, `LogName` resolving to `Security`, and the Event Viewer GUI open/placement question — none CI-gated, since the first two need a host with the event source registered and the third additionally needs an interactive desktop session — are covered by the dated manual protocol in `docs/windows-verification.md` section 5: an initial 2026-08-10 reading on winvm found descriptions not rendering, traced to this project's own `-emit-test-events` fixture leaving `ProviderName` empty rather than a go-evtx or saved-log defect (see `CHANGELOG.md`'s `[5.1.0]` entry); re-measured 2026-08-10 with `ProviderName` set, all three descriptions render correctly. A second, separate defect surfaced the same day: `windowsEventToFields` dropped `WindowsEvent.Channel` (which `pkg/mapper` has set to `"Security"` on every event since v2) before it ever reached go-evtx, so `LogName` resolved empty on every record this exporter has ever written. Fixed by passing `Channel` through with `evtx.DefaultChannel = "Security"` as the fallback (`pkg/evtx/writer_evtx_notwindows_test.go::TestWindowsEventToFields_Channel`); the same saved log now measures `LogName=[Security]` where it measured `LogName=[]` before. The Event Viewer GUI open/placement question was run on 2026-08-10 over an RDP session to winvm (the earlier SSH-only connection had no interactive desktop) against the **released v5.1.0 binary downloaded from GitHub**, not a local build: the saved log opens via Action → Open Saved Log…, lists under Saved Logs → released-v5.1.0 with all three IDs present and `Log Name: Security` in the General pane. A pre-run prediction that the Level/Task Category/Keywords columns would render blank — based on `Get-WinEvent`'s `*DisplayName` properties returning empty strings headlessly — was falsified: Event Viewer supplies its own defaults (`Information`/`None`/`Info`/`None`) for those zero values, because the empty PowerShell properties reflect missing provider metadata to resolve against, not an absent GUI value. |

---

## v3.0 TLS Certificate Automation (Phase 8, shipped 2026-03-04)

IDs renumbered `TLS-03`–`TLS-07` — see [ID renumbering](#id-renumbering-notes).

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| TLS-03 | `tls_mode="acme"` auto-obtains/renews a certificate from Let's Encrypt via TLS-ALPN-01 | **Unverified** | Nothing. Requires a live ACME server. Config recognition only (`cmd/cee-exporter/tls_test.go::TestMigrateListenConfig`, "already_set_acme" case). |
| TLS-04 | `tls_mode="self-signed"` generates a runtime ECDSA certificate — no files, no network | Delivered | `cmd/cee-exporter/tls_test.go::TestBuildSelfSignedTLS` |
| TLS-05 | `tls_mode="manual"` (or legacy `tls=true` + `cert_file`/`key_file`) loads credentials from files | Delivered | `cmd/cee-exporter/tls_test.go::TestMigrateListenConfig` |
| TLS-06 | Existing `tls=true` + `cert_file`/`key_file` configs are auto-migrated to `tls_mode="manual"` | Delivered | `cmd/cee-exporter/tls_test.go::TestMigrateListenConfig` ("legacy_tls_true_with_cert" case) |
| TLS-07 | `config.toml.example` documents all four TLS modes with explanatory comments | Delivered | `config.toml.example` (`tls_mode` block) + `docs/operator-guide.md` ("TLS / HTTPS setup") |

---

## v4.0 Industrialisation (shipped 2026-03-05)

### OSS Module Extraction (EXT)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| EXT-01 | `github.com/fjacquet/go-evtx` exists as an independent Go module with its own repo, CI, README | Delivered (partial) | `go.mod`/`go.sum` (`github.com/fjacquet/go-evtx v0.8.2`). The module's own repository, CI, and README are external and not verifiable from this repo. |
| EXT-02 | go-evtx exposes a low-level `WriteRaw(chunk []byte) error` API | **Unverified from this repo** | cee-exporter does not call `WriteRaw`; go-evtx's own test suite is external, not observable here. |
| EXT-03 | go-evtx exposes a high-level `WriteRecord(eventID int, fields map[string]string) error` API | Delivered | `pkg/evtx/writer_evtx_notwindows.go:49` calls `b.w.WriteRecord(...)` |
| EXT-04 | Existing pkg/evtx EVTX tests are ported to go-evtx and pass in its CI | **Unverified from this repo** | External repository/CI, not observable from here. |
| EXT-05 | cee-exporter's BinaryEvtxWriter becomes a thin adapter over go-evtx | Delivered | `pkg/evtx/writer_evtx_notwindows.go` — a translation-layer adapter delegating to `goevtx.New`/`WriteRecord`/`Close`/`Rotate`, plus the defensive field-truncation and idempotent-`Close` guards Task 4 added on top. (A line count was cited here before this correction; it read 49 at extraction, then went stale at 228 once Task 4 landed — a measurement that rots the moment the file is next edited. Describing the shape instead of counting lines is the fix, not a fresher number.) |

### Durability (FLUSH)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| FLUSH-01 | `flush_interval_s` (default 15) triggers periodic `f.Sync()` | Delivered (partial) | `cmd/cee-exporter/validate_test.go::TestValidateOutputConfig` covers config validation; the fsync ticker itself now lives in go-evtx (external, post-EXT-05), not testable from this repo. |
| FLUSH-02 | All buffered events are flushed and fsynced before process exit on graceful shutdown | Delivered | `pkg/queue/queue_test.go::TestDrainOnStop` + `pkg/evtx/writer_evtx_notwindows_test.go::TestBinaryEvtxWriter_WriteClose` |
| FLUSH-03 | `/metrics` exposes `cee_last_fsync_unix_seconds` | Delivered | `pkg/metrics/metrics_test.go::TestStore_LastFsyncUnix` + `pkg/prometheus/handler_test.go::TestMetricsHandler_AllRequiredMetrics` |

### EVTX Correctness (EVTX)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| EVTX-01 | BinaryEvtxWriter writes all events regardless of session length (fix `flushChunkLocked()` stub silently dropping events beyond ~2,400/session) | Moved | The fix and its implementation now live in go-evtx (external, post-EXT-05 extraction). Nothing to verify in this repo today. |

### File Rotation (ROT)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| ROT-01 | `max_file_size_mb` rotates the active `.evtx` file when it reaches that size | Delivered (partial) | Config validated by `cmd/cee-exporter/validate_test.go::TestValidateOutputConfig`; the rotation algorithm itself is now implemented in go-evtx (external, unverified from here). |
| ROT-02 | `max_file_count` keeps only the N most recent archive files | Delivered (partial) | Same as ROT-01. |
| ROT-03 | `rotation_interval_h` rotates on a fixed schedule | Delivered (partial) | Same as ROT-01. |
| ROT-04 | SIGHUP triggers an immediate rotation without restart | Delivered (partial) | `pkg/evtx/writer_multi_test.go::TestMultiWriter_Rotate_ForwardsAndSkips` verifies `Rotate()` forwarding; the SIGHUP signal registration itself (`cmd/cee-exporter/sighup_notwindows.go`) has no test. |

### Configuration (CFG)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| CFG-01 | All flush/rotation parameters configurable in `[output]` with documented zero-value semantics | Delivered | `config.toml.example` |
| CFG-02 | Invalid configuration (e.g. `flush_interval_s = 0`) is rejected at startup with a clear error | Delivered | `cmd/cee-exporter/validate_test.go::TestValidateOutputConfig` |
| CFG-03 | `config.toml.example` documents all four new `[output]` fields | Delivered | `config.toml.example` |

### Architecture & Documentation (ADR)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| ADR-01 | ADR documents owning the flush ticker inside BinaryEvtxWriter (not the queue layer) | Delivered | `docs/adr/ADR-012-flush-ticker-ownership.md` |
| ADR-02 | ADR documents switching from write-on-close to open-handle incremental flush | **Unverified** | `docs/adr/ADR-013-write-on-close-model.md` exists but its own **Status** field still reads "Accepted (superseded by Phase 10 open-handle model when complete)" — it was never updated after Phase 10 shipped, and the whole area was later overtaken again by the go-evtx extraction (ADR-014). The decision was made and shipped; the record documenting it was not kept current. Documentation debt, not a code gap. |

---

## Deferred / Future Requirements (not yet built)

| ID | Requirement | Status | Verified by |
|----|-------------|--------|-------------|
| OBS-F01 | Prometheus alerting-threshold docs/examples for dropped events | Deferred | N/A — not implemented |
| OBS-F02 | `sd_notify READY=1` for `Type=notify` systemd units | Deferred | N/A — not implemented |
| OBS-09 | `cee_rotation_total` counter for tracking rotation events | Deferred | N/A — not implemented (renumbered from v5 draft `OBS-01`; see [ID renumbering](#id-renumbering-notes)) |
| OUT-F01 | BinaryEvtxWriter cross-event template sharing for reduced file size | Deferred | N/A — not implemented |
| OUT-F02 | BeatsWriter AsyncClient with batching | Deferred | N/A — not implemented |
| OUT-F03 | Syslog TLS transport (RFC 5425) | Deferred | N/A — not implemented |
| EVTX-02 | Full multi-chunk EVTX file support | Deferred | N/A — not implemented |
| EVTX-03 | Startup repair pass for partial-chunk files left by a crash | Deferred | N/A — not implemented |
| ROT-F01 | Compression of rotated `.evtx` files | Deferred | N/A — blocked on forensics tool support |
| TLS-F01 | DNS-01 ACME challenge via go-acme/lego (air-gapped / private-network ACME) | Deferred | N/A — not implemented |
| TLS-F02 | Let's Encrypt staging-URL support via a dedicated config flag | Deferred | N/A — not implemented. An earlier draft documented that flag by name as if it already existed; the 2026-08-08 audit found and removed that phantom-key claim (see the `no-phantom-config` job in `.github/workflows/docs-lint.yml`, and the correction banner in [ADR-011](adr/ADR-011-tls-certificate-automation.md)). |

---

## ID renumbering notes

The three source `REQUIREMENTS.md` files reused some prefixes across
milestones for unrelated requirements. This document keeps the original IDs
where they don't collide, and renumbers the later, colliding set — continuing
the same prefix's sequence rather than inventing a new one — so every ID in
this document is unique. The mapping:

| Prefix | Original ID (source doc) | Original meaning | ID used here |
|---|---|---|---|
| OBS | `OBS-01`..`OBS-03` (`.planning/milestones/v1.0-REQUIREMENTS.md`) | `/health` JSON, structured logs, dropped-event WARN | `OBS-01`..`OBS-03` (unchanged) |
| OBS | `OBS-01`..`OBS-05` (v2 section, `.planning/milestones/v3.0-REQUIREMENTS.md`) | Prometheus `/metrics` counters/gauge, port config | `OBS-04`..`OBS-08` |
| OBS | `OBS-01` (v5 section, `.planning/milestones/v4.0-REQUIREMENTS.md`) | `cee_rotation_total` counter (deferred) | `OBS-09` |
| TLS | `TLS-01`, `TLS-02` (`.planning/milestones/v1.0-REQUIREMENTS.md`) | HTTPS support, cert expiry logging | `TLS-01`, `TLS-02` (unchanged) |
| TLS | `TLS-01`..`TLS-05` (v3 section, `.planning/milestones/v3.0-REQUIREMENTS.md`) | ACME, self-signed, manual, migration, docs | `TLS-03`..`TLS-07` |

`docs/PRD.md`'s own inline table (written before this consolidation) already
worked around part of this collision ad hoc: it used `OBS-04` for the
metrics-port item (this document's `OBS-08`) and `TLS-03`/`TLS-04` for the
ACME/self-signed items (this document's `TLS-03`/`TLS-04` — those two happen
to agree). The PRD table was not rewritten as part of this consolidation
(out of scope for the change that produced this document); this document is
the canonical numbering going forward.

Separately, `.planning/milestones/v1.0-REQUIREMENTS.md` contains a **draft**
"v2 Requirements" section (`EVTX-01`, `EVTX-02`, `BEATS-01`, `OPS-01`..`OPS-03`)
written before Phase 4-8 planning happened. It was entirely superseded by the
real v2.0 milestone requirements (`OUT-01`..`OUT-06`, `DEPLOY-01`..`DEPLOY-05`,
`OBS-04`..`OBS-08` above) under different names, and is not carried into this
table — it is preserved only in the archived source file for provenance.
