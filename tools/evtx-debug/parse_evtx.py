import Evtx.Evtx as evtx

with evtx.Evtx("/tmp/audit.evtx") as log:
    records = list(log.records())
    print(len(records), "record(s)")
    if records:
        print(records[0].xml()[:500])
