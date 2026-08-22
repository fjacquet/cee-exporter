# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Every PowerStore event was stamped with the year 243179022179.**
  `timeStamp` is not a plain second count in the PowerStore dialect: it is a
  packed 64-bit value whose high 32 bits are the epoch second and whose low 32
  bits are a sub-second remainder. Read whole, `0x6a7f7c090008765f` became
  7673988668159719007 seconds. The true event time appeared nowhere in the
  output — verified against a live `audit.evtx`, where its FILETIME occurred
  zero times and the record header carried the write time instead.

  The low word is discarded rather than guessed at: its unit is undocumented
  and the capture could not settle it, because the array replayed one identical
  event throughout. Second resolution is what CEPA promises.

  Unpacking happens before any range check. An upper bound ordered first would
  discard every packed timestamp after 2038-01-19, when the epoch sets bit 63;
  unpacking first also makes the bound unnecessary, since the converted value
  is then always at most `MaxUint32` and cannot wrap negative.

- **NFS events carried no actor at all.** PowerStore publishes NFS events
  (`protocol="1"`) with no `userSid` — NFS has no Windows SID to send — and
  puts the POSIX ids on an `EventExt` child element that the parser did not
  declare, so `encoding/xml` discarded it. Every such record named the file and
  nothing about who touched it. The uid is now rendered `S-1-22-1-<uid>`, the
  form OneFS itself already uses for a POSIX account. A real `userSid` still
  wins where one is sent.

- **`cee_cepa_registrations_total` counted heartbeats as registrations.** It
  was incremented from three call sites — the `RegisterRequest` handshake, the
  PowerScale `CheckFileRequest` action 9, and CEE's `HeartBeatRequest` probe.
  Against a live estate five of six series were heartbeats wearing a
  registration label: four OneFS nodes and a PowerStore Data Mover that had
  never registered. Liveness exchanges are now counted separately as
  `cee_cepa_heartbeats_total`, and the bundled dashboard plots both.

- **The binary evtx writer dropped the client address.** It was parsed, carried
  through `WindowsEvent` and emitted by the syslog, GELF, Beats and Win32
  writers, but go-evtx's `EventData` schema was closed at twelve fields and
  `WriteRecord` ignored the key in silence. It now writes `IpAddress`, the name
  Windows Security auditing uses on 4625 and 5145. **Requires go-evtx v0.9.0.**

## [5.5.0] - 2026-08-22

### Fixed

- **CEE never registered this consumer, so no array ever published.** The
  `RegisterRequest` handshake was answered with HTTP 200 and a deliberately
  empty body, on the strength of a comment in `pkg/server/server.go` citing
  "Dell CEPA documentation". That rule is wrong, and it was the reason two
  separate PowerStore bring-ups ended in "connected, healthy, and silent".

  CEE parses the reply into a `CRegisterResponse` and rejects a body with no
  root element — `Top node is not RegisterResponse` — so registration cannot
  complete, and a consumer that never registers can never be sent events.

  **This IS why the PowerStore deployment saw `CEPP_NOT_FOUND`, and it now
  works.** Verified end to end 2026-08-22: the fix took a live PowerStore from
  `0x16` with 151 discarded events to `0x0` with events reaching the consumer
  and a `.evtx` readable by `Get-WinEvent` on Windows Server 2025. A
  dead-endpoint control appears to exonerate this leg — identical `0x16` either
  way — but that is a false negative, since both cases mean "no partner".
  An unregistered CEE answers array heartbeats `status="0x16"`
  (`VC_ERROR_CEPP_NOT_FOUND`), and the array counted its events missed and
  discarded them without ever putting one on the wire.

  The consumer now returns the document CEE requires:

  ```xml
  <RegisterResponse><EndPoint friendlyName="ceeexporter" guid="…"
    version="1.0" desc="cee-exporter CEPA consumer"/>
    <Filter protocol="0"><EventTypeFilter value="0xFFFFFFFF0000000000000000"/></Filter>
    <Filter protocol="1"><EventTypeFilter value="0xFFFFFFFF0000000000000000"/></Filter>
  </RegisterResponse>
  ```

  encoded UTF-16LE when addressed in UTF-16LE, as the OneFS path already did.
  Configurable under `[cepa]` — `friendly_name`, `guid`, `description`,
  `protocols`, `event_filter`.

  **The response shape alone is not enough** — CEE also demands an identity it
  already knows (`CGuidStore`, see `RegistrationConfig`) and a `hbStatus=0`
  reply to its `<HeartBeatRequest />`. Diagnosing this needs `Debug=63` on the
  CEE side: `Debug`/`Verbose` are a 6-bit mask, and at `1` — or even `9`, which
  prints less than `3` — CEE says nothing about why it refused a partner.

  The shape is not inferred. It is CEE's own template for its built-in
  SplunkHEC proxy, recovered from `libCEPPAPIWrapper.so` in the vendored CEE
  9.2.0.0 rpm, and the rules it is validated against are the failure messages
  in `CEndPoint::Init()` (`libCEPPFilter.so`). Protocol codes `0=CIFS, 1=NFS,
  2=FTP, 3=Unknown` come from CEE's own `ProtocolDesc` table. Dell publishes
  no protocol specification and CEE on Windows writes no log at all, so the
  binary is the only available source.

  `NewHandler` now takes a `RegistrationConfig`.

### Added

- **The CEE partner allowlist is now honoured** (`pkg/server/register.go`).
  `friendly_name`, `guid` and `version` are configurable under `[cepa]`, because
  CEE will only register an identity present in `CGuidStore` — a table compiled
  into `libCEPPAPIWrapper.so` keyed by *(friendlyName, facility)* → GUID. The
  defaults deliberately do not match any row: picking someone else's registered
  vendor identity is a decision for the operator, not a silent default. The
  extracted table lives in cee-worker's `docs/cee-partner-allowlist.md`.

- **CEE's 21 event codes are mapped** (`pkg/parser/checkevent.go`,
  `pkg/mapper`). `Event/@event` is a bitmask, one bit per event in the order
  Dell documents (`OpenFileNoAccess … OpenFileWriteOffline`, mask `0x1fffff` —
  21 names, 21 bits; Dell KB 000194250, and the same order in PowerStore's REST
  API). **Bit 3 (`0x8` = CreateFile) is confirmed by measurement** against a
  live array; the other twenty are documented rather than measured and are
  marked as such in the table. Codes outside the 21 still reach the writers with
  their raw value preserved and a WARN, as before.

- **CEE's post-registration heartbeat is answered** (`pkg/server/heartbeat.go`).
  Once registered, CEE probes the consumer with `<HeartBeatRequest />` and
  scans the reply for `hbStatus=` and `ntStatus=`. This had never been seen on
  the wire because CEE only sends it to a partner it managed to register, so
  it would have fallen through to the event parser and been answered with an
  empty body — leaving CEE with no idea whether the consumer was online, one
  step after the registration fix. The reply reports `hbStatus=0`,
  `CEPP_SERVICE_ONLINE`, whose value is measured: the five state names sit in
  an indexed pointer array in `libCEPPAPIWrapper.so`, recovered from the
  relocations, and the index is the value. The separator between the two
  fields is *not* established and is noted as such in the code.

- **Dell CEE's own event dialect is parsed** (`pkg/parser/checkevent.go`).
  CEE delivers events as `<CheckEventRequest><EventList><Event …/></EventList>`,
  a third shape distinct from both the `RegisterRequest` handshake and OneFS's
  `CheckFileRequest`. The parser was keyed on `<CEEEvent>`/`<EventBatch>` only,
  so every CEE-delivered event would have been dropped as an unrecognised
  payload even once registration was fixed. `encodedPath` is preferred over
  `path` when present, since CEE supplies it precisely when the plain
  attribute is lossy.

  The numeric `Event/@event` codes **are** mapped: `pkg/parser/checkevent.go`
  names all 21 documented bits, cross-checked against Dell's Unity CLI
  `post-Events` ordering rather than against the binary's string layout, which
  is a compiler artefact and not evidence. A code outside that set is still
  written, with its raw value preserved in the label
  (`CEPP_CEE_UNMAPPED_<n>`) and logged at WARN — the same discipline used for
  the OneFS `eventType` values before an isolation run resolved them by
  measurement, so a gap stays visible instead of being silently mislabelled.

- **OneFS file events are parsed and written.** `CheckFileRequest` with
  `action="11"` carries a real audit event, not a heartbeat — same element,
  different action. These were previously acknowledged and logged at WARN but
  never written, because acknowledging one advances the cluster's forwarding
  cursor and dropping it silently would have been worse than refusing it.

  `pkg/parser/onefs.go` decodes the payload into the same `CEPAEvent` the
  PowerStore path produces — base64 UTF-16LE UNC path, `NFSEventArgs`,
  microsecond timestamps, I/O counters — so both dialects feed one mapper and
  one set of writers rather than a parallel pipeline.

  The numeric `eventType` bitmask is fully resolved, by an isolation run of one
  operation per 10-second window against a live OneFS 9.13.0.0 cluster:

  | eventType | Operation |
  |---|---|
  | 8 | open/create |
  | 32 | delete |
  | 128 | close after writing |
  | 256 | ordinary close |
  | 512 | rename (emitted against the **source** path) |
  | 2048 | set_security |

  An earlier capture of the same operations batched together could not
  attribute 32, 512 and 2048, and they were deliberately left unmapped rather
  than guessed. The attribution now closes on itself: `rm` produced 32, so 512
  cannot be delete; `mv` produced only 512, so 512 is the rename.

  Values outside that table become `CEPP_ONEFS_UNMAPPED_<n>`, are logged at
  WARN, and are still written — the cursor has already moved by then, so
  dropping them would lose them outright — but they carry the mapper's
  documented default EventID rather than a researched one, and that distinction
  is visible to whoever reads the trail.

- **`CEPP_CLOSE_UNMODIFIED` → EventID 4658.** OneFS eventType 256 is a close
  that wrote nothing; 4658 ("The handle to an object was closed") is the
  Windows audit event for exactly that. Deliberately not folded into
  `CEPP_CLOSE_MODIFIED`'s 4663, which asserts an access a bare close never
  performed.

- **The PowerStore CEPA dialect is accepted.** Three changes, each measured off
  a wire capture of a live PowerStore NAS server:

  - **POST is accepted alongside PUT.** PowerStore's Data Mover publishes with
    `POST /vee` (`User-Agent: EMC Data Mover`); Dell CEE and OneFS both use
    PUT. While only PUT was accepted, a NAS server pointed at this consumer got
    `405 Method Not Allowed` on every heartbeat and could never establish a
    CEPP session.
  - **The `CheckFileResponse` is returned in the encoding it was addressed
    in.** PowerStore speaks UTF-16LE and Dell CEE answers it in UTF-16LE; OneFS
    speaks UTF-8 and is answered in UTF-8. A reply the publisher cannot parse
    is fatal to it.
  - The existing `action="9"` handshake path then answers PowerStore's
    heartbeat with `status="0x0"`.

  **This does not, by itself, let a PowerStore array publish here directly, and
  on at least one PowerStoreOS version it cannot.** The Events Publisher's
  transport selection makes **Microsoft RPC mandatory** — it cannot be
  unticked, on an existing publisher or a newly created one — and this daemon
  serves HTTP only. Measured against a live array: with the pool listing a
  Linux consumer first and a Windows CEE host second, the array skipped the
  first entry without ever opening a TCP connection to it and used the second.
  A Linux host cannot be a direct CEPA target for such an array at all.

  What the change is good for: any publisher that speaks the PowerStore dialect
  over HTTP reaches this consumer correctly now instead of getting 405. That
  includes Dell CEE itself, and it removes a whole class of silent failure. It
  is not a route around CEE for PowerStore.

  Context for why it was attempted: Dell CEE 9.2.0.0 and 9.3.0.0 both answer
  `status="0x16"` (`VC_ERROR_CEPP_NOT_FOUND`) to a correctly-configured
  PowerStore NAS server, which responds by generating events, counting them as
  missed, and transmitting nothing. Note also that Dell does not support CEPA
  without CEE in the path.

### Resolved in this release

- **OneFS events are received but not yet decoded.** ~~Events arrive in the
  same `CheckFileRequest` element as the heartbeat, distinguished only by
  `Args/@action` (9 = heartbeat, 11 = event), carrying an `<NFSEventArgs>`
  with a **numeric** `eventType` and a base64 UTF-16LE UNC path — not the
  `CEPP_*` strings `pkg/mapper` keys on.~~

  **Superseded.** All six measured event types (8, 32, 128, 256, 512, 2048 — a
  bitmask) were resolved by an isolation run of one operation per 10-second
  window against a live OneFS 9.13.0.0 cluster, and OneFS events are now
  parsed, mapped and written — see "OneFS file events are parsed and written"
  above. This entry is kept rather than deleted because the reasoning it
  records (deliberately not guessing the bits: wrong event IDs in an audit
  trail are worse than no audit trail) is why the isolation run happened.

  The sampler main introduced for this branch survives, narrowed to its real
  audience: a payload that still fails to parse renders its structure for the
  first ten occurrences and then a structure-free line every thousandth, and
  every one is counted in `cee_events_dropped_total`. What it renders is now
  redacted — element and attribute names, no values — so the UNC path, user
  SID and client IP never reach the log store at all.

## [5.4.0] - 2026-08-13

### Added

- **PowerScale (OneFS) CEPA handshake.** OneFS opens with `<CheckFileRequest>`,
  not the `<RegisterRequest/>` that Dell CEE sends, and it requires a
  `<CheckFileResponse>` back — where PowerStore requires an empty body and
  treats any XML as fatal. The two dialects are now detected separately and
  answered differently.

  Before this, every OneFS cluster fell through to the event parser, was
  rejected as an unrecognised payload, and got an empty 200. The cluster logged
  `Error while parsing CEE CheckFileResponse` then `STATUS_DATA_ERROR`, marked
  the consumer dead, and **never sent a single event**.

  Verified against a live 4-node OneFS 9.13.0.0 cluster: all four nodes
  heartbeat cleanly, and the cluster's `Protocol Audit Cee Time` — stuck for
  ten hours — advanced to match `Protocol Audit Log Time` the moment the
  handshake was accepted. This closes one of the open questions in
  `docs/powerscale-verification.md`: OneFS does *not* perform the
  `RegisterRequest` handshake.

  The response's `status` attribute is the `vcstatus` the cluster reports.
  Measured from CEE's own replies: `0x1` surfaced as `VC_ERROR_SETUP`, `0x16`
  as `VC_ERROR_CEPP_NOT_FOUND`. `0x0` for success began as an inference from
  those two and is confirmed by the live run above — the cluster accepted it.
  What is still **not** measured is whether the same body is the right answer
  to an *event* request (`action="11"`): it is a `HeartBeatResponse`, returned
  verbatim because the cluster must be answered, and no capture of CEE
  replying to an event request exists.

  `cee_cepa_registrations_total` now counts OneFS heartbeats alongside
  PowerStore `RegisterRequest`s. Both dialects send their handshake once per
  heartbeat, so the counter's meaning is unchanged — it was already a
  heartbeat rate, not a count of distinct registrations.

### Known limitation

- **OneFS events are received but not yet decoded.** Events arrive in the same
  `CheckFileRequest` element as the heartbeat, distinguished only by
  `Args/@action` (9 = heartbeat, 11 = event), carrying an `<NFSEventArgs>` with
  a **numeric** `eventType` and a base64 UTF-16LE UNC path — not the `CEPP_*`
  strings `pkg/mapper` keys on. They are counted in `cee_events_dropped_total`
  rather than silently dropped, because acknowledging an event advances the
  cluster's forwarding cursor and destroys the record.

  The counter is the alertable signal and counts every event. The payload is
  logged at WARN for the first ten only, capped at 4 KiB, with a payload-free
  line every thousandth after that: OneFS sends one `CheckFileRequest` per
  file operation, so logging them all would flood the log and copy a UNC path,
  a user SID and a client IP per event into a second store. Ten samples answer
  what the format is; the counter answers how much is being lost.

  Six event types were measured (8, 32, 128, 256, 512, 2048 — a bitmask). Only
  the open (8) and the closes (128, 256) are identified; 32, 512 and 2048
  divide between rename, set_security and delete and need one capture per
  isolated operation to separate. Deliberately not guessed: wrong event IDs in
  an audit trail are worse than no audit trail.

## [5.3.3] - 2026-08-11

### Fixed

- **`verify-artifacts` failed its own first run** on `v5.3.2`, against a release
  whose artifacts were correct. `${archive%.*}` strips only `.gz`, so each
  tarball extracted to a `...tar` directory and the version-stamp step looked
  for `extracted/cee-exporter_5.3.2_linux_amd64/cee-exporter`, which did not
  exist. Both extensions are now stripped explicitly.

  The `v5.3.2` artifacts were verified by hand while this was fixed: checksums
  match, and the shipped `darwin_arm64` binary serves
  `cee_build_info{go_version="go1.26.5",version="5.3.2"} 1`. This release is
  the job's first end-to-end execution.

## [5.3.2] - 2026-08-11

No runtime code changed in this release. What changed is what the project can
prove about the binary it ships.

### Added

- **`static-binary` CI job** — runs on every push. Asserts four independent
  signals on the built Linux binary (`file` says `statically linked`, `ldd`
  says `not a dynamic executable`, `readelf -d` shows no `NEEDED` entry, and
  `go version -m` records `CGO_ENABLED=0`), then runs a **negative control**: a
  deliberate `CGO_ENABLED=1` build that must come out dynamic. Without the
  control, a broken assertion is indistinguishable from a clean binary.

  The job failed its own first run — on the assertion, not the binary. `ldd`
  exits 1 on a static binary, which is how it reports "not a dynamic
  executable", so piping it into `grep` under `set -o pipefail` failed the step
  on exactly the outcome being asserted.

- **`verify-artifacts` release job** — runs on every `v*` tag, after goreleaser.
  Downloads the published assets, verifies them against the published
  `checksums.txt`, then asserts, on the artifacts users actually download:
  static linking and no `NEEDED` entries (Linux), `CGO_ENABLED=0` (all), a
  `LICENSE` entry in every archive, that the shipped binary reports the tag's
  version in `cee_build_info` and serves a healthy `/health` — failing
  explicitly on `version="dev"`, which is what a lost `-ldflags` produces and
  what no unit test can catch — and that the published container tags are a
  multi-arch index. This release is its first execution.

- **`docs/powerscale-verification.md`** — the procedure for determining whether
  PowerScale (OneFS) can publish to this daemon, for a OneFS Simulator on ESXi
  and for a physical cluster. Explicitly marked as never executed. OneFS
  forwards protocol audit events by HTTP PUT to a CEE server URI on port 12228
  and `pkg/server` routes on method rather than path, so the transport matches
  by documentation — but OneFS names its audited events
  `create`/`close`/`delete`/`rename`/`set_security` while `pkg/mapper` keys on
  the `CEPP_*` family, and nothing has measured which appears on the wire.
  `docs/PROMISES.md` records PowerScale as **Unverified**.

- **Grafana dashboard screenshot** in the operator guide, with its caveats
  stated next to it: one traffic burst on an otherwise idle window, a queue
  never put under pressure, and a silence threshold that must be raised above
  the CEE `HeartBeatIntervalSecs` in use.

### Changed

- **The "single static binary" claim is now scoped correctly.** The darwin
  binaries are not statically linked and cannot be: `otool -L` on the released
  `v5.3.1` artifact shows `libSystem.B.dylib`, `libresolv.9.dylib`,
  `CoreFoundation` and `Security`, which Go links on macOS regardless of CGO.
  The static half of the claim is Linux; the CGO-free half holds on every
  platform. `docs/index.md` states this rather than implying full static
  linking everywhere.

- **`docs/PROMISES.md`** — several rows moved from build-configuration
  arguments to artifact measurements taken against the released `v5.3.1`
  assets: the static-binary claim, the version stamp reaching the running
  binary, the release build matrix (five archives, no windows/arm64), the
  `LICENSE`-in-every-archive claim, and the multi-arch container index. Adds a
  "Where this stands" preamble with the current status counts and what
  verification means here.

## [5.3.1] - 2026-08-11

### Fixed

- **`make install-systemd` now installs the binary** (#34). The target
  installed the unit file and created `/etc/cee-exporter`, but never put a
  binary at the unit's `ExecStart` path (`/usr/local/bin/cee-exporter`), so a
  fresh `sudo make install-systemd && systemctl enable --now cee-exporter`
  failed as `status=203/EXEC` in a restart loop — with no message from the
  daemon, which never ran.

  The build targets the host architecture rather than reusing `build-linux`,
  which pins `GOARCH=amd64`: a wrong-arch binary is also reported as
  `203/EXEC`, indistinguishable from a missing file. Note that the target now
  runs `go build`, so under `sudo`'s reset PATH the host may not find `go`;
  the operator guide's manual path covers hosts with no Go toolchain.

## [5.3.0] - 2026-08-11

### Added

- **Per-publisher CEPA liveness metrics** (#27). `/metrics` now exposes
  `cee_cepa_last_request_unix_seconds{remote}`,
  `cee_cepa_registrations_total{remote}` and a label-free
  `cee_cepa_peers_dropped_total`. Until now `cee_events_received_total == 0`
  could not distinguish a quiet NAS from a publisher that had died and was
  losing every event; Prometheus's own `up` does not separate them either,
  since it reports only that this process answers a scrape.

  The timestamp is stamped on every request path — handshake, event batch,
  unparseable payload and unreadable body alike — so a broken-but-alive
  publisher does not read as dead. The `remote` label departs from the
  deliberately label-free design of the other metrics because the signal has
  to be per-publisher to be useful: a single aggregate stays green while one
  of three publishers goes dark. Cardinality is bounded by stripping the
  ephemeral port and capping distinct publishers at 64, with overflow counted
  rather than silently dropped.

  **This does not detect a NAS Data Mover that stopped publishing into a
  healthy CEE server** — a `remote` label is a CEE host, and one CEE host
  aggregates many Data Movers.

- **Grafana dashboard and monitoring stack.** `dashboards/cee-exporter.json`
  plus `deploy/compose.yaml`, which brings up Prometheus and Grafana with the
  datasource, dashboard and six alert rules provisioned. Both services bind to
  loopback and Grafana requires an admin password; the exporter itself is
  deliberately not containerised, because that would change the source address
  CEE sees, which is the `remote` label.

### Fixed

- **CEE sends UTF-16LE; the parser only read ASCII** (#32). `IsRegisterRequest`
  and `Parse` compared raw ASCII bytes, so no CEPA handshake ever matched:
  every one fell through to the parse-error branch,
  `cee_cepa_registrations_total` sat at 0 permanently, and each publisher
  logged an ERROR every 10 seconds. A UTF-16 event payload was ACKed with
  HTTP 200 and dropped.

  The integration worked only because the parse-error branch returns 200 with
  an empty body — exactly what the handshake requires. That coincidence is why
  a full test suite never saw it. Found by deploying against three live CEE
  9.2.0.0 publishers and reading `/metrics`, not by reading code.

  Payloads are now transcoded once at the head of both entry points using
  stdlib `unicode/utf16` — no new dependency. BOM'd LE/BE and the no-BOM form
  CEE actually sends are all handled; UTF-8 passes through untouched. A
  truncated payload (an odd trailing byte) is rejected rather than decoded to
  a well-formed prefix, which would turn a partial write into a whole audit
  record.

  **Still unverified:** whether CEE encodes *event* payloads as UTF-16 the way
  it encodes handshakes. No array publishes to the test hosts, so only
  handshakes are observable. The decoder is correct either way.

### Verified against live CEE

Measured on 2026-08-11 against CEE 9.2.0.0 on RHEL 9.8, SLES 15 SP7 and
Windows Server, three publishers to one exporter:

- `HeartBeatIntervalSecs` defaults to 10s — the 60s alert threshold is six
  missed beats.
- `CEEPublisherSilent` fires for a stopped publisher only, leaving the other
  two green; `CEEExporterDown` fires when the exporter itself stops while the
  publisher alerts stay silent, so a scrape failure is not misreported as
  every publisher dying at once.
- All 8 dashboard panel expressions return data.

See `docs/PROMISES.md` for what remains **Unverified**.

## [5.2.1] - 2026-08-10

### Changed

- `github.com/fjacquet/go-evtx` v0.7.4 → v0.8.2 (`refs/tags/v0.8.2`,
  `42f02cc`). Everything in v0.8.0 through v0.8.2 is reader-side or CLI —
  `ErrChunkUnreadable` so a chunk that cannot be read stops reporting as a
  clean end of stream, a FILETIME conversion covering the format's whole
  range, `Reader.FileInfo`, a new `evtx dump`/`evtx info` binary, and a fix to
  that binary's flat shape rounding integers above 2^53. This exporter uses
  the writer, so none of it reaches the event path. Recorded as hygiene, not
  as a fix for anything observed here.

  Checked rather than assumed, because "reader-side" is a claim about someone
  else's code: a `.evtx` generated under v0.8.2 renders XML identical to one
  generated under v0.7.4, field for field, apart from `TimeCreated` and
  `Computer` — both of which differ between any two runs on a host whose name
  has changed. A byte comparison is useless here and was discarded once
  measured: two runs of the *same* version already differ, because the
  timestamp shifts every offset and CRC in the chunk.

- `make lint` now fails on an untidy `go.sum`. Stale hash lines were
  introduced and removed twice during v5.1.0 because nothing checked; one
  `go mod tidy -diff` closes it.
- `-emit-test-events` exits non-zero when the writer's `Close()` fails. It
  logged a warning and exited 0, while `Close()` is what finalises the `.evtx`
  chunk — so the `evtx-oracle` CI job could be told the output was good when
  the file was unfinalised. The decision is extracted as `emitExitCode` and
  table-tested across all four error combinations.
- The `evtx-oracle` job's generated config no longer carries `[listen]` and
  `[metrics]` addresses. `-emit-test-events` exits before either binds, so
  they suggested coverage that does not exist.

### Fixed

- `verify_evtx.py` reported a bug in its own assertion logic as
  `python-evtx could not open the file`, pointing the reader at the `.evtx`
  rather than at the script. The `try` now guards only opening the file and
  materialising the record list — every assertion runs outside it. A
  non-numeric `<EventID>` — malformed data rather than a script bug — used to
  escape the same way, as an uncaught traceback, while its `Level` and
  `Keywords` neighbours were already guarded. That parse is now guarded too,
  reported as a `FAIL:` line naming the offending text.
- `docs/windows-verification.md`'s Cleanup section already removed the
  registry key and directory sections 1-4 create, but never mentioned the
  directory and file section 5 adds for the saved-log protocol, so following
  the protocol left those behind on the test VM.

## [5.2.0] - 2026-08-10

Written after the fact: v5.2.0 was tagged and published without a CHANGELOG
entry. This records what it actually shipped, taken from the diff
`v5.1.1..v5.2.0` rather than from memory.

### Fixed

- **`docker build` worked and `podman build` did not.** The Dockerfile's
  builder stage said `FROM golang:1.26-alpine`. Docker implies
  `docker.io/library` for an unqualified name; Podman resolves it through
  `unqualified-search-registries`, which on RHEL and Fedora does not begin
  with `docker.io` and may be empty entirely under
  `short-name-mode=enforcing` — so the build failed outright there. The base
  image is now fully qualified as `docker.io/library/golang:1.26-alpine`, so
  both engines resolve it identically.

### Changed

- The version-stamp test drives `make build-<goos> VERSION=…`, the target a
  release actually uses, instead of passing `-ldflags` to `go build` itself.
  The old form proved only that the Go linker honours `-X main.version`, which
  was never in doubt, and would have stayed green if the Makefile dropped the
  flag — the failure `CLAUDE.md` warns about and the one worth catching.

## [5.1.1] - 2026-08-10

Consistency and verification follow-up to v5.1.0. Nothing an operator's SIEM
receives changes; the `.evtx` files this release writes differ from v5.1.0's
only in two `System` values that Windows was already filling in by default.

### Fixed

- **The two Windows-facing writers disagreed on `Level` and `Keywords`.** The
  same audit event written through `Win32EventLogWriter` carried Level 4 and
  the CLASSIC keyword — Windows stamps those for `EVENTLOG_INFORMATION_TYPE` —
  while the same event written to a `.evtx` file carried Level 0 and Keywords
  0x0. Measured on Windows Server 2025, before and after:

    ```text
    before   Win32 live  : Level=4 Keywords=0x80000000000000
             file        : Level=0 Keywords=0x0
    after    both        : Level=4 Keywords=0x80000000000000
    ```

    Cosmetic on its own, and worth saying so plainly: Event Viewer supplies its
    own defaults for the zeros, so a v5.1.0 file already displayed Level
    "Information" and Keywords "None" — confirmed in the GUI on 2026-08-10.
    What this fixes is one product emitting two shapes.

    The one observable gain is narrower than first stated here, and the
    qualification matters: `Get-WinEvent`'s `.LevelDisplayName` resolves to
    "Information" for file records **only on a host where the event source is
    registered**. On a host without the registration it stays empty, exactly
    as before — re-measured on 2026-08-10 against the released v5.1.1 binary.
    A trap for anyone repeating the measurement: the provider metadata is
    cached per process, so a PowerShell session that read the file before the
    source was registered keeps returning the empty string until a fresh
    process is started.

    An upstream issue was filed claiming this made Event Viewer's columns
    blank. That claim was wrong and has been corrected: the columns render
    either way. See [fjacquet/go-evtx#13](https://github.com/fjacquet/go-evtx/issues/13).

### Changed

- `github.com/fjacquet/go-evtx` v0.7.3 → v0.7.4 (`Origin.Hash 4bd5d03`,
  `refs/tags/v0.7.4`), which honours `Level`, `Version`, `Task`, `Opcode` and
  `Keywords` from the fields map. Before it, those keys were accepted without
  error and silently dropped — which is how the disagreement above went
  unnoticed: passing them looked like it worked.
- `evtx-oracle` now asserts `System/Level` and `System/Keywords`, compared
  numerically rather than by string: python-evtx renders Keywords zero-padded
  to sixteen hex digits and Windows does not, so a string assertion would pass
  on one reader and fail on the other for no reason that concerns correctness.
- OUT-06 moves from **Verified (partial)** to **Verified**. The one check
  that had not been run — opening the saved log in Event Viewer's GUI — was
  run on 2026-08-10, over an RDP session to `winvm` (Windows Server 2025
  Datacenter; the earlier SSH-only connection had no interactive desktop),
  against the **released v5.1.0 binary downloaded from GitHub**, not a local
  build. It passed completely: the saved log opens via Action → Open Saved
  Log…, lists under Saved Logs → released-v5.1.0 with "Number of events: 3",
  all three IDs {4660, 4663, 4670} present, and the General pane for event
  4660 shows `Log Name: Security` — the `Channel` fix from v5.1.0, now
  visible in the GUI itself and not only in `Get-WinEvent -Path`'s
  `.LogName` property. Full output in `docs/windows-verification.md`
  section 5.

  A prediction made ahead of this run was falsified. A headless
  `Get-WinEvent` measurement had found `.LevelDisplayName`,
  `.TaskDisplayName`, `.OpcodeDisplayName` and `.KeywordsDisplayNames` all
  returning empty strings for these records, and predicted the Event Viewer
  columns would therefore render blank. They did not: Event Viewer supplies
  its own defaults for the zero values and rendered `Information` / `None` /
  `Info` / `None`. The PowerShell properties are empty because there is no
  provider metadata to resolve a display name against, which is a different
  thing from what the GUI displays — an upstream issue filed against
  go-evtx on the strength of the headless measurement has since been
  corrected: it never had an operator-visible symptom, and the API-surface
  half of it — `Level`, `Task`, `Opcode` and `Keywords` emitted as literal
  `0` with their fields-map keys silently dropped, while `Channel` was
  honoured — describes go-evtx **before v0.7.4**, which this release pins and
  which fixes it. `docs/PROMISES.md`, `docs/requirements.md`, and
  `docs/PRD.md` are updated to reflect OUT-06's full verification.

## [5.1.0] - 2026-08-10

Windows can read the `.evtx` files the non-Windows build of this exporter
writes. It never could before.

### Fixed

- **Every `.evtx` written on Linux before this release is unreadable by
  Windows.** `Get-WinEvent -Path` returns `The event log file is corrupted`,
  and Event Viewer refuses them for the same reason. The cause was in the
  EVTX encoder: go-evtx before v0.7.0 wrote `NULL` both in an
  `OptionalSubstitution`'s token and in its substitution-array entry, a
  combination that occurs zero times in 27 million structural observations
  across 320 398 real Windows records.

    These files cannot be repaired — the bytes are wrong and the source events
    are gone. Only files written by v5.1.0 and later are readable. The `gelf`,
    `syslog` and `beats` outputs are unaffected, as is the Windows-native
    `evtx` output, which uses the Win32 API rather than writing files.

    This shipped in every release since v2. Nothing caught it because nothing
    in this repository had ever read one of these files back.

- **`-emit-test-events` produced a record shaped unlike anything `pkg/mapper`
  ever emits.** It left `ProviderName`, `Computer` and `TimeCreated` at their
  zero values; every real mapped event carries all three. A record with an
  empty `ProviderName` renders as `<Provider></Provider>` with no `Name`
  attribute — go-evtx v0.7.0 tolerated that (`Name=''`), but v0.7.1 omits the
  attribute entirely, and `Get-WinEvent` throws a `NullReferenceException`
  dereferencing it. `wevtutil qe` read the same file without complaint (exit
  `0`), which is what showed the file itself was not malformed — see Added,
  below. `mapper.ProviderName` is now exported so `-emit-test-events` and
  `pkg/mapper` share one literal instead of two that can drift apart again.

- **`windowsEventToFields` dropped `Channel`, so every `.evtx` record this
  exporter has ever written rendered as `<Channel></Channel>` and Windows
  resolved `LogName` to the empty string.** This is not a fixture-only bug
  like the one above — `pkg/mapper` has set `Channel: "Security"` on every
  mapped event since v2; the field was simply never copied into the map
  handed to go-evtx, so it affected every real event this exporter has ever
  produced, not only `-emit-test-events`. Fixed by passing `Channel` through,
  with the new `evtx.DefaultChannel = "Security"` covering
  `-emit-test-events`, which names no channel of its own. Measured on
  Windows Server 2025, same three records, source registered:

  ```text
  before   LogName=[]           Message: An object was deleted. test-user
  after    LogName=[Security]   Message: An object was deleted. test-user
  ```

  The descriptions rendered before this fix too — what was missing was the
  log the events belong to, not the message text. An earlier draft of
  `docs/windows-verification.md` had blamed the empty `Channel` for
  descriptions not rendering; that was wrong (the cause was the empty
  `ProviderName` above), and the empty `Channel` was a separate, real defect
  in its own right — both are recorded there now.

### Added

- `evtx-readback` CI job. Downloads a `.evtx` generated by the new Linux
  `evtx-oracle` job and reads it on `windows-latest` with `Get-WinEvent
  -Path`, asserting 3 records, the event ID set {4660, 4663, 4670}, `ToXml()`
  per record, and `ObjectName` in the rendered XML. This is the proof behind
  OUT-06, which had been marked Unverified since v2 — and its first catch was
  real and valuable, though not what it first looked like. Evaluating
  go-evtx v0.7.1 as a candidate bump, a file with three records and an empty
  `ProviderName` threw `Object reference not set to an instance of an
  object` under `Get-WinEvent` — no record returned, at file level — and
  v0.7.1 was blamed. It was not the library. `wevtutil qe` read the same
  file with exit `0`, which is what showed the file was not malformed.
  Isolated on Windows Server 2025 on 2026-08-10, one variable at a time, all
  under v0.7.1: the same three records read back cleanly once
  `ProviderName` was set, regardless of how many `EventData` fields were
  empty. The fixture was broken in a way no real mapped event is — see
  Fixed, above. Tracked upstream as
  [fjacquet/go-evtx#10](https://github.com/fjacquet/go-evtx/issues/10),
  closed as not-planned with this correction; the project pin moved to
  v0.7.1 rather than staying back, then to v0.7.2 once that issue's
  suggested write-time validation landed there — see Changed, below.
- `evtx-oracle` CI job and `tools/evtx-debug/verify_evtx.py`. Checks the same
  file with python-evtx, an independent parser, on Linux where it is produced.
  Its blind spot is stated in the script and the README: it parses the
  pre-v0.7.0 files Windows rejects, and it accepted the broken-fixture file
  that `Get-WinEvent` rejected above without complaint too — so it is a
  structural second opinion, not the Windows proof; only a Windows read-back
  job catches what it misses. Since the `Channel` fix (see Fixed, above), it
  also asserts `System/Channel == "Security"` in the generated XML — nothing
  else in either CI job would have noticed that field going missing again.
- `docs/windows-verification.md` section 5 — the dated manual Event Viewer
  protocol. It records what CI cannot check: opening a saved log in Event
  Viewer's GUI, not run on 2026-08-10 since `winvm` is reached only over SSH
  with no interactive desktop session. Description rendering from a saved
  log — the other question this section answers — was first read as a
  go-evtx limitation; re-measured on 2026-08-10 with the `-emit-test-events`
  fixture corrected (see Fixed, above), the same saved `.evtx` renders all
  three descriptions correctly, with the event source registered on the
  host. The section also traced and recorded the separate empty-`Channel`
  defect and its fix, narrowing the section's one remaining open item to
  whether Event Viewer's GUI opens the log and where it places the events.

### Changed

- `github.com/fjacquet/go-evtx` v0.6.0 → v0.7.3, in four steps. v0.7.0's
  BREAKING change removes `Reader.ReadRecord()` and the `Record` struct; this
  project uses only the writer API, so nothing here changed. v0.7.1 tightens
  `<Provider>` rendering — an empty `ProviderName` now omits the `Name`
  attribute instead of emitting `Name=''` — which is what surfaced this
  project's own `-emit-test-events` defect (see Fixed, above) rather than a
  library regression. The pin then moved once more, to v0.7.2
  (`Origin.Hash e39994d`, `refs/tags/v0.7.2`), which carries the write-time
  validation suggested when
  [fjacquet/go-evtx#10](https://github.com/fjacquet/go-evtx/issues/10) was
  closed. It moved again, to v0.7.3 (`Origin.Hash 5fba621`,
  `refs/tags/v0.7.3`), which declares an EVTX template once per chunk instead
  of once per record (F19). Regenerating the `-emit-test-events` fixture
  shows 2907 bytes differ from the v0.7.2 output for identical input, so the
  bump reaches this project's write path — but the file size itself does not
  change: both v0.7.2 and v0.7.3 produce a 69632-byte file, because EVTX
  chunks are fixed-size and padded and a three-record fixture is one chunk
  regardless of pin. Upstream reports a 46% size reduction on its own corpus;
  that is upstream's claim about its own corpus, not this project's, and the
  benefit — more records per chunk — would only be visible in a VCAPS batch
  of thousands, which this project has not measured. v0.7.3 is the pin this
  project stays on; the full chain was re-run against it — build, 152 tests
  under `-race` across 9 packages, `go mod tidy -diff` clean, the Linux
  python-evtx oracle, and `Get-WinEvent` on Windows Server 2025 reading three
  records with descriptions and `LogName=[Security]`.
- OUT-06 moves from **Unverified** to **Verified (partial)** in
  `docs/PROMISES.md`, `docs/requirements.md` and `docs/PRD.md`: the file
  opens, all three records enumerate, all twelve `EventData` fields carry
  correct values, `LogName` resolves to `Security`, and descriptions render.
  Which job gates which, because the two check different things and a field
  regression must not look covered by the wrong one:

    - `evtx-readback` (windows-latest) — the file opens under
      `Get-WinEvent -Path`, three records enumerate, the event ID set is
      {4660, 4663, 4670}, `ToXml()` succeeds per record, and `ObjectName`
      appears in the rendered XML.
    - `evtx-oracle` (ubuntu) — the twelve `EventData` names and values, plus
      `System/Provider` `Name`, `System/Computer` and `System/Channel`. The
      last is what keeps `LogName` resolving to `Security`.
    - The dated manual protocol in `docs/windows-verification.md` —
      description rendering from a saved log, which needs a host with the
      event source registered and so cannot be a CI check.

    Partial, not closed. One check has not been run: **opening the saved log
    in Event Viewer's GUI**, which would also answer where it places the
    events. `winvm` is reached only over SSH with no interactive desktop
    session. `Get-WinEvent -Path` is not the GUI, and this project does not
    grade an unrun check as verified — see the vocabulary at the top of
    `docs/PROMISES.md`.

## [5.0.1] - 2026-08-09

Codebase health pass. Four items from an audit of the tree after v5.0.0, plus
one found reviewing the fix for the first — every one a case of something that
looked verified and was not.

Nothing here changes what the exporter sends to a SIEM. The one behavioural
change an operator can observe is in `/health`: `days_remaining` now reports
the last day before expiry and already-expired certificates correctly, where
before both read as a missing field or `0`. Anyone alerting on that field
should read the table in the Operator Guide.

### Fixed

- **`/health` deleted `days_remaining` on the day it mattered.** The field was
  an `int` with `omitempty`, and `int(hours/24)` truncates to 0 throughout the
  final 24 hours before certificate expiry — so the field vanished from the
  response at exactly the moment a monitor needed it, and its absence was
  indistinguishable from TLS being switched off. It is now a `*int`: absent
  means no certificate, `0` means it expires within 24 hours, and a negative
  value means it has already expired. Note for anyone alerting on this: check
  the value, not truthiness.
- **`days_remaining` also could not tell an expired certificate from one
  expiring today.** `int()` truncates toward zero, so a certificate that
  expired eleven hours ago reported `0` — the same as one with eleven hours
  left. The value is now floored, which makes it monotonic in the expiry time
  and leaves `0` meaning exactly one thing. Caught by CodeRabbit on the very
  PR that introduced the surrounding fix; the three tests written for that fix
  all missed it.
- Four comments that contradicted their code.
  `startACMEChallengeListener` documented HTTP-01 while implementing
  TLS-ALPN-01 (which is also why its `:443` requirement cannot be relocated);
  `installSIGHUP` claimed only `BinaryEvtxWriter` satisfies `Rotate()`, when
  `MultiWriter` does too and that is the entire reason `MultiWriter.Rotate`
  exists; `writer_multi.go`'s header said the first error is returned while
  `WriteEvent` joins every error; and two comments in `tls.go` deferred work
  to a "Plan 02" that shipped long ago.

### Changed

- **`make ci` no longer passes on unformatted code.** `.golangci.yml` had no
  `formatters:` section, and in golangci-lint v2 formatters are not linters —
  without that section `golangci-lint run` checks no formatting at all. The
  gate has been blind for the project's entire life; five files in the tree
  were unformatted when this was found. `gofmt` and `goimports` are now
  enabled and `make lint` fails on them. `issues.max-same-issues` is set to 0
  as well, because every gofmt violation carries the same message and the
  default cap of 3 reported five broken files as three.
- Three tests rewritten because each one passed against a mutation that broke
  the behaviour it was named for. `TestBuildGELFShortMessageTruncation` never
  reached the 250-byte branch; `TestBeatsWriterDialerInjection` injected no
  dialer and its TLS and non-TLS cases both failed at TCP connect, returning
  the identical error; and `TestBinaryEvtxWriter_Concurrent` asserted only
  that the output file was non-empty, which holds with nine of ten events
  dropped. Its comment also credited a `sync.Mutex` that does not guard the
  write path at all — `b.mu` covers `Close` alone, and serialisation belongs
  to go-evtx.
- A fourth test in the same family. `TestBuildSyslog5424`'s only subtest was
  called "all required fields present" while asserting bare substrings for
  four of the seven `audit@32473` SD-PARAMs — `Domain`, `AccessMask` and
  `ClientAddr` were unmentioned, so deleting them left it green. It now
  asserts each rendered `Key="Value"` pair, the header fields, and the message
  body; every field carries a distinct value so swapping two also fails.
  `TestBuildSyslog5424ProcID` covers the PROCID nil-value branch. Nine
  mutations run, nine caught.
- `/dist/` is gitignored. goreleaser's five-binary output was sitting
  untracked, one `git add -A` away from a 60 MB commit.
- `docs-lint.yml` uses `actions/checkout@v5`, matching `ci.yml`.

## [5.0.0] - 2026-08-09

Windows verification pass. For the product's entire life, every Windows event
rendered as `The description for Event ID N from source PowerStore-CEPA cannot
be found`, with the payload appended as a raw insertion string. Nobody noticed
because nothing ever executed that code: `make ci` runs on Linux, where
`//go:build windows` excludes those files from the compiler entirely, so a
Windows-only change could be green on every check and still be broken.

### Added

- Compiled Windows message resource (`pkg/evtx/messages.mc` →
  `rsrc_windows_amd64.syso`) defining descriptions for event IDs 4660, 4663
  and 4670. Committed and linked by filename, so it works under
  `CGO_ENABLED=0` and adds nothing to distribute. `make winres` regenerates it
- `windows` CI job (`windows-latest`) — the first thing in this repo's history
  to *execute* `writer_windows.go` and `service_windows.go` rather than merely
  compile them. It asserts, per event ID, that Event Viewer resolves that ID's
  own description; a resource with a missing or swapped description fails the
  job. It also drives the real SCM lifecycle
  (`install` → `sc.exe start` → `stop` → `uninstall`), which is the only thing
  that ever calls `svcProgram.Start`/`Stop`
- `-emit-test-events` — writes one event per mapped ID, so an operator can
  confirm event source registration and message rendering on their own host
- `docs/windows-verification.md` — manual Event Viewer protocol covering what
  CI cannot: whether a human reading the event sees something legible, and the
  upgrade path from an actually-released older build
- `TestMessageResourcePresent` — platform-agnostic guard that the committed
  resource exists and is a COFF amd64 object, so a deletion fails the Linux gate

### Changed

- The event source is registered with `eventlog.Install` against the
  exporter's own executable instead of `eventlog.InstallAsEventCreate`, whose
  `EventMessageFile` points at `EventCreate.exe` — a message table that stops
  at ID 1000
- Registration failure (no Administrator rights) now logs a warning naming the
  consequence and continues, instead of being silently ignored. An exporter
  writing badly-rendered events is more useful than one that refuses to start

### Fixed

- **Upgrade path.** `eventlog.Install` is a no-op on an existing source, so
  every host that ran a previous version would have kept rendering placeholder
  text forever. The exporter now detects a stale `EventMessageFile` and
  re-registers

### Removed

- **`windows/arm64` is no longer built or published.** Windows Server does not
  ship for ARM64 and no CI runner exists to execute the artifact, so it was
  published every release without ever being run. This is the breaking change
  behind the major version bump

### Still unverified

Recorded in `docs/PROMISES.md` rather than left looking proven:

- Generated `.evtx` files opening in Windows Event Viewer — depends on
  go-evtx v0.7.0, not yet released
- SIEM content-pack compatibility. What is proven is that Event Viewer and
  Event-Log-API readers resolve a real description
- DEPLOY-05, Windows Service auto-restart after a crash — nothing simulates a
  crash
- The non-Administrator registration path, verified by an equivalent
  (a registry Deny ACE) rather than a genuine non-admin logon, which the test
  host could not provide
## [4.1.3] - 2026-08-08

Documentation and truth pass: closes a five-month gap between what the docs
claimed and what the code actually did. See `docs/PROMISES.md` for the
mechanism that is meant to prevent this from recurring.

### Added

- `LICENSE` (MIT) at the repo root and in every release archive — the README
  badge had claimed MIT-licensed for months with no `LICENSE` file present
- Build-stamped `-X main.version` reaches the running binary on every build
  path (Makefile, Dockerfile, goreleaser); `cee_build_info` Prometheus gauge
  exposes `version` and `go_version` labels
- Docker image publishing wired into the release pipeline via goreleaser's
  `dockers_v2` (multi-arch `linux/amd64` + `linux/arm64`, `Dockerfile.goreleaser`)
- `docs/PROMISES.md` — maps every user-facing claim to the job that verifies
  it; a claim with no job is labelled Unverified rather than left looking proven
- Tests for the two CEPA protocol guarantees that were previously correct
  only "by inspection": `TestServeHTTP_RegisterRequest_EmptyBody` (CEPA-01)
  and `TestServeHTTP_ACKsBeforeQueueWork` / `TestServeHTTP_LargeBatchACKsWellUnder3s`
  (the 3-second heartbeat ACK ordering)
- `docs/requirements.md` — canonical, verification-checked requirement
  traceability list, consolidated from the retired `.planning/` milestone documents
- `CacheDirectory=cee-exporter` in the systemd unit, so the default
  `acme_cache_dir` is writable out of the box under `tls_mode="acme"`
  without operator action
- CI guard (`docs-lint.yml`) rejecting documented-but-nonexistent config
  keys (`type = "binary-evtx"`, `acme_staging`) from ever being reintroduced
- Archive banners on the 13 uncaveated files under `docs/research/`, so a
  direct link or search hit lands on the same "historical, not current
  guidance" warning already shown at `docs/research/index.md`

### Changed

- `pkg/evtx/writer_windows.go` doc comment corrected: the event source is
  registered via `InstallAsEventCreate`, which only carries message
  definitions for event IDs 1–1000. IDs 4660/4663/4670 render as
  "The description for Event ID … cannot be found" in Event Viewer and any
  forwarder built on the Event Log API — the previous comment's claim that
  IDs were "pre-registered via the message DLL path" and that "SIEM content
  packs for 4663/4660/4670 work" was false
- `.planning/` moved to `docs/archive/planning/`, excluded from the built
  documentation site (`mkdocs.yml` `exclude_docs: archive/`)
- `BinaryEvtxWriter.Close` made idempotent (a second call no longer propagates
  a `go-evtx` "close of closed channel" panic); oversized event fields are now
  capped before being handed to `go-evtx`, tracked via the new
  `cee_events_truncated_total` counter, instead of risking record corruption
- `docs/adr/ADR-014-go-evtx-library-extraction.md` written to record the
  `BinaryEvtxWriter` → `github.com/fjacquet/go-evtx` extraction (already
  shipped in `[4.0.0]`, commit `29ed067`) as an ADR for the first time;
  `ADR-009` marked **Superseded by ADR-014** — it had stood at "Accepted"
  with a "no new production dependencies" claim that stopped being true a
  full milestone ago; `docs/PRD.md`'s dependency table corrected to match
  `go.mod`

### Fixed

- Phantom config keys purged: `type = "binary-evtx"` (rejected at startup —
  the real value is `evtx`) and `acme_staging` (never implemented; silently
  ignored) were documented in the operator guide and config template for
  months
- Documentation reconciled with the shipped feature set: the README and
  `docs/index.md` covered only GELF and Win32, omitting syslog, Beats,
  Prometheus, ACME/self-signed TLS, and Windows Service; the stated Go
  requirement (1.21+ / 1.24+ in different places) was three versions behind
  `go.mod`'s 1.26.5; the platform table listed two targets against
  goreleaser's six; `config.toml.example` was missing the syslog and Beats
  fields `config.toml` already documented; the operator guide carried two
  contradicting "full config reference" blocks; SIGHUP-triggered EVTX
  rotation was implemented and documented nowhere; the metrics table listed
  five of the eight series the code exposes

## [4.1.2] - 2026-07-12

### Changed

- Go toolchain and dependencies updated to Go 1.26.5

## [4.1.1] - 2026-06-20

### Added

- `.goreleaser.yaml` release configuration (`main: ./cmd/cee-exporter`)
- Logo shown in the README; favicon and logo wired into the docs site

### Changed

- CI standardised on `fjacquet/ci` reusable workflows; `golangci-lint-action`
  bumped v6 → v9 for v2 config support; the `security` job made advisory
  (non-blocking)

### Fixed

- `LICENSE` dropped from the release archive template (the file did not
  exist in the repo at the time — see `[4.1.3]`, which adds the file itself)

## [4.1.0] - 2026-04-18

### Added

- `.golangci.yml` and CI wiring for `golangci-lint` plus the `-race` detector
- Favicon and logo assets under `public/`

### Changed

- Shared writer helpers (`sendWithRetry`, `ShortMessage`, `hostPort`) and
  `MultiWriter.Rotate()` extracted for reuse across writers
- Additional test coverage: `MultiWriter`, shared writer helpers, the health
  handler

### Fixed

- Stricter `RegisterRequest` detection
- Write deadlines added to network writer connections
- Directory-rename CEPA event mapping corrected
- Leaked `net.Conn` connections on GELF/syslog reconnect now closed before
  the replacement connection is assigned
- `server.readBody` no longer compares `err.Error() == "EOF"`; uses
  `errors.Is` and `io.ReadAll(http.MaxBytesReader(...))` instead of a manual
  chunk loop, so truncated request bodies surface as proper errors instead
  of silent partial reads

## [4.0.0] - 2026-03-05

### Added

- File rotation: `max_file_size_mb`, `max_file_count`, `rotation_interval_h`,
  and immediate manual rotation via `SIGHUP` (non-Windows)
- Periodic fsync with the `cee_last_fsync_unix_seconds` gauge; open-handle
  incremental flush model (replacing write-on-close)
- Startup validation of `[output]` config (`validateOutputConfig`)
- ADR-012 (flush ticker ownership) and ADR-013 (write-on-close model)

### Changed

- `BinaryEvtxWriter`'s EVTX binary encoding extracted into the standalone
  `github.com/fjacquet/go-evtx` module; cee-exporter's writer became a thin
  adapter over it

## [3.0.0] - 2026-03-04

### Added

- TLS certificate automation: `tls_mode` config field with four modes —
  `off`, `manual`, `acme` (automatic Let's Encrypt via TLS-ALPN-01), and
  `self-signed` (runtime-generated ECDSA certificate)
- `SyslogWriter` — RFC 5424 over UDP and TCP (RFC 6587 octet-counting framing)
- `BeatsWriter` — Lumberjack v2 to Logstash/Graylog, with TLS
- `BinaryEvtxWriter` — native `.evtx` file writer for non-Windows platforms,
  implemented from scratch (no pure-Go EVTX writer library existed)
- Windows Service (SCM) `install`/`uninstall` via `kardianos/service`, with
  automatic restart-on-failure
- Prometheus `/metrics` endpoint on a dedicated port (default 9228)
- Hardened Linux systemd unit (`deploy/systemd/cee-exporter.service`),
  `install-systemd` Makefile target

### Fixed

- EVTX chunk `HeaderSize` set to the correct offset so parsers can find
  records
- BinXML `NameNode` offset/hashing bug corrected

## [1.0.0] - 2026-03-03

### Added

- CEPA HTTP listener with RegisterRequest handshake and heartbeat ACK (3-second window)
- CEE XML parser for single-event and VCAPS bulk batch payloads
- Semantic mapping of 6 CEPA event types to Windows Event IDs (4660, 4663, 4670)
- GELF 1.1 writer over UDP and TCP with automatic TCP reconnection
- Win32 EventLog writer for Windows hosts (ReportEvent API, "PowerStore-CEPA" event source)
- MultiWriter fan-out to multiple output backends simultaneously
- HTTPS/TLS listener with configurable x509 certificate and key
- TLS certificate expiry warning at startup (WARN logged when < 30 days remain)
- GET /health JSON endpoint returning uptime, queue depth, and event counters
- Structured JSON logging via slog (level and format configurable via TOML or env)
- Async worker queue with configurable capacity and worker count
- TOML configuration file with CEE_LOG_LEVEL / CEE_LOG_FORMAT environment variable overrides
- Makefile with build, build-windows, test, lint, clean targets
- Cross-compiled Windows/amd64 binary (CGO_ENABLED=0, GOOS=windows GOARCH=amd64)
- GET /health endpoint with JSON status (uptime, queue_depth, events_received, events_written, events_dropped)

### Fixed

- http.MaxBytesReader nil ResponseWriter panic: payloads > 64 MiB now close the connection gracefully instead of panicking
