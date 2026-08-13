# CEE verification protocol

**Status: written, never executed.** Nothing here has been run. It is the
procedure for finding out whether putting a Dell CEE server between a
PowerScale cluster and cee-exporter produces events this exporter already
understands — not a claim that it does.

CEE ships for both Windows and Linux and either will answer the question; the
procedure covers both, and says which to prefer.

Read this alongside
[powerscale-verification.md](powerscale-verification.md), which covers the
*direct* path (OneFS PUTs straight at the exporter). That path has been run;
this one has not.

## Why this exists

The direct path was measured on 2026-08-12 against a live 4-node OneFS
9.13.0.0 cluster, and it half-worked:

- **Transport: fine.** The cluster reaches the exporter, the handshake is
  answered, all four nodes heartbeat, and `Protocol Audit Cee Time` advances.
- **Events: undecodable.** OneFS sends its events inside `<CheckFileRequest>`
  with `Args/@action="11"`, carrying an `<NFSEventArgs>` whose `eventType` is a
  **numeric bitmask** and whose path is base64 UTF-16LE. `pkg/mapper` keys on
  the `CEPP_*` strings PowerStore's CEE emits. Six values were observed
  (8, 32, 128, 256, 512, 2048); only 8 (open) and 128/256 (closes) are
  identified.

So the exporter currently answers PowerScale events and throws them away,
counting them in `cee_events_dropped_total`.

That leaves two ways forward. Write a decoder for the OneFS dialect — weeks of
guessing bitmask values with one capture per isolated operation — or **let
Dell's own CEE do the translation** and see whether what comes out the far side
is already `CEPP_*`.

This protocol answers that, and it is worth answering first because a pass
means zero new exporter code.

## The one question

> Does a Dell CEE server, fed by OneFS, emit `CEPP_*` event payloads — the
> same dialect PowerStore's CEE emits and `pkg/mapper` already handles?

Everything below exists to answer that. Three outcomes, all useful:

| Outcome | Meaning | Work implied |
|---|---|---|
| CEE emits `CEPP_*` events | PowerScale support already works via CEE | None — record it and promote the PROMISES row |
| CEE emits events in some third shape | Transport works, one new mapper dialect | Mapper keys, measurable from the capture |
| CEE forwards nothing | OneFS→CEE link is the problem, not the exporter | Debug CEE, not this repo |

## Topology

Direct path, as measured (v5.4.0 behaviour):

```text
OneFS  ──HTTP PUT :12228──▶  cee-exporter
        <CheckFileRequest>    answers, counts as dropped, discards
```

What this protocol sets up:

```text
OneFS  ──HTTP PUT :12228──▶  Dell CEE  ──HTTP PUT──▶  cee-exporter
        <CheckFileRequest>   (translates?)  <RegisterRequest/> + <CEEEvent>?
```

The exporter's role does not change: it is the CEPA consumer, an HTTP listener
that answers PUTs. Only who PUTs to it changes.

!!! danger "Do not leave both URIs configured"

    `isi audit settings global --cee-server-uris` is a **list**, and OneFS
    distributes events across the servers in it. If the exporter's direct URI
    stays in that list next to the new CEE server's, roughly half the events go
    down the direct path — where this exporter acknowledges them and destroys
    them, because the ACK advances the cluster's forwarding cursor. The other
    half go through CEE and are the only ones you can measure.

    The result is a run that looks half-broken for a reason that has nothing to
    do with what you are testing, and a set of audit records that no longer
    exist. **Replace the URI, never append to it** — step D2.

## Windows or Linux

CEE ships for both, and either answers the question. They differ in ways that
matter for a lab:

| | Windows | Linux |
|---|---|---|
| Delivery protocols | RPC **and** HTTP | HTTP only |
| Configuration | Registry, `HKLM\SOFTWARE\EMC\CEE\CEPP` | One XML file, `/opt/CEEPack/emc_cee_config.xml` |
| Service control | `Restart-Service` | `/opt/CEEPack/emc_cee_svc restart` |
| Log | Windows Application event log | `/opt/CEEPack/emc_cee_svc.log` |
| Host needed | A Windows box | Any RHEL 9.x host — possibly the one already running the exporter |

**Prefer Linux if you have the choice.** It is HTTP-only, which removes a whole
class of failure: on Windows, delivery can silently be attempted over RPC and
the HTTP server has to be explicitly enabled, so "CEE received the event but
the exporter saw nothing" has one extra cause. The Linux configuration is also
a single file you can diff and put in the results record, rather than registry
values you have to screenshot.

`EndPoint` has identical semantics on both: a semicolon-separated list of
`PartnerId@http://address:port`, case sensitive.

## Prerequisites

| | |
|---|---|
| CEE host | RHEL 9.x, **or** Windows Server 2016+ / Windows 10+ — reachable from the cluster and able to reach the exporter |
| CEE version | 9.x — [Dell downloads](https://www.dell.com/support/product-details/en-us/product/common-event-enabler/drivers) |
| Exporter | v5.4.0 or later |
| Cluster | The same one used for the direct run, so the comparison is like-for-like |
| Ports | Cluster → CEE **12228/tcp**; CEE → exporter, whatever port you put in `EndPoint` |

!!! warning "12228 is claimed twice"

    CEE's own HTTP listener defaults to 12228, and so does cee-exporter. Putting
    both on one host with default settings means whichever starts second fails
    to bind — or, worse, the cluster's PUTs reach the exporter directly and CEE
    is never in the path at all, which looks like a successful test right up
    until you read the payloads and find `CheckFileRequest`.

    Colocating is otherwise fine and convenient on Linux. Move the exporter:

    ```toml
    [listen]
    addr = "0.0.0.0:12229"
    ```

    and point `EndPoint` at `:12229`. Then confirm who owns what before
    starting the run:

    ```bash
    sudo ss -lntp | grep -E ':(12228|12229)\b'
    ```

---

## Part A — Install CEE

Do **A-Linux** or **A-Windows**, not both. Everything from Part C onwards is
identical.

### A-Linux. Install on RHEL

```bash
# Install the CEE package for Linux, then confirm where it landed.
ls -l /opt/CEEPack/
```

Record the exact version — it belongs in the results table, because the
translation behaviour this protocol measures may differ between releases.

Turn on verbose logging **before** the run. On Linux this is the same file you
are about to configure, so it costs one edit rather than a second restart:

```bash
sudo cp /opt/CEEPack/emc_cee_config.xml /opt/CEEPack/emc_cee_config.xml.orig
# Set Debug and Verbose to 63 in emc_cee_config.xml.
sudo /opt/CEEPack/emc_cee_svc restart
tail -f /opt/CEEPack/emc_cee_svc.log
```

Keep the `.orig` copy. It is both the rollback and the evidence of what the
host looked like before.

### A-Windows. Install on Windows

Run the CEE installer. It installs the CAVA (antivirus) and CEPA (event
publishing) facilities; only CEPA matters here.

```powershell
Get-ItemProperty 'HKLM:\SOFTWARE\EMC\CEE' | Format-List
```

Find the service names — do not assume them, they have varied across releases:

```powershell
Get-Service | Where-Object { $_.Name -match 'CEE|CEPA|CAVA|EMC' } |
  Format-Table Name, DisplayName, Status
```

Whatever the CEPA service is called on this build is the one to restart in B4
and watch in F2.

---

## Part B — Configure CEE to publish to the exporter

Same three settings either way — enable the Audit facility, point `EndPoint` at
the exporter, leave the other subfacilities alone. Only the storage differs: an
XML file on Linux, registry values on Windows. All protocol strings are **case
sensitive** on both.

### B-Linux. Edit `emc_cee_config.xml`

There is no HTTP-server toggle to find: Linux CEE delivers over HTTP only,
which is the reason to prefer it for this test.

Edit `/opt/CEEPack/emc_cee_config.xml` so the CEPP Audit section is enabled and
its `EndPoint` names the exporter:

```xml
<CEPP>
  <Audit>
    <Configuration>
      <Enabled>1</Enabled>
      <EndPoint>cee-exporter@http://&lt;exporter-ip&gt;:12228</EndPoint>
    </Configuration>
  </Audit>
</CEPP>
```

Match the element names to what the installed file already contains rather than
to the sketch above — the surrounding structure varies by version, and this is
one file you can read in full before touching it.

```bash
sudo /opt/CEEPack/emc_cee_svc restart
grep -A3 -i endpoint /opt/CEEPack/emc_cee_config.xml
```

!!! warning "Two ways to mangle the value in a file"

    **The semicolon.** In an XML *file* it is an ordinary character and needs
    no escaping. In the *shell command that writes it*, `;` ends the command.
    Editing the file with an editor is safe; `echo`/`sed` without quoting is
    not.

    ```bash
    # Wrong — the shell ends the command at the semicolon.
    sudo sed -i s/OLD/a@http://x:1;b@http://y:2/ /opt/CEEPack/emc_cee_config.xml
    # Right — quoted.
    sudo sed -i 's|OLD|a@http://x:1;b@http://y:2|' /opt/CEEPack/emc_cee_config.xml
    ```

    **The ampersand.** If a `PartnerId` or URL ever contains `&`, it must be
    written `&amp;` in the XML or CEE will not parse the file at all — a
    failure that shows up as the service refusing to start, not as a bad
    endpoint.

    Read the value back after every edit, as above.

### B-Windows. Set the registry values

All of this is under `HKLM\SOFTWARE\EMC\CEE\CEPP`.

CEE on Windows can deliver over RPC or HTTP, so the HTTP server has to be
switched on explicitly. This step has no Linux equivalent:

```powershell
$http = 'HKLM:\SOFTWARE\EMC\CEE\CEPP\Configuration\Security\Http'
New-Item -Path $http -Force | Out-Null
Set-ItemProperty -Path $http -Name 'ServerEnabled' -Value 1 -Type DWord
Get-ItemProperty -Path $http
```

Then point the Audit facility at the exporter.

`EndPoint` is a semicolon-separated list in `PartnerId@http://address:port`
form for HTTP delivery. The `PartnerId` is a name you choose; it identifies
this consumer to CEE.

```powershell
$audit = 'HKLM:\SOFTWARE\EMC\CEE\CEPP\Audit\Configuration'
Set-ItemProperty -Path $audit -Name 'Enabled'  -Value 1 -Type DWord
Set-ItemProperty -Path $audit -Name 'EndPoint' `
  -Value 'cee-exporter@http://<exporter-ip>:12228' -Type String
Get-ItemProperty -Path $audit | Format-List Enabled, EndPoint
```

If `EndPoint` already has a value — another consumer such as a SIEM agent —
**append** with a semicolon rather than overwriting it. That is the opposite of
the rule for the OneFS URI list in D2, and for the opposite reason: CEE
delivers to *every* endpoint, while OneFS distributes across its URIs.
Overwriting it here silently unhooks whatever was already consuming events.

Read, append, write back — never retype the existing value from a screenshot:

```powershell
$existing = (Get-ItemProperty -Path $audit -Name 'EndPoint' -ErrorAction SilentlyContinue).EndPoint
$mine     = 'cee-exporter@http://<exporter-ip>:12228'

$value = if ([string]::IsNullOrWhiteSpace($existing)) { $mine } else { "$existing;$mine" }
Set-ItemProperty -Path $audit -Name 'EndPoint' -Value $value -Type String

(Get-ItemProperty -Path $audit).EndPoint -split ';'   # one endpoint per line
```

!!! warning "Quote the semicolon"

    The `;` is a statement separator in PowerShell and a nothing-in-particular
    in `cmd.exe`. Inside `'single'` or `"double"` quotes it is a literal
    character and the value lands intact, which is why every example here is
    quoted. Unquoted, PowerShell ends the command at the semicolon and writes a
    **truncated** `EndPoint` containing only the first consumer — the other one
    stops receiving events, with nothing in any log to say why.

    The same applies to `reg.exe`: quote the whole `/d` argument.

    ```cmd
    reg add "HKLM\SOFTWARE\EMC\CEE\CEPP\Audit\Configuration" /v EndPoint ^
      /t REG_SZ /d "other-app@http://10.0.0.9:12229;cee-exporter@http://10.0.0.8:12228" /f
    ```

    Verify by reading the value back and splitting it, as above. A semicolon
    count that does not match the number of consumers you expect is the bug.

The path component is absent above on purpose. The exporter routes on HTTP
method only, never on path (`pkg/server/server.go`), so `/cee`, `/vee` or
nothing all behave identically. If CEE insists on a path, any value works.

### B3. Leave VCAPS and CQM alone — both platforms

CEPA has separate subfacilities: Audit, VCAPS (asynchronous post-events) and
CQM. Enabling more than one makes it ambiguous which produced a given payload,
and VCAPS in particular is the one that batches thousands of events per PUT —
a different question from the one this protocol asks.

```bash
# Linux — read the whole file; it is short enough.
grep -nEi '<(Audit|VCAPS|CQM)>|<Enabled>' /opt/CEEPack/emc_cee_config.xml
```

```powershell
# Windows — expect these absent or disabled.
Get-ChildItem 'HKLM:\SOFTWARE\EMC\CEE\CEPP' | Select-Object Name
```

### B4. Restart CEPA — both platforms

Configuration is read at service start, on either platform.

```bash
# Linux
sudo /opt/CEEPack/emc_cee_svc restart
tail -n 50 /opt/CEEPack/emc_cee_svc.log
```

```powershell
# Windows — the service name discovered in A-Windows.
Restart-Service -Name '<the CEPA service>' -Force
Get-Service -Name '<the CEPA service>'
```

---

## Part C — Start the exporter with capture running

Do this **before** pointing OneFS at CEE. The `RegisterRequest` handshake and
the first event batch are the two most valuable packets in the exercise, and
neither is repeatable once the cluster's forwarder advances past them.

On the exporter host:

```bash
sudo tcpdump -i any -s0 -w cee-windows.pcap 'tcp port 12228' &

cat > /etc/cee-exporter/config.toml <<'TOML'
[listen]
addr = "0.0.0.0:12228"

[output]
type      = "evtx"
evtx_path = "/var/log/cee-exporter/cee-windows-test.evtx"

[metrics]
addr = "0.0.0.0:9228"
TOML

CEE_LOG_LEVEL=debug ./cee-exporter -config /etc/cee-exporter/config.toml
```

Baseline before anything is pointed at it — the absence matters as much as the
later presence:

```bash
curl -s http://localhost:12228/health | jq .
curl -s http://localhost:9228/metrics | grep -E 'cee_cepa|cee_events'
```

`cee_cepa_last_request_unix_seconds` should have **no series at all**, and
`cee_events_dropped_total` should be 0. Any series appearing later came from
this run.

---

## Part D — Point OneFS at CEE

### D1. Record the current setting first

You are about to overwrite it, and you will need it for rollback:

```bash
isi audit settings global view
```

Write the existing `CEE Server URIs` value down somewhere outside the cluster.

### D2. Replace the URI — do not append

Clear the list rather than removing one entry by name — `--clear` cannot leave
behind a URI you forgot was there, and D1 already recorded what to restore:

```bash
isi audit settings global modify --clear-cee-server-uris

isi audit settings global modify \
  --add-cee-server-uris=http://<cee-host>:12228/cee

isi audit settings global view
```

`--remove-cee-server-uris=<uri>` also exists if you would rather drop a single
entry, but it silently does nothing when the URI does not match exactly.

Confirm the list contains **exactly one** URI, the Windows host. See the danger
note above for why.

### D3. Forward only from now on

Without this, a cluster that has had auditing enabled for a while replays its
whole backlog at the new endpoint — thousands of events that predate the test,
which makes both the counts and the throughput meaningless:

```bash
isi audit settings global modify --cee-log-time "Protocol@$(date -u '+%Y-%m-%d %H:%M:%S')"
```

---

## Part E — Generate a known set of events

Mount the audit share and perform **one operation of each type**, in this
order, noting the wall-clock time of the first:

```bash
sudo mount -t cifs //<cluster>/audit-test /mnt/audit -o user=<user>
cd /mnt/audit

echo hello > alpha.txt      # create + write + close
cat alpha.txt               # read
mv alpha.txt beta.txt       # rename
chmod 640 beta.txt          # set_security
rm beta.txt                 # delete
```

Five operations. Anything other than five event families arriving is itself a
result — CEE may coalesce, or emit several events per operation.

---

## Part F — Verify hop by hop

Three hops, three places to lose events. Check them in order; blaming the
exporter for something CEE never forwarded wastes the most time.

### F1. Did the cluster send?

```bash
isi_audit_viewer -t protocol          # events recorded at all
isi audit progress view               # Cee Time vs Log Time — a gap means queued
tail -f /var/log/isi_audit_cee.log    # delivery errors land here
```

A `vcstatus` in that log is CEE's answer coming back. Two values are already
known from the direct run: `0x1` is `VC_ERROR_SETUP`, `0x16` is
`VC_ERROR_CEPP_NOT_FOUND` — which specifically means CEE is running but the
partner app named in `EndPoint` is not reachable. Seeing `0x16` here points at
Part B, not at the cluster.

### F2. Did CEE receive and forward?

```bash
# Linux — with Debug/Verbose at 63 from A-Linux, this is the richest
# view of the whole chain.
tail -f /opt/CEEPack/emc_cee_svc.log
```

```powershell
# Windows
Get-EventLog -LogName Application -Source '*CEE*','*CEPA*' -Newest 50 |
  Format-Table TimeGenerated, EntryType, Message -Wrap
```

If CEE logs receipt but the exporter sees nothing, the problem is `EndPoint`
(Part B) or the network between CEE and the exporter — not OneFS.

### F3. Did the exporter receive, and in what dialect?

This is the measurement the whole protocol exists for.

```bash
curl -s http://localhost:9228/metrics | grep -E 'cee_cepa|cee_events'
```

- `cee_cepa_registrations_total{remote="<cee-host>"}` climbing → CEE is sending
  the `RegisterRequest` handshake, same as PowerStore's CEE.
- `cee_events_received_total` > 0 → **the payload parsed as `CEEEvent`.** This
  is the pass condition. It means CEE translated the OneFS dialect into the one
  `pkg/parser` already reads.
- `cee_events_dropped_total` climbing while received stays 0 → CEE is
  forwarding the OneFS `CheckFileRequest` dialect unchanged. Transport works,
  translation does not.

Then read the wire, because the counter tells you *that* it parsed, not *what*
the event types were:

```bash
sudo pkill tcpdump
tcpdump -r cee-windows.pcap -A 'tcp port 12228' | grep -aoE '<EventType>[^<]+' | sort -u
```

Those strings, verbatim, are the answer. `CEPP_*` means no exporter change is
needed.

### F4. Did anything come out the far end?

```bash
ls -l /var/log/cee-exporter/cee-windows-test.evtx
evtx dump /var/log/cee-exporter/cee-windows-test.evtx | head -40
```

Event IDs 4663 / 4660 / 4670 with the right paths closes the loop from a file
operation on a client to a record in a backend.

---

## Rollback

Whatever the outcome, put the cluster back:

```bash
isi audit settings global modify \
  --remove-cee-server-uris=http://<cee-host>:12228/cee

# Restore whatever D1 recorded.
isi audit settings global view
```

Leaving the CEE host in the URI list after the test means audit events
keep flowing through a lab machine.

---

## Results record

Fill this in when the protocol is run. An empty row is not a failure — it is an
accurate statement that nobody has looked yet.

| Question | Result (date / CEE version / OneFS version) |
|---|---|
| CEE platform (Linux/Windows), version, service name | |
| CEE sends `RegisterRequest` to the exporter (yes/no) | |
| Payload encoding (UTF-8 / UTF-16LE / BOM) | |
| Event type strings, verbatim | |
| `cee_events_received_total` after the five operations | |
| `cee_events_dropped_total` after the five operations | |
| Event count vs the five operations performed | |
| Events mapped to a Windows event ID (count / total) | |
| Backend received the events (EVTX, evidence) | |

Once filled, add the corresponding rows to `docs/PROMISES.md` with the same
evidence and cite this document.

## Known unknowns

Stated explicitly so they are not mistaken for oversights:

- **Whether CEE translates the OneFS dialect at all.** The whole point. CEE may
  be a router rather than a translator, in which case the exporter receives the
  same `CheckFileRequest` payloads it cannot decode today, just from a
  different IP.
- **Whether the `PartnerId` appears in the forwarded payload.** If CEE labels
  events with it, that is a field the exporter currently ignores.
- **Whether CEE batches.** PowerStore's CEE uses VCAPS batches of thousands of
  events per PUT. Whether the Audit facility batches OneFS-sourced events the
  same way decides whether `gelf_protocol = "tcp"` is required in production.
- **HTTPS.** CEE URIs are documented as `http://` throughout. The exporter's
  TLS listener is untested against CEE.
- **Which CEE releases behave alike.** One version measured is one version
  known.

## Sources

- [Using the Common Event Enabler on Windows Platforms 9.x (Dell)](https://device.report/m/a7fd6faebaf65b556be708890f0db1960efc2e840723024c8f387bb5b1568308.pdf)
- [PowerScale: Linux CEE debugging and log gather — `/opt/CEEPack/emc_cee_config.xml`, `emc_cee_svc` (Dell KB 000009293)](https://www.dell.com/support/kbdoc/en-us/000009293/linux-cee-debugging-and-log-gather)
- [Common Event Enabler — downloads and support (Dell)](https://www.dell.com/support/product-details/en-us/product/common-event-enabler/drivers)
- [Configure CEE for Windows — OneFS CLI Administration Guide](https://www.dell.com/support/manuals/en-us/isilon-onefs/ifs-pub-91000-administration-guide-cli/configure-cee-for-windows?guid=guid-e577d98b-a1b9-4050-aff8-ac4e938787ef&lang=en-us)
- [Configure CEE servers to deliver protocol audit events — OneFS CLI Administration Guide](https://www.dell.com/support/manuals/en-us/isilon-onefs/ifs_pub_administration_guide_cli/configure-cee-servers-to-deliver-protocol-audit-events?guid=guid-24658e0e-77c4-4e34-8382-065e2c25b96e&lang=en-us)
- [CEPP server state ERROR_CEPP_NOT_FOUND (Dell KB 000052027)](https://www.dell.com/support/kbdoc/en-us/000052027/dell-emc-vnx-cee-is-not-working-due-to-cepp-server-state-error-cepp-not-found-user-correctable)
- [Install Dell CEE — registry `EndPoint` format (Netwrix)](https://docs.netwrix.com/docs/activitymonitor/8_0/requirements/activityagent/nas-device-configuration/isilon-powerscale-aac/installcee)
- [File System Auditing with Dell PowerScale and Dell Common Event Enabler](https://infohub.delltechnologies.com/en-us/l/file-system-auditing-with-dell-powerscale-and-dell-common-event-enabler/audit-management-1/)
