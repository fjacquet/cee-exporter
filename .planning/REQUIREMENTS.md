# Requirements: cee-exporter

**Defined:** 2026-03-03
**Core Value:** Any SIEM can ingest Dell PowerStore file-system audit events as native Windows EventLog or GELF, from any Linux or Windows host, with no external dependencies beyond the Go binary.

## v2 Requirements

Requirements for the v2.0 Operations & Output Expansion milestone.

### Observability

- [x] **OBS-01**: Operator can scrape `cee_events_received_total` counter from `/metrics` endpoint
- [x] **OBS-02**: Operator can scrape `cee_events_dropped_total` counter from `/metrics` endpoint
- [x] **OBS-03**: Operator can scrape `cee_queue_depth` gauge from `/metrics` endpoint
- [x] **OBS-04**: Operator can scrape `cee_writer_errors_total` counter from `/metrics` endpoint
- [x] **OBS-05**: `/metrics` endpoint is served on a configurable dedicated port (default 9228, separate from CEPA port 12228)

### Service Deployment

- [x] **DEPLOY-01**: Linux operator is provided a hardened systemd unit file for daemon management
- [x] **DEPLOY-02**: Linux operator can `systemctl enable --now cee-exporter` to auto-start at boot with auto-restart on failure
- [x] **DEPLOY-03**: Windows operator can run `cee-exporter.exe install` to register daemon with Service Control Manager
- [x] **DEPLOY-04**: Windows operator can run `cee-exporter.exe uninstall` to remove daemon from Service Control Manager
- [x] **DEPLOY-05**: Windows Service auto-restarts after unexpected crash (recovery actions configured)

### Output Targets

- [x] **OUT-01**: Operator can configure BeatsWriter to forward events to Logstash or Graylog Beats Input via Lumberjack v2 protocol
- [x] **OUT-02**: BeatsWriter supports TLS for encrypted Beats transport
- [x] **OUT-03**: Operator can configure SyslogWriter to forward RFC 5424 structured syslog events over UDP
- [x] **OUT-04**: Operator can configure SyslogWriter to forward RFC 5424 structured syslog events over TCP
- [x] **OUT-05**: Operator can configure BinaryEvtxWriter to write native `.evtx` files on Linux
- [x] **OUT-06**: `.evtx` files generated on Linux open correctly in Windows Event Viewer and can be parsed by forensics tools

## v3 Requirements

Requirements for Phase 8: TLS Certificate Automation with Let's Encrypt.

### TLS Certificate Management

- [x] **TLS-01**: Operator can set `tls_mode="acme"` with `acme_domains` in config.toml and the daemon automatically obtains and renews a TLS certificate from Let's Encrypt via ACME TLS-ALPN-01 challenge on port 443
- [x] **TLS-02**: Operator can set `tls_mode="self-signed"` and the daemon generates a runtime ECDSA certificate at startup — no files, no network access, no external dependencies
- [x] **TLS-03**: Operator can set `tls_mode="manual"` (or use the legacy `tls=true` + `cert_file`/`key_file` config) and the daemon loads TLS credentials from the specified files (backward compatible with pre-Phase-8 configs)
- [x] **TLS-04**: Existing config.toml files with `tls=true` + `cert_file`/`key_file` are automatically migrated to `tls_mode="manual"` behavior without requiring operator changes
- [x] **TLS-05**: config.toml.example documents all four TLS modes (`off`, `manual`, `acme`, `self-signed`) with explanatory comments including the CEPA HTTP-only protocol constraint

## Future Requirements

Features acknowledged but deferred beyond v2.0.

### Observability

- **OBS-F01**: Operator can configure alerting thresholds for dropped events via Prometheus alerting rules (documentation/examples only)
- **OBS-F02**: sd_notify READY=1 integration for `Type=notify` systemd units

### Output Targets

- **OUT-F01**: BinaryEvtxWriter uses cross-event template sharing for reduced file size
- **OUT-F02**: BeatsWriter uses AsyncClient with batching for higher throughput
- **OUT-F03**: Syslog TLS transport (RFC 5425)

### TLS

- **TLS-F01**: DNS-01 ACME challenge via go-acme/lego for air-gapped / private-network ACME (no public port 80/443 needed)
- **TLS-F02**: Let's Encrypt staging URL support via `acme_staging = true` config flag for development environments

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
| DNS-01 ACME via go-acme/lego (Phase 8) | Adds 160+ DNS provider deps; deferred to TLS-F01 |
| Let's Encrypt staging URL (Phase 8) | Operator concern; use acme_cache_dir for separation; deferred to TLS-F02 |

## Traceability

Which phases cover which requirements. Populated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| OBS-01 | Phase 4 | Complete |
| OBS-02 | Phase 4 | Complete |
| OBS-03 | Phase 4 | Complete |
| OBS-04 | Phase 4 | Complete |
| OBS-05 | Phase 4 | Complete |
| DEPLOY-01 | Phase 4 | Complete |
| DEPLOY-02 | Phase 4 | Complete |
| DEPLOY-03 | Phase 5 | Complete |
| DEPLOY-04 | Phase 5 | Complete |
| DEPLOY-05 | Phase 5 | Complete |
| OUT-01 | Phase 6 | Complete |
| OUT-02 | Phase 6 | Complete |
| OUT-03 | Phase 6 | Complete |
| OUT-04 | Phase 6 | Complete |
| OUT-05 | Phase 7 | Complete |
| OUT-06 | Phase 7 | Complete |
| TLS-01 | Phase 8 | Planned |
| TLS-02 | Phase 8 | Planned |
| TLS-03 | Phase 8 | Planned |
| TLS-04 | Phase 8 | Planned |
| TLS-05 | Phase 8 | Planned |

**Coverage:**

- v2 requirements: 16 total — all complete
- v3 requirements (Phase 8): 5 total — 0 complete, 5 planned
- Mapped to phases: 21 (roadmap complete)
- Unmapped: 0

---
*Requirements defined: 2026-03-03*
*Last updated: 2026-03-03 — Phase 8 TLS requirements added (TLS-01 through TLS-05)*
