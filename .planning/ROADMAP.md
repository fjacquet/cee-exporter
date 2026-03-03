# Roadmap: cee-exporter

## Milestones

- ✅ **v1.0 MVP** — Phases 1-3 (shipped 2026-03-03) — see [milestones/v1.0-ROADMAP.md](milestones/v1.0-ROADMAP.md)
- 🚧 **v2.0 Operations & Output Expansion** — Phases 4-7 (in progress)

## Phases

<details>
<summary>✅ v1.0 MVP (Phases 1-3) — SHIPPED 2026-03-03</summary>

- [x] Phase 1: Quality (3/3 plans) — completed 2026-03-02
- [x] Phase 2: Build (1/1 plan) — completed 2026-03-02
- [x] Phase 3: Documentation (2/2 plans) — completed 2026-03-03

</details>

### v2.0 Operations & Output Expansion (In Progress)

**Milestone Goal:** Make cee-exporter production-deployable as a managed service on Linux and Windows, add Prometheus observability, and expand SIEM output targets to cover Beats, syslog, and native .evtx on Linux.

- [x] **Phase 4: Observability & Linux Service** - Prometheus /metrics endpoint on port 9228 plus hardened systemd unit file (completed 2026-03-03)
- [x] **Phase 5: Windows Service** - SCM-managed service registration via install/uninstall subcommands (completed 2026-03-03)
- [x] **Phase 6: SIEM Writers** - SyslogWriter (RFC 5424 UDP/TCP) and BeatsWriter (Lumberjack v2) output targets (completed 2026-03-03)
- [ ] **Phase 7: BinaryEvtxWriter** - Pure-Go BinXML .evtx file writer for Linux hosts

## Phase Details

### Phase 4: Observability & Linux Service

**Goal**: Operators can scrape live telemetry from the daemon and deploy it as a managed Linux service
**Depends on**: Phase 3 (v1.0 complete)
**Requirements**: OBS-01, OBS-02, OBS-03, OBS-04, OBS-05, DEPLOY-01, DEPLOY-02
**Success Criteria** (what must be TRUE):

  1. Operator runs `curl http://host:9228/metrics` and receives Prometheus text with `cee_events_received_total`, `cee_events_dropped_total`, `cee_queue_depth`, and `cee_writer_errors_total` counters
  2. Operator changes the metrics listen address in config.toml and the /metrics endpoint binds to the new port without touching the CEPA port 12228
  3. Operator copies `deploy/systemd/cee-exporter.service` to `/etc/systemd/system/` and runs `systemctl enable --now cee-exporter` and the daemon starts and stays running
  4. Operator stops the daemon process unexpectedly (kill -9) and systemd restarts it automatically within 5 seconds
  5. Prometheus scrape of /metrics returns only low-cardinality labels (event_id, writer, status) — no per-file or per-user label explosion
**Plans**: 3 plans

Plans:

- [ ] 04-01-PLAN.md — Add prometheus/client_golang dependency, implement pkg/prometheus/handler.go, write unit test
- [ ] 04-02-PLAN.md — Create hardened systemd unit file and Makefile install-systemd target
- [ ] 04-03-PLAN.md — Refactor main() to run(), wire metrics goroutine, create service_notwindows.go shim

### Phase 5: Windows Service

**Goal**: Windows operators can register, start, and recover cee-exporter as a native SCM-managed service without external tools
**Depends on**: Phase 4
**Requirements**: DEPLOY-03, DEPLOY-04, DEPLOY-05
**Success Criteria** (what must be TRUE):

  1. Operator runs `cee-exporter.exe install` on Windows and the service appears in the Services snap-in (services.msc) with Automatic Delayed Start
  2. Operator runs `cee-exporter.exe uninstall` and the service is removed from SCM with no leftover registry entries
  3. The Windows Service starts within the SCM 30-second window (StartPending sent before Go runtime init completes) and reports Running status in services.msc
  4. If cee-exporter.exe crashes, Windows SCM restarts it automatically according to the recovery actions configured at install time
**Plans**: 3 plans

Plans:

- [ ] 05-01-PLAN.md — Add kardianos/service dependency, refactor run() to accept context.Context, update service shims
- [ ] 05-02-PLAN.md — TDD: parseCfgPath helper (pure function, no build tag, Linux CI testable)
- [ ] 05-03-PLAN.md — Full service_windows.go SCM wrapper: install/uninstall dispatch, Stop() bridge, recovery actions

### Phase 6: SIEM Writers

**Goal**: Operators can forward audit events to any syslog-compatible receiver and to Logstash or Graylog Beats Input via Lumberjack v2
**Depends on**: Phase 4
**Requirements**: OUT-01, OUT-02, OUT-03, OUT-04
**Success Criteria** (what must be TRUE):

  1. Operator sets `type = "syslog"` with `syslog_protocol = "udp"` in config.toml and audit events arrive at the syslog receiver as valid RFC 5424 messages with structured-data containing audit fields
  2. Operator sets `syslog_protocol = "tcp"` and audit events flow over a persistent TCP connection to the syslog server
  3. Operator sets `type = "beats"` with Logstash or Graylog Beats Input address and audit events arrive as Lumberjack v2 frames readable by the receiver
  4. Operator enables `beats_tls = true` and the Beats connection is established over TLS — plaintext is refused
**Plans**: 3 plans

Plans:

- [ ] 06-01-PLAN.md — TDD: SyslogWriter (crewjam/rfc5424 + net.Conn, UDP datagram + TCP octet-counting framing)
- [ ] 06-02-PLAN.md — TDD: BeatsWriter (go-lumber SyncClient + TLS dialer via SyncDialWith)
- [ ] 06-03-PLAN.md — Wire SyslogWriter and BeatsWriter into main.go OutputConfig + buildWriter factory

### Phase 7: BinaryEvtxWriter

**Goal**: Linux operators can configure cee-exporter to write native .evtx files that open correctly in Windows Event Viewer and forensics tools
**Depends on**: Phase 4
**Requirements**: OUT-05, OUT-06
**Success Criteria** (what must be TRUE):

  1. Operator sets `type = "evtx"` with an output file path on a Linux host and the daemon writes a .evtx file without crashing or emitting errors
  2. The .evtx file produced on Linux is copied to a Windows machine and opens in Event Viewer showing correct EventIDs (4663, 4660, 4670) with readable audit fields
  3. A forensics tool (Splunk, Elastic Agent, or the `0xrawsec/golang-evtx` parser) reads the file and extracts event records with correct timestamps and subject/object fields
**Plans**: 2 plans

Plans:

- [ ] 07-01-PLAN.md — EVTX binary format helpers: toFILETIME, encodeUTF16LE, buildFileHeader, patchChunkCRC, wrapEventRecord + unit tests
- [ ] 07-02-PLAN.md — BinaryEvtxWriter full implementation replacing stub, static BinXML template, round-trip tests

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Quality | v1.0 | 3/3 | Complete | 2026-03-02 |
| 2. Build | v1.0 | 1/1 | Complete | 2026-03-02 |
| 3. Documentation | v1.0 | 2/2 | Complete | 2026-03-03 |
| 4. Observability & Linux Service | v2.0 | 3/3 | Complete | 2026-03-03 |
| 5. Windows Service | 3/3 | Complete   | 2026-03-03 | - |
| 6. SIEM Writers | 3/3 | Complete   | 2026-03-03 | - |
| 7. BinaryEvtxWriter | 1/2 | In Progress|  | - |
