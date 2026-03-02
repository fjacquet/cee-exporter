# Requirements: cee-exporter

**Defined:** 2026-03-02
**Core Value:** Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## v1.0 Requirements

### CEPA Protocol

- [x] **CEPA-01**: Listener completes the RegisterRequest handshake with HTTP 200 OK and strictly empty body
- [x] **CEPA-02**: Listener responds to heartbeat PUT requests within 3 seconds to prevent SDNAS_CEPP_ALL_SERVERS_UNREACHABLE alerts
- [x] **CEPA-03**: Listener parses single-event CEE XML payloads into CEPAEvent structs
- [x] **CEPA-04**: Listener parses VCAPS bulk batch XML payloads (EventBatch containing multiple CEEEvents)
- [x] **CEPA-05**: HTTP handler ACKs immediately and delegates event processing to an async queue

### Semantic Mapping

- [x] **MAP-01**: CEPP_CREATE_FILE / CEPP_CREATE_DIRECTORY maps to Windows EventID 4663 with WriteData access mask
- [x] **MAP-02**: CEPP_FILE_READ maps to Windows EventID 4663 with ReadData access mask
- [x] **MAP-03**: CEPP_FILE_WRITE maps to Windows EventID 4663 with WriteData access mask
- [x] **MAP-04**: CEPP_DELETE_FILE / CEPP_DELETE_DIRECTORY maps to Windows EventID 4660 with DELETE access mask
- [x] **MAP-05**: CEPP_SETACL_FILE / CEPP_SETACL_DIRECTORY maps to Windows EventID 4670 with WRITE_DAC access mask
- [x] **MAP-06**: CEPP_CLOSE_MODIFIED maps to Windows EventID 4663 with I/O statistics (bytesRead, bytesWritten) preserved

### Output — GELF (Cross-Platform)

- [x] **GELF-01**: GELFWriter emits valid GELF 1.1 JSON payloads over UDP to a configurable host:port
- [x] **GELF-02**: GELFWriter supports TCP transport (in addition to UDP)
- [x] **GELF-03**: GELF payload includes _event_id, _object_name, _account_name, _account_domain, _client_address, _access_mask, _cepa_event_type fields
- [x] **GELF-04**: GELFWriter reconnects automatically after a lost TCP connection

### Output — Win32 (Windows)

- [x] **WIN-01**: Win32EventLogWriter writes events to the Windows Application log via ReportEvent API
- [x] **WIN-02**: Win32EventLogWriter registers the "PowerStore-CEPA" event source on first start

### Output — Multi

- [x] **MULTI-01**: MultiWriter fans events to all configured backends; a failure in one backend does not prevent delivery to others

### Transport Security

- [x] **TLS-01**: Listener supports HTTPS with a configurable x509 certificate and key
- [x] **TLS-02**: TLS certificate expiry is logged at startup; WARN logged when < 30 days remain

### Observability

- [x] **OBS-01**: GET /health returns JSON with uptime, queue depth, events received/written/dropped, writer type/target
- [x] **OBS-02**: Structured JSON logs (slog) include event_type, file_path, client_ip, queue_depth, latency_ms per received batch
- [x] **OBS-03**: Dropped events (queue overflow) are logged at WARN with running total

### Quality

- [ ] **QUAL-01**: Unit tests cover CEE XML parser (single event, VCAPS batch, malformed input, RegisterRequest detection)
- [ ] **QUAL-02**: Unit tests cover CEPA → WindowsEvent mapper (all 6 event types, field propagation)
- [ ] **QUAL-03**: Unit tests cover queue (enqueue, drop on full, drain on stop)
- [ ] **QUAL-04**: Unit tests cover GELFWriter payload construction (field presence, GELF 1.1 compliance)
- [ ] **QUAL-05**: Fix readBody nil ResponseWriter bug (panic on oversized payload)
- [x] **QUAL-06**: `go build ./...` and `go vet ./...` pass with zero warnings on Linux and Windows targets

### Build & Distribution

- [ ] **BUILD-01**: Makefile with `build`, `build-windows`, `test`, `lint`, `clean` targets
- [ ] **BUILD-02**: Cross-compiled Windows binary (`GOOS=windows GOARCH=amd64`) produced by `make build-windows`

### Documentation

- [ ] **DOC-01**: README covers installation, prerequisites, quick-start (GELF → Graylog)
- [ ] **DOC-02**: README documents all config.toml fields with examples
- [ ] **DOC-03**: README covers TLS setup (self-signed cert generation example)
- [ ] **DOC-04**: README covers CEPA registration (Dell PowerStore Event Publishing Pool configuration)

## v2 Requirements

### Output — Binary EVTX (Linux)

- **EVTX-01**: Pure-Go BinaryEvtxWriter generates valid binary .evtx files on Linux (BinXML format)
- **EVTX-02**: Generated .evtx files are accepted by Winlogbeat and Windows Event Viewer

### Beats Protocol

- **BEATS-01**: BeatsWriter sends events to Logstash/Graylog Beats Input via Lumberjack v2 protocol

### Operations

- **OPS-01**: Prometheus /metrics endpoint exposes cee_events_received_total, cee_events_dropped_total, cee_queue_depth
- **OPS-02**: Windows Service installer (NSSM or native service registration)
- **OPS-03**: Systemd unit file for Linux deployment

## Out of Scope

| Feature | Reason |
|---------|--------|
| Binary .evtx writer for Linux (v1) | GELF covers Graylog use case; BinXML is 500-1500 LOC with format complexity — deferred to v2 |
| RPC/MSRPC transport | HTTP transport only; RPC is Windows-only and adds significant complexity |
| CAVA antivirus events | Not an audit use case for this project |
| HA load-balancer setup | Operational concern, not code |
| PowerStore AppsON deployment guide | Documentation only, out of code scope |
| Database / message broker | Self-contained binary by design |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| CEPA-01 | Done (pre-roadmap) | Complete |
| CEPA-02 | Done (pre-roadmap) | Complete |
| CEPA-03 | Done (pre-roadmap) | Complete |
| CEPA-04 | Done (pre-roadmap) | Complete |
| CEPA-05 | Done (pre-roadmap) | Complete |
| MAP-01 | Done (pre-roadmap) | Complete |
| MAP-02 | Done (pre-roadmap) | Complete |
| MAP-03 | Done (pre-roadmap) | Complete |
| MAP-04 | Done (pre-roadmap) | Complete |
| MAP-05 | Done (pre-roadmap) | Complete |
| MAP-06 | Done (pre-roadmap) | Complete |
| GELF-01 | Done (pre-roadmap) | Complete |
| GELF-02 | Done (pre-roadmap) | Complete |
| GELF-03 | Done (pre-roadmap) | Complete |
| GELF-04 | Done (pre-roadmap) | Complete |
| WIN-01 | Done (pre-roadmap) | Complete |
| WIN-02 | Done (pre-roadmap) | Complete |
| MULTI-01 | Done (pre-roadmap) | Complete |
| TLS-01 | Done (pre-roadmap) | Complete |
| TLS-02 | Done (pre-roadmap) | Complete |
| OBS-01 | Done (pre-roadmap) | Complete |
| OBS-02 | Done (pre-roadmap) | Complete |
| OBS-03 | Done (pre-roadmap) | Complete |
| QUAL-01 | Phase 1 | Pending |
| QUAL-02 | Phase 1 | Pending |
| QUAL-03 | Phase 1 | Pending |
| QUAL-04 | Phase 1 | Pending |
| QUAL-05 | Phase 1 | Pending |
| QUAL-06 | Done (pre-roadmap) | Complete |
| BUILD-01 | Phase 2 | Pending |
| BUILD-02 | Phase 2 | Pending |
| DOC-01 | Phase 3 | Pending |
| DOC-02 | Phase 3 | Pending |
| DOC-03 | Phase 3 | Pending |
| DOC-04 | Phase 3 | Pending |

**Coverage:**

- v1.0 requirements: 35 total
- Complete (pre-roadmap): 24
- Mapped to Phase 1: 5 (QUAL-01 through QUAL-05)
- Mapped to Phase 2: 2 (BUILD-01, BUILD-02)
- Mapped to Phase 3: 4 (DOC-01 through DOC-04)
- Unmapped: 0 (100% coverage) ✓

---
*Requirements defined: 2026-03-02*
*Last updated: 2026-03-02 — Traceability populated after roadmap creation*
