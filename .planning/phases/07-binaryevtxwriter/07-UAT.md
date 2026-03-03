---
status: testing
phase: 07-binaryevtxwriter
source: 07-01-SUMMARY.md, 07-02-SUMMARY.md
started: 2026-03-03T22:00:00Z
updated: 2026-03-03T22:00:00Z
---

## Current Test

number: 1
name: Start daemon with evtx output configured
expected: |
  Edit config.toml to set type = "evtx" and evtx_path = "/tmp/test-audit.evtx".
  Run ./cee-exporter -config config.toml.
  Expected: daemon starts, prints a log line indicating it is ready,
  no crash or error about the evtx writer.
awaiting: user response

## Tests

### 1. Start daemon with evtx output configured
expected: Edit config.toml to set type = "evtx" and evtx_path = "/tmp/test-audit.evtx". Run ./cee-exporter -config config.toml. Daemon starts and logs ready — no crash or error about the evtx writer.
result: [pending]

### 2. File created after sending an event
expected: With daemon running, send a PUT request simulating a CEPA event (or use the RegisterRequest handshake). After calling Close (e.g. stop the daemon with Ctrl-C), /tmp/test-audit.evtx exists and has non-zero size.
result: [pending]

### 3. EVTX magic bytes correct
expected: Run `xxd /tmp/test-audit.evtx | head -1`. The first 8 bytes should be `45 6c 66 46 69 6c 65 00` (ElfFile\x00). The file is recognisable as a valid EVTX file.
result: [pending]

### 4. File header CRC32 valid
expected: Run `go test ./pkg/evtx/ -run TestBinaryEvtxWriter_WriteClose -v`. All assertions pass, including the file header CRC32 check. No "CRC mismatch" failure.
result: [pending]

### 5. Windows Event Viewer opens the file
expected: Copy /tmp/test-audit.evtx to a Windows machine. Open eventvwr.msc → Action → Open Saved Log → select the file. Event Viewer displays a list of events with EventIDs 4663, 4660, or 4670 and readable Computer/Subject fields. No "The file is not a valid event log file" error.
result: [pending]

### 6. Forensics tool parses the file
expected: Run evtxdump (or python-evtx / Chainsaw) on /tmp/test-audit.evtx. Tool outputs event records with correct EventIDs and timestamps. No parse error about corrupt data.
result: [pending]

## Summary

total: 6
passed: 0
issues: 0
pending: 6
skipped: 0

## Gaps

[none yet]
