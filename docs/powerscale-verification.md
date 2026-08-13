# PowerScale (OneFS) verification protocol

**Status: partly executed.** Parts A4–A7 were run on 2026-08-12 against a live
4-node OneFS 9.13.0.0 cluster — not the Simulator. What that answered, and what
it did not, is in the [Results](#results-record) table; the short version is
that question (1) passed and question (2) failed, which is the outcome this
document predicted as most likely.

`docs/PROMISES.md` still says PowerScale support is **Unverified**, and that is
still the honest reading: the cluster reaches the exporter and heartbeats, but
not one PowerScale audit event has been decoded, mapped or written to a
backend.

Read this alongside:

- [windows-verification.md](windows-verification.md) — the same shape for the
  Windows Event Log path.
- [cee-verification.md](cee-verification.md) — the alternative
  topology, with a Dell CEE server between the cluster and the exporter, which
  is the cheapest remaining answer to question (2). Not yet run.

## What this protocol answers

One question, in three parts:

1. **Does OneFS reach us at all?** Does the cluster deliver protocol audit
   events by HTTP PUT to a listener that is not a Dell CEE server?
2. **Do we understand what it sends?** OneFS documents its audited events as
   `create`, `close`, `delete`, `rename`, `set_security`. `pkg/mapper` keys on
   the `CEPP_*` family that PowerStore's CEE emits. Whether the CEE HTTP
   payload normalises OneFS events into `CEPP_*` names is **not documented by
   Dell and must be measured**, not assumed.
3. **Does anything come out the other end?** Do the events reach the configured
   backend (GELF/syslog/Beats/EVTX) with the right event IDs and fields?

A pass on (1) with a fail on (2) is the most likely outcome, and it is a useful
result: it means the transport works and only the mapper needs new keys.

## Two deployment paths

| | Virtual (OneFS Simulator) | Physical cluster |
|---|---|---|
| Purpose | Protocol discovery, mapper development, repeatable teardown | Confirmation under real load, multi-node publisher behaviour |
| Nodes | 3 minimum (a cluster cannot form with fewer), 3–4 recommended | Whatever the cluster has |
| Risk | None — throwaway lab | Changes global audit config on a production system |
| Gets you | The wire bytes, event-type names, mapping coverage | Publisher count, load, SmartConnect naming, sustained throughput |

Do the virtual path **first**. It answers question (2) — the one that decides
whether any code changes are needed — with zero production risk.

---

## Part A — Virtual: OneFS Simulator on ESXi

### A1. Deploy the simulator

1. Download the OneFS Simulator ZIP from Dell support downloads and extract the
   OVA ([installation
   guide](https://www.dell.com/support/manuals/en-us/isilon-onefs/ifs_pub_onefs_simulator_guide/installing-onefs-simulator-by-importing-the-ova-file?guid=guid-0bc7f0ac-c626-4a5b-98fb-e3a452f1033d&lang=en-us)).
2. In vSphere, **Deploy OVF Template** → the extracted `.ova`, once per node.
3. Boot the first node and run the console wizard to **create a new cluster**.
   Boot the remaining nodes and **join the existing cluster** from each.
4. Record the OneFS version — it belongs in the results table, because the
   answer to question (2) may differ between major versions:

   ```bash
   isi version
   ```

Three nodes is the documented minimum for a virtual cluster. A single node is
enough to *emit* audit events but will not reproduce the multi-publisher
behaviour that Part B exists to test.

### A2. Create an access zone and an SMB share

Protocol auditing is configured **per access zone**, and only audits SMB, NFS
and HDFS *protocol access*. A booted cluster with no share produces no events at
all, which is indistinguishable from a broken exporter.

```bash
# The System zone is present by default and is sufficient for a lab.
isi zone zones list

# A directory and an SMB share to generate events against.
mkdir -p /ifs/data/audit-test
isi smb shares create --name=audit-test --path=/ifs/data/audit-test
isi smb shares list
```

### A3. Start cee-exporter with capture running

Do this **before** pointing OneFS at it. The first PUT is the most valuable
packet in the whole exercise and it is not repeatable — once the cluster's
forwarder advances past it, you would have to reset `--cee-log-time` to see it
again.

On the exporter host:

```bash
# Capture everything on the CEPA port, full packets, to a file.
sudo tcpdump -i any -s0 -w onefs-cee.pcap 'tcp port 12228' &

# Config: no output backend that can silently swallow a parse failure.
# EVTX to a local file keeps the evidence on disk next to the capture.
cat > /etc/cee-exporter/config.toml <<'TOML'
[listen]
addr = "0.0.0.0:12228"

[output]
type      = "evtx"
evtx_path = "/var/log/cee-exporter/onefs-test.evtx"

[metrics]
addr = "0.0.0.0:9228"
TOML

CEE_LOG_LEVEL=debug ./cee-exporter -config /etc/cee-exporter/config.toml
```

Confirm the listener is up before touching OneFS:

```bash
curl -s http://localhost:12228/health | jq .
curl -s http://localhost:9228/metrics | grep cee_cepa
```

`cee_cepa_last_request_unix_seconds` should have **no series at all** at this
point. That absence is the baseline: any series appearing later came from
OneFS, not from a stale peer.

### A4. Point OneFS at the exporter

The exporter is not a Dell CEE server, but OneFS does not know that — it PUTs
to whatever URI it is given. The path component (`/cee`, `/vee`) is irrelevant
here: `pkg/server/server.go` routes on method only, never on path.

```bash
# Note the doubled hyphens; they are required.
isi audit settings global modify \
  --protocol-auditing-enabled=yes \
  --add-cee-server-uris=http://<exporter-ip>:12228/cee \
  --hostname=<cluster-name-or-smartconnect-zone>

# Audit the System zone.
isi audit settings global modify --audited-zones=system

# Only forward events from now on. Without this, a cluster that has had
# auditing enabled for a while replays its entire backlog at the new
# endpoint — thousands of events that predate the test and make the
# throughput numbers meaningless.
isi audit settings global modify --cee-log-time "Protocol@$(date -u '+%Y-%m-%d %H:%M:%S')"

isi audit settings global view
```

Per-zone event selection, if the defaults need narrowing:

```bash
isi audit settings modify --zone=System \
  --audit-success=create,close,delete,rename,set_security
isi audit settings view --zone=System
```

`--hostname` is what the cluster calls itself in forwarded events — normally the
SmartConnect zone name. It matters because it is the identity a SIEM will group
on, not because the exporter needs it.

### A5. Generate a known set of events

Mount the share from a Windows or Linux client and perform **one operation of
each type**, in this order, noting the wall-clock time of the first:

```bash
# From a Linux client (cifs-utils):
sudo mount -t cifs //<cluster>/audit-test /mnt/audit -o user=<user>
cd /mnt/audit

echo hello > alpha.txt      # create + write + close
cat alpha.txt               # read
mv alpha.txt beta.txt       # rename
chmod 640 beta.txt          # set_security
rm beta.txt                 # delete
```

Five operations, five event families. A count that does not match on the
exporter side is as informative as a wrong mapping — OneFS may coalesce, or emit
several events per operation.

### A6. Verify on the OneFS side first

Establish that the cluster believes it sent the events, before blaming the
exporter for not receiving them:

```bash
# Did the cluster record the events at all?
isi_audit_viewer -t protocol

# How far has the CEE forwarder got? "Audit Log Time" and "Audit Cee Time"
# should match or be close. A large gap means events are queued, not lost.
isi audit progress view

# Delivery errors land here, not in the audit log.
tail -f /var/log/isi_audit_cee.log
```

### A7. Verify on the exporter side

```bash
# Did anything arrive? A series here at all answers question (1).
curl -s http://localhost:9228/metrics | grep cee_cepa

# Received vs written vs dropped answers question (3).
curl -s http://localhost:9228/metrics | grep -E 'cee_events_(received|written|dropped)_total'

# Unknown event types are the expected failure mode — check the log.
# Also check for cepa_parse_error, which means the payload did not decode.
```

Then read the capture, which is the only artefact that settles question (2):

```bash
# The bytes, as sent. Strings, not a rendering.
tcpdump -r onefs-cee.pcap -A | less

# Extract full request bodies with the HTTP headers intact.
tshark -r onefs-cee.pcap -Y 'http.request.method == "PUT"' \
  -T fields -e http.file_data | xxd -r -p | less
```

Record verbatim, in the results table:

- the exact **event type strings** in the payload (`CEPP_CREATE_FILE`? `create`?
  something else?)
- the **encoding** (UTF-8, UTF-16LE with or without BOM — CEE 9.2.0.0 sends the
  handshake UTF-16LE, see issue #32)
- whether a **RegisterRequest-style handshake** precedes the events at all
- the **element and attribute names** around the file path, user, and client IP

### A8. Turn the wire bytes into a test

The point of the capture is not the answer, it is the fixture. Whatever OneFS
sent goes into `pkg/parser` as a byte-for-byte test case — written from the
capture, **not** re-encoded by the same code path it is meant to test, which is
the mistake issue #32 corrected. New event-type strings go into
`pkg/mapper/mapper.go` with a mapping decision recorded per type, and a table
case in `mapper_test.go`.

---

## Part B — Physical cluster

Everything in Part A applies unchanged. What differs is scale, blast radius, and
the things a single simulator node cannot show you.

### B1. Before touching anything

- `isi audit settings global view` — **save the output**. If audit forwarding is
  already configured for a real consumer (Varonis, Netwrix, Data Insight, a SIEM
  connector), adding a URI redirects nothing but adding load; removing one
  breaks that consumer. Know which state you are starting from.
- A cluster writes to **up to 5 CEE servers per node**, in parallel, with each
  node opening multiple HTTP/1.1 connections. If 5 URIs are already configured,
  yours cannot be added without removing one — a change that affects an existing
  consumer, not just this test.
- Agree a change window. `--protocol-auditing-enabled=yes` on a cluster where it
  was off is a global change with a real performance cost on busy nodes.

### B2. What only a physical cluster tells you

| Question | Why the simulator cannot answer it |
|---|---|
| How many `remote` label values appear? | Every node forwards independently, so one cluster produces N publisher series — not one. The `remote` label is capped at **64** distinct publishers (`cee_cepa_peers_dropped_total` counts overflow); a large cluster plus other publishers can approach that. |
| Does the queue ever back up? | `queue_capacity` defaults to 100,000 and the lab burst never fills it. Watch `cee_queue_depth` and `cee_events_dropped_total` under real user load. |
| Is UDP GELF lossy here? | Under a real audit stream, use `gelf_protocol = "tcp"`. A UDP drop is invisible in `cee_events_written_total` — the writer counts a successful `sendto`, not a delivery. |
| Does the SmartConnect name land correctly in the SIEM? | `--hostname` is usually the SmartConnect zone name; a lab cluster has no SmartConnect. |

### B3. Sustained-load pass

Run for at least one full business day with real user traffic, then record:

- peak `cee_queue_depth`
- `cee_events_dropped_total` (must be 0 — anything else means the queue or the
  backend cannot keep up)
- `cee_writer_errors_total`
- the gap between `Audit Log Time` and `Audit Cee Time` from
  `isi audit progress view` (a growing gap means OneFS is queuing because *we*
  are slow to ACK)
- the number of distinct `remote` label values, against the 64 cap

This is also the first opportunity to see the Grafana dashboard's queue-depth
and throughput panels carry a non-trivial signal — see
[Grafana dashboard](operator-guide.md#grafana-dashboard), whose current
screenshot deliberately says it has only ever been seen idle.

### B4. Rollback

```bash
# Remove only our URI; leave any pre-existing consumer's URI alone.
isi audit settings global modify --remove-cee-server-uris=http://<exporter-ip>:12228/cee

# Only if protocol auditing was OFF before this test — check the saved output
# from B1 before running this.
isi audit settings global modify --protocol-auditing-enabled=no

isi audit settings global view
```

---

## Results record

Fill this in when the protocol is run. An empty row is not a failure — it is an
accurate statement that nobody has looked yet.

| Question | Virtual (date / OneFS version) | Physical (date / OneFS version) |
|---|---|---|
| OneFS delivers PUTs to a non-CEE listener | not run | **Yes** — 2026-08-12, OneFS 9.13.0.0, 4 nodes |
| Handshake sent before events (yes/no, what shape) | not run | **Yes, but not `RegisterRequest`** — `<CheckFileRequest>` with `Args/@action="9"`, 229 bytes, and it requires a `<CheckFileResponse>` back; an empty body is fatal |
| Payload encoding (UTF-8 / UTF-16LE / BOM) | not run | **Plain UTF-8, no BOM, no XML declaration** — unlike the 38-byte UTF-16LE CEE sends |
| Event type strings, verbatim | not run | **There are none.** `<NFSEventArgs eventType="8">` — a numeric bitmask, not a string. Six values seen: 8, 32, 128, 256, 512, 2048. Only 8 (open) and 128/256 (closes) identified |
| Events mapped to a Windows event ID (count / total) | not run | **0 / all** — `pkg/mapper` keys on `CEPP_*` names that OneFS never sends |
| `cee_events_dropped_total` after the run | not run | One per event received: they are acknowledged and discarded, because the ACK advances the cluster's forwarding cursor either way |
| Distinct `remote` label values | not run | 1 — all four nodes publish from a single source IP |
| Backend received the events (GELF/EVTX, evidence) | not run | **No.** Nothing reaches a backend; nothing is decoded |

Once filled, add the corresponding rows to `docs/PROMISES.md` with the same
evidence, and cite this document — the same way
[windows-verification.md](windows-verification.md) is cited by the Windows rows.

## Known unknowns

Stated explicitly so they are not mistaken for oversights:

- **Whether OneFS uses `CEPP_*` event names.** The single most likely reason
  this fails. Dell documents OneFS event types as `create`/`close`/`delete`/
  `rename`/`set_security`; `pkg/mapper` keys on `CEPP_*`.
- **Whether OneFS performs the CEPA `RegisterRequest` handshake.** PowerStore's
  CEE does. Nothing says OneFS does, and `pkg/server` treats a non-handshake
  body as an event batch.
- **Whether OneFS honours a non-Dell endpoint indefinitely.** It PUTs to a URI;
  whether it validates anything about the response beyond HTTP 200 is unmeasured.
- **HTTPS.** OneFS CEE URIs are documented as `http://`. The exporter's TLS
  listener is untested against OneFS.

## Sources

- [PowerScale OneFS Simulator Installation Guide](https://www.dell.com/support/manuals/en-us/isilon-onefs/ifs_pub_onefs_simulator_guide/installing-onefs-simulator-by-importing-the-ova-file?guid=guid-0bc7f0ac-c626-4a5b-98fb-e3a452f1033d&lang=en-us)
- [Configure CEE servers to deliver protocol audit events — OneFS CLI Administration Guide](https://www.dell.com/support/manuals/en-us/isilon-onefs/ifs_pub_administration_guide_cli/configure-cee-servers-to-deliver-protocol-audit-events?guid=guid-24658e0e-77c4-4e34-8382-065e2c25b96e&lang=en-us)
- [Enable protocol access auditing — OneFS CLI Administration Guide](https://www.dell.com/support/manuals/en-us/isilon-onefs/ifs_pub_administration_guide_cli/enable-protocol-access-auditing?guid=guid-dceb7edf-c746-47c4-8043-cc7dfb0962be&lang=en-us)
- [File System Auditing with Dell PowerScale and Dell Common Event Enabler](https://infohub.delltechnologies.com/en-us/l/file-system-auditing-with-dell-powerscale-and-dell-common-event-enabler/powerscale-onefs-audit-overview-1/)
- [OneFS protocol audit — event types and CEE forwarding](https://infohub.delltechnologies.com/en-us/l/powerscale-onefs-nfs-design-considerations-and-best-practices-3/onefs-protocol-audit-1-1/)
- [PowerScale: How to View Audit Logs for OneFS (isi_audit_viewer)](https://www.dell.com/support/kbdoc/en-us/000020901/how-to-view-audit-logs-on-onefs?lang=en)
