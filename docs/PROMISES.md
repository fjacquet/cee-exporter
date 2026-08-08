# Promises

Every user-facing claim this project makes, and the check that keeps it
true. A claim with no verifying job is either deleted or labelled
**Unverified** — it is never left to look proven. A claim checked only by a
human running a command by hand, not by CI, is labelled **Verified (manual)**
naming that step. A claim where part of it is genuinely tested and part is
not is labelled **Verified (partial)**, with the "Verified by" column stating
exactly which part.

New claims arrive with their job. That is the rule this file exists to
enforce. Claims below are drawn from `README.md`, `docs/index.md`, and
`docs/PRD.md` (including its Success Metrics section) as they stood on
2026-08-08. Every "Verified by" test name below was opened and read before
being cited here — see `docs/requirements.md` for the fuller per-requirement
traceability this table draws on.

## CEPA protocol

| Claim | Where stated | Verified by | Status |
|---|---|---|---|
| RegisterRequest handshake returns HTTP 200 with a strictly empty body | `README.md`, `docs/PRD.md` CEPA-01 | `pkg/server/server_test.go::TestServeHTTP_RegisterRequest_EmptyBody` asserts the response body is exactly zero bytes, not merely visually empty (CI `ci` job, `make test`) | Verified |
| The handler ACKs the heartbeat within the 3-second CEPA budget | `README.md`, `docs/PRD.md` NFR | The handler ACKs without waiting for the writer; verified by `TestServeHTTP_ACKsBeforeQueueWork`, which proves `ServeHTTP` returns a written 200 while the queued write is provably still in progress. `TestServeHTTP_LargeBatchACKsWellUnder3s` additionally shows a 2000-event VCAPS batch ACKs in ~1s under `-race`, well inside the 3s limit (CI `ci` job, `make test`) | Verified |
| Malformed/non-RegisterRequest bodies still get a 200, not a 4xx/5xx (CEPA must never be told the endpoint is unreachable) | `pkg/server/server.go` behaviour, relied on by the protocol note in `README.md`/`docs/index.md` | `pkg/server/server_test.go::TestServeHTTP_ParseErrorStillACKs` | Verified |
| VCAPS bulk batch payloads (`<EventBatch>`) are parsed | `docs/PRD.md` CEPA-04 | `pkg/parser/parser_test.go::TestParse` ("vcaps_batch_two_events" case) | Verified |
| Heartbeat latency is bounded, not just "ACK ordering is correct" (CEPA-02) | `docs/PRD.md` NFR | Nothing measures wall-clock response latency against the 3s limit directly — the ordering guarantee above is tested, not the latency number itself | **Unverified** — do not treat CEPA-02 as closed by the ACK-ordering tests above; see `docs/requirements.md` |

## Output backends

| Claim | Where stated | Verified by | Status |
|---|---|---|---|
| GELF 1.1 JSON output over UDP or TCP → Graylog | `README.md`, `docs/index.md` | `pkg/evtx/writer_gelf_test.go` verifies payload construction and GELF 1.1 field validity only — no test dials a real UDP or TCP socket, and `writer_helpers_test.go`'s `sendWithRetry` tests use a fake closure, not `GELFWriter.connect()`/`send()` | **Verified (partial)** — message format is verified; on-the-wire UDP/TCP delivery is not |
| RFC 5424 syslog output over UDP or TCP | `README.md`, `docs/index.md` | `pkg/evtx/writer_syslog_test.go::TestBuildSyslog5424` (RFC 5424 message format) and `TestSyslogTCPFraming` (RFC 6587 octet-counting framing verified against a real `net.Conn` via `net.Pipe`, exercising the writer's actual `send()` method) | **Verified (partial)** — TCP framing is exercised against a real connection; UDP is checked for message format only, not dialed |
| Beats/Lumberjack v2 output to Logstash/Graylog, with optional TLS | `README.md`, `docs/index.md` | `pkg/evtx/writer_beats_test.go::TestBuildBeatsEvent` (event field mapping) and `TestBeatsWriterDialerInjection` (both the plain and TLS `dial()` paths return an error against an unreachable address) | **Verified (partial)** — event mapping is verified and the dial-failure path is exercised; no test performs a successful Lumberjack handshake or send |
| Native `.evtx` files written on non-Windows platforms | `README.md`, `docs/index.md`, `docs/PRD.md` OUT-05 | `pkg/evtx/writer_evtx_notwindows_test.go` (`TestBinaryEvtxWriter_WriteClose`, `TestBinaryEvtxWriter_ChunkLayout`, and others) | Verified |
| Generated `.evtx` files open correctly in Windows Event Viewer and parse with forensics tools | `docs/PRD.md` OUT-06, Success Metrics | Nothing. No Windows Event Viewer available in CI or this environment | **Unverified** — tracked as go-evtx F6 |
| Win32 EventLog writer registers a "PowerStore-CEPA" source and writes via `ReportEvent` | `README.md`, `docs/index.md` | Nothing. `pkg/evtx/writer_windows.go` has no `_test.go` counterpart; CI cross-compiles Windows-tagged files but never executes them (no Windows runner) | **Unverified** |
| Windows Event Log entries render with the real field text, so SIEM content packs for 4663/4660/4670 work | `pkg/evtx/writer_windows.go` (previously) | Nothing — and by inspection this is currently **false**: `InstallAsEventCreate` points `EventMessageFile` at `EventCreate.exe`, which only carries message text for IDs 1-1000, so 4660/4663/4670 render as "The description for Event ID N ... cannot be found" | **Unverified — currently false**; tracked for a v5.0 message-resource fix (`docs/superpowers/specs/2026-08-08-promise-remediation-design.md` section V1) |
| Multi-target fan-out: one backend's failure doesn't block delivery to others | `README.md`, `docs/index.md` | `pkg/evtx/writer_multi_test.go::TestMultiWriter_WriteEvent_AllCalledJoinedErrors` | Verified |
| SIGHUP triggers immediate EVTX rotation on non-Windows platforms | `docs/operator-guide.md` | `cmd/cee-exporter/sighup_notwindows.go` has no dedicated test of the signal registration itself; `pkg/evtx/writer_multi_test.go::TestMultiWriter_Rotate_ForwardsAndSkips` verifies the underlying `Rotate()` forwarding it calls | **Verified (partial)** — the rotation mechanism is tested; the SIGHUP wiring itself is not |

## TLS

| Claim | Where stated | Verified by | Status |
|---|---|---|---|
| `tls_mode="self-signed"` generates a runtime ECDSA certificate, no files or network needed | `README.md`, `docs/operator-guide.md` | `cmd/cee-exporter/tls_test.go::TestBuildSelfSignedTLS` | Verified |
| `tls_mode="manual"` (or legacy `tls=true`+`cert_file`/`key_file`) loads certificates from files, and the legacy form auto-migrates to `tls_mode="manual"` | `README.md`, `docs/operator-guide.md` | `cmd/cee-exporter/tls_test.go::TestMigrateListenConfig` covers config recognition and the legacy-field migration; no test drives an actual TLS handshake against the listener | **Verified (partial)** |
| `tls_mode="acme"` auto-obtains and renews a certificate from Let's Encrypt via TLS-ALPN-01 | `README.md`, `docs/operator-guide.md`, `docs/PRD.md` TLS-03 | `cmd/cee-exporter/tls_test.go::TestMigrateListenConfig` ("already_set_acme" case) covers config recognition only — nothing exercises a real ACME exchange, which needs a live ACME server | **Unverified** for the actual certificate issuance/renewal; config wiring only |
| TLS certificate expiry is surfaced via `/health`, with a startup warning under 30 days remaining | `README.md` | `pkg/server/health_test.go::TestHealth_TLSCertExpiryPopulated` confirms expiry is computed and exposed via `/health`; the startup WARN-log-under-30-days code path itself has no test | **Verified (partial)** |

## Observability

| Claim | Where stated | Verified by | Status |
|---|---|---|---|
| `/metrics` exposes `cee_events_received_total`, `cee_events_written_total`, `cee_events_dropped_total`, `cee_events_truncated_total`, `cee_writer_errors_total`, `cee_queue_depth`, `cee_last_fsync_unix_seconds`, and `cee_build_info` | `README.md`, `docs/index.md`, `docs/operator-guide.md`, `docs/PRD.md` Success Metrics | `pkg/prometheus/handler_test.go::TestMetricsHandler_AllRequiredMetrics` (the first seven) and `TestBuildInfoMetric` (`cee_build_info` with `version`/`go_version` labels), and confirms Go runtime metrics are absent from the private registry | Verified |
| `/metrics` is served on a dedicated, configurable port (default 9228) | `README.md`, `docs/index.md` | The default is set in `cmd/cee-exporter/main.go` (`MetricsConfig.Addr = "0.0.0.0:9228"`); no test exercises the actual port binding or a non-default value end-to-end | **Verified (partial)** — metric content is tested; the port/binding itself is not |
| `GET /health` returns JSON with uptime, queue depth, event counters, and writer type/target; HTTP 503 when degraded | `README.md`, `docs/operator-guide.md` | `pkg/server/health_test.go::TestHealth_OKWithBaseline`, `TestHealth_DegradedWhenDropped` | Verified |
| Structured JSON logging (`slog`) includes event/queue/latency fields on every batch | `README.md` | Nothing captures/parses `slog` output to confirm field presence; correct by inspection of `pkg/server/server.go` | **Unverified** |
| Dropped events (queue overflow) are logged at WARN with a running total | `README.md` ("async queue"), `docs/operator-guide.md` | `pkg/queue/queue_test.go::TestDropOnFull` verifies the counter increments; the WARN log line itself is not asserted by any test | **Verified (partial)** |
| The async queue ACKs the HTTP request immediately and processes events in the background | `README.md` | Covered by the CEPA-protocol ACK-ordering row above; queue mechanics (enqueue, drop-on-full, drain-on-stop) also covered by `pkg/queue/queue_test.go` | Verified |

## Build, packaging, and distribution

| Claim | Where stated | Verified by | Status |
|---|---|---|---|
| Requires Go 1.26.5 | `README.md`, `docs/index.md`, `docs/operator-guide.md` | `go.mod:3` (`go 1.26.5`); the Go toolchain itself refuses to build against an older `go` if `GOTOOLCHAIN=local` and the installed version is too old (verified directly for the previous `golang:1.24-alpine` builder image during Task 3 — it failed with `go: go.mod requires go >= 1.26.5`) | Verified |
| Single static binary, `CGO_ENABLED=0`, no external runtime dependencies | `README.md` | `CGO_ENABLED=0` is set on every binary-producing path (`Makefile` `build-linux`/`build-windows`/`build-darwin`, `Dockerfile`, `.goreleaser.yaml`) — a Go build with CGO disabled cannot link a dynamic dependency. No CI step runs `ldd`/`file` against a built binary to confirm this directly | **Verified (partial)** — guaranteed by build configuration; not confirmed by inspecting an actual built artifact |
| Build-stamped version reaches the running binary (`-X main.version=...`) and appears in the `cee_build_info` metric, replacing the previous hardcoded `"1.0.0"` | `README.md`, `docs/PRD.md` | `cmd/cee-exporter/version_test.go` (`TestVersion_DefaultIsDev`, `TestVersion_NotHardcodedRelease`) only asserts the *unstamped* default is `"dev"` and isn't a hardcoded release string — no test builds a binary with `-ldflags` and inspects it. **Verified (manual):** `make build-darwin && ./cee-exporter-darwin -config config.toml` logged `version` as a real `git describe` string, and `curl :9228/metrics \| grep cee_build_info` showed the same version label (Task 2 verification, 2026-08-08) | **Verified (manual)** |
| Releases build for Linux, Windows, and macOS on amd64 and arm64 | `docs/index.md` | `.goreleaser.yaml` build matrix; real tagged releases (`v4.1`, `v4.1.1`, `v4.1.2`) have completed successfully with this matrix (`gh run list --workflow=release.yml`) | Verified |
| Docker image published to `ghcr.io/fjacquet/cee-exporter` (multi-arch `linux/amd64`+`linux/arm64`) as part of the release | `README.md` | `.goreleaser.yaml` `dockers_v2:` block, invoked by `release.yml` on a `v*` tag. This mechanism is new on this branch and has never run as part of a real release. Task 3 verified the `Dockerfile.goreleaser` context resolution and image runtime behaviour by hand-assembling the same `<goos>/<goarch>/<binary>` layout goreleaser uses and building/running both architectures directly — both produced a working, correctly version-stamped image. What that did **not** prove is goreleaser's own orchestration of `dockers_v2` (tag templating, manifest assembly, `extra_files` staging) | **Verified (manual)** for the image content and Dockerfile logic; the actual `goreleaser`-driven publish is **unverified** until the `v4.1.3` tag runs it |
| `LICENSE` (MIT) is present at the repo root and inside every release archive | `README.md` badge | `test -f LICENSE` (present); `.goreleaser.yaml` `archives.files` lists `README.md` and `LICENSE` | Verified |
| Windows Service (`cee-exporter.exe install`) registers with the SCM and auto-restarts after a crash | `README.md`, `docs/operator-guide.md`, `docs/PRD.md` DEPLOY-03/04/05 | Nothing. No Windows CI runner; recovery-action configuration is correct by inspection of `cmd/cee-exporter/service_windows.go` only | **Unverified** |
| `go test ./...` passes with zero failures | `docs/PRD.md` Success Metrics | Linux: CI `ci` job runs `make test` (`go test -race ./...`) on every push, currently green | **Verified** on Linux — **Unverified** on Windows (no Windows CI runner; see the row above) |
| Linux systemd unit starts cleanly and is self-sufficient (no manual directory setup), including the `tls_mode="acme"` default cache path | `docs/operator-guide.md`, `deploy/systemd/cee-exporter.service` | No automated CI test — verified by hand for this release: booted the unit under real `systemd` (PID 1, `DynamicUser=yes`, `ProtectSystem=strict`) in a container with `tls_mode="acme"` configured. `systemctl status` showed `active (running)`, the ACME challenge listener bound `:443`, and `cee_exporter_ready` logged. Entered the running service's own mount namespace (`nsenter --target $MAINPID --mount`) and, as its exact dynamic UID, successfully created and wrote `/var/cache/cee-exporter/acme/test.pem` — confirming `CacheDirectory=cee-exporter` makes the default `acme_cache_dir` writable with zero operator action, closing the gap Amendment A2 identified | **Verified (manual)** |

## Documentation and process

| Claim | Where stated | Verified by | Status |
|---|---|---|---|
| An operator can follow the README quickstart and see events in Graylog within 15 minutes | `docs/PRD.md` Success Metrics | Nothing — no timed or automated check of this exists | **Unverified** |
| `docs/PRD.md` links a `docs/requirements.md` that exists; the retired `.planning/` directory no longer sits at the repo root | Exit criteria for this release | `test -f docs/requirements.md`, `test -d docs/archive/planning` | Verified |
| Documented config keys always exist in code (no phantom keys like the former `type = "binary-evtx"` or `acme_staging`) | `docs/operator-guide.md`, `config.toml.example` | `.github/workflows/docs-lint.yml` `no-phantom-config` job, run on every push/PR | Verified |
| `make docs` succeeds under `--strict` (no broken internal links) | Development workflow, `CLAUDE.md` | `make docs` (run locally as part of this release's verification; not yet re-run by CI after mkdocs.yml's `copyright`/nav changes in this release, since `docs.yml` only triggers on push to `main`) | **Verified (manual)** — will also run automatically as `docs.yml`'s CI check on merge to `main` |
