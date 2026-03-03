# Requirements: cee-exporter

**Defined:** 2026-03-03
**Core Value:** Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## v2 Requirements

Requirements for the v2.0 Operations & Output Expansion milestone.

### Observability

- [ ] **OBS-01**: Operator can scrape `cee_events_received_total` counter from `/metrics` endpoint
- [ ] **OBS-02**: Operator can scrape `cee_events_dropped_total` counter from `/metrics` endpoint
- [ ] **OBS-03**: Operator can scrape `cee_queue_depth` gauge from `/metrics` endpoint
- [ ] **OBS-04**: Operator can scrape `cee_writer_errors_total` counter from `/metrics` endpoint
- [ ] **OBS-05**: `/metrics` endpoint is served on a configurable dedicated port (default 9228, separate from CEPA port 12228)

### Service Deployment

- [ ] **DEPLOY-01**: Linux operator is provided a hardened systemd unit file for daemon management
- [ ] **DEPLOY-02**: Linux operator can `systemctl enable --now cee-exporter` to auto-start at boot with auto-restart on failure
- [ ] **DEPLOY-03**: Windows operator can run `cee-exporter.exe install` to register daemon with Service Control Manager
- [ ] **DEPLOY-04**: Windows operator can run `cee-exporter.exe uninstall` to remove daemon from Service Control Manager
- [ ] **DEPLOY-05**: Windows Service auto-restarts after unexpected crash (recovery actions configured)

### Output Targets

- [ ] **OUT-01**: Operator can configure BeatsWriter to forward events to Logstash or Graylog Beats Input via Lumberjack v2 protocol
- [ ] **OUT-02**: BeatsWriter supports TLS for encrypted Beats transport
- [ ] **OUT-03**: Operator can configure SyslogWriter to forward RFC 5424 structured syslog events over UDP
- [ ] **OUT-04**: Operator can configure SyslogWriter to forward RFC 5424 structured syslog events over TCP
- [ ] **OUT-05**: Operator can configure BinaryEvtxWriter to write native `.evtx` files on Linux
- [ ] **OUT-06**: `.evtx` files generated on Linux open correctly in Windows Event Viewer and can be parsed by forensics tools

## Future Requirements

Features acknowledged but deferred beyond v2.0.

### Observability

- **OBS-F01**: Operator can configure alerting thresholds for dropped events via Prometheus alerting rules (documentation/examples only)
- **OBS-F02**: sd_notify READY=1 integration for `Type=notify` systemd units

### Output Targets

- **OUT-F01**: BinaryEvtxWriter uses cross-event template sharing for reduced file size
- **OUT-F02**: BeatsWriter uses AsyncClient with batching for higher throughput
- **OUT-F03**: Syslog TLS transport (RFC 5425)

## Out of Scope

Explicitly excluded from v2.0. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| MSI installer for Windows Service | Scope explosion; `install`/`uninstall` subcommands provide equivalent value |
| NSSM-based Windows Service | External binary dependency; breaks single-artifact deploy contract |
| Prometheus push gateway | Pull model is correct for a long-running daemon |
| RFC 3164 (legacy BSD syslog) | Unstructured; cannot carry audit SD-PARAMs reliably |
| EVTX chunked streaming (mid-chunk) | Not how the EVTX format works; flush per chunk or on Close |
| Grafana dashboards / alerting rules | Operational concern; not part of the daemon |
| MSI / WiX installer | Scope explosion for v2 |
| macOS platform support | Not a target platform |
| HA / load-balancer setup | Operational concern, not code |

## Traceability

Which phases cover which requirements. Populated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| OBS-01 | — | Pending |
| OBS-02 | — | Pending |
| OBS-03 | — | Pending |
| OBS-04 | — | Pending |
| OBS-05 | — | Pending |
| DEPLOY-01 | — | Pending |
| DEPLOY-02 | — | Pending |
| DEPLOY-03 | — | Pending |
| DEPLOY-04 | — | Pending |
| DEPLOY-05 | — | Pending |
| OUT-01 | — | Pending |
| OUT-02 | — | Pending |
| OUT-03 | — | Pending |
| OUT-04 | — | Pending |
| OUT-05 | — | Pending |
| OUT-06 | — | Pending |

**Coverage:**
- v2 requirements: 16 total
- Mapped to phases: 0 (roadmap pending)
- Unmapped: 16 ⚠️

---
*Requirements defined: 2026-03-03*
*Last updated: 2026-03-03 after initial v2.0 definition*
