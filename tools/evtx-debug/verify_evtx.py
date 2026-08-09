#!/usr/bin/env python3
"""Verify a cee-exporter .evtx against the -emit-test-events contract.

This is the Linux-side oracle in CI. python-evtx is an independent
implementation, so it is a genuine second opinion on structure -- and it
covers the literal "parse with forensics tools" half of OUT-06.

Its blind spot is measured, not assumed: python-evtx parses the v0.6.0 files
that Get-WinEvent rejects outright as "The event log file is corrupted". It is
lenient where Windows is strict. This script therefore cannot be the proof for
OUT-06; the evtx-readback job is. Do not promote it.

Usage: verify_evtx.py <path-to-evtx>
Exit:  0 all assertions hold; 1 otherwise, one FAIL line per problem.
"""

import sys
import xml.etree.ElementTree as ET

import Evtx.Evtx as evtx

EXPECTED_IDS = {4660, 4663, 4670}

# Exactly the keys windowsEventToFields emits. ClientAddr is deliberately
# absent: emitTestEvents sets it, but the evtx field map does not carry it --
# only the syslog writer does. Expecting it here would fail on correct output.
EXPECTED_FIELDS = [
    "SubjectUserSid",
    "SubjectUserName",
    "SubjectDomainName",
    "SubjectLogonId",
    "ObjectServer",
    "ObjectType",
    "ObjectName",
    "HandleId",
    "AccessList",
    "AccessMask",
    "ProcessId",
    "ProcessName",
]

# Values -emit-test-events produces. Asserting these is what separates "the
# file parsed" from "the file carries our data": a writer that emitted twelve
# correctly-named empty fields would satisfy a names-only check.
EXPECTED_VALUES = {
    "ObjectName": r"C:\test\emit-test-events.txt",
    "ObjectType": "File",
    "ObjectServer": "Security",
    "SubjectUserName": "test-user",
    "SubjectDomainName": "TEST",
    "AccessList": "ReadData",
    "AccessMask": "0x1",
}


def strip_namespaces(root):
    """Drop XML namespaces so plain tag paths work.

    v0.7.0 output carries xmlns on <Event>; v0.6.0 output does not. Stripping
    means a v0.6.0 file reaches the real assertions and fails on its merits
    rather than on a path that silently matches nothing.
    """
    for el in root.iter():
        if "}" in el.tag:
            el.tag = el.tag.split("}", 1)[1]
    return root


def check(path):
    failures = []

    # NOTE (deviation from task-2-brief.md, documented in task-2-report.md):
    # record.xml() lazily reads from the mmap that Evtx.__exit__() closes, so
    # it must run while the `with` block is still open -- calling it after
    # the block exits raises "ValueError: mmap closed or invalid" on every
    # record, every time, on any platform. The brief's version called it
    # after the block exited and could never exit 0. Only the scope of the
    # `with` block changed below; every assertion, message and value is
    # unchanged from the brief.
    try:
        with evtx.Evtx(path) as log:
            records = list(log.records())

            if len(records) != 3:
                failures.append(f"expected 3 records, got {len(records)}")

            seen_ids = []
            for index, record in enumerate(records):
                try:
                    raw = record.xml()
                except Exception as exc:  # noqa: BLE001
                    failures.append(f"record {index}: xml() raised {type(exc).__name__}: {exc}")
                    continue

                try:
                    root = strip_namespaces(ET.fromstring(raw))
                except ET.ParseError as exc:
                    failures.append(f"record {index}: rendered XML is not well-formed: {exc}")
                    continue

                event_id_el = root.find("./System/EventID")
                if event_id_el is None or not (event_id_el.text or "").strip():
                    failures.append(f"record {index}: no EventID")
                else:
                    seen_ids.append(int(event_id_el.text.strip()))

                data = {}
                for el in root.findall("./EventData/Data"):
                    data[el.get("Name")] = el.text if el.text is not None else ""

                for field in EXPECTED_FIELDS:
                    if field not in data:
                        failures.append(f"record {index}: EventData is missing {field}")

                extra = sorted(set(data) - set(EXPECTED_FIELDS))
                if extra:
                    failures.append(f"record {index}: unexpected EventData fields {extra}")

                for field, want in EXPECTED_VALUES.items():
                    got = data.get(field)
                    if got != want:
                        failures.append(f"record {index}: {field} = {got!r}, want {want!r}")

            # A set, not a list. Get-WinEvent enumerates newest-first on the Windows
            # side and this script must agree with that contract rather than pin an
            # order neither tool guarantees.
            if seen_ids and set(seen_ids) != EXPECTED_IDS:
                failures.append(f"event IDs {sorted(seen_ids)}, want {sorted(EXPECTED_IDS)}")
            if len(set(seen_ids)) != len(seen_ids):
                failures.append(f"duplicate event IDs: {sorted(seen_ids)}")
    except Exception as exc:  # noqa: BLE001 - any parse failure is a failure
        return [f"python-evtx could not open the file: {type(exc).__name__}: {exc}"]

    return failures


def main():
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <path-to-evtx>", file=sys.stderr)
        return 2

    path = sys.argv[1]
    failures = check(path)

    if failures:
        for failure in failures:
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1

    print(f"OK: {path} -- 3 records, IDs {sorted(EXPECTED_IDS)}, all 12 EventData fields")
    return 0


if __name__ == "__main__":
    sys.exit(main())
