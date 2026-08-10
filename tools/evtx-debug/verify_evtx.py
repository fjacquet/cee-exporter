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

import contextlib
import sys
import xml.etree.ElementTree as ET

import Evtx.Evtx as evtx

EXPECTED_IDS = {4660, 4663, 4670}

# Exactly the keys windowsEventToFields emits. ClientAddr is deliberately
# absent: emitTestEvents sets it, but the evtx field map does not carry it --
# only the syslog writer does. Expecting it here would fail on correct output.
#
# Presence of these names cannot currently fail: go-evtx v0.7.0 renders a
# fixed per-EventID template with all twelve EventData slots always emitted,
# so a missing key in windowsEventToFields yields an empty value, not a
# missing element (measured 2026-08-09 by deleting HandleId from the writer
# and observing the field still appear, empty). This list is kept as a guard
# against a future template change that drops slots, not as today's teeth.
# Today's teeth are EXPECTED_VALUES.
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

# Values -emit-test-events produces, covering all twelve EventData fields.
# Asserting these is what separates "the file parsed" from "the file carries
# our data": a writer that emitted twelve correctly-named empty fields would
# satisfy a names-only check, and until this covered every field, a writer
# that dropped SubjectUserSid or silently mis-set ProcessId would too.
EXPECTED_VALUES = {
    "ObjectName": r"C:\test\emit-test-events.txt",
    "ObjectType": "File",
    "ObjectServer": "Security",
    "SubjectUserName": "test-user",
    "SubjectDomainName": "TEST",
    "AccessList": "ReadData",
    "AccessMask": "0x1",
    "SubjectUserSid": "",
    "SubjectLogonId": "",
    "HandleId": "",
    "ProcessId": "0",
    "ProcessName": "",
}


def _as_int(text):
    """Parse a System-block numeric value, hex or decimal, or None if unparseable.

    Renderers disagree on formatting the same value: python-evtx zero-pads
    Keywords to sixteen hex digits, Windows does not. Comparing the number
    sidesteps that without loosening the assertion.
    """
    try:
        return int(text, 0) if text else None
    except ValueError:
        return None


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
    # it must run while the file is still open -- calling it after the block
    # exits raises "ValueError: mmap closed or invalid" on every record,
    # every time, on any platform. The brief's version called it after the
    # block exited and could never exit 0. Only the scope of the open file
    # changed below; every assertion, message and value is unchanged from
    # the brief.
    #
    # The try below is deliberately narrower than that open scope: it guards
    # only entering the file (via ExitStack.enter_context, which drives
    # Evtx.__enter__ -- the actual open()/mmap() call; Evtx.__init__ itself
    # does no I/O) and materialising the record list. A plain `try: with
    # evtx.Evtx(path) as log: ...` cannot be narrowed this way -- the with
    # statement's own __enter__ still runs unguarded before its suite starts,
    # and any try nested inside that suite could not also close the mmap
    # after the try's scope ends without ending the whole block early. Using
    # ExitStack instead lets us guard exactly the open, then let the
    # per-record assertion loop run in the same still-open scope but outside
    # the try, so a bug in the assertion logic raises as itself -- naming the
    # real line -- instead of being relabelled "could not open the file" and
    # pointing the next reader at the fixture instead of the script.
    with contextlib.ExitStack() as stack:
        try:
            log = stack.enter_context(evtx.Evtx(path))
            records = list(log.records())
        except Exception as exc:  # noqa: BLE001 - any open/parse failure is a failure
            return [f"python-evtx could not open the file: {type(exc).__name__}: {exc}"]

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
                event_id_text = event_id_el.text.strip()
                try:
                    # Decimal only, not _as_int()'s base 0: an EVTX <EventID>
                    # is always decimal, and base 0 would silently accept
                    # "0x10" as 16 -- a quiet widening nobody asked for.
                    seen_ids.append(int(event_id_text, 10))
                except ValueError:
                    failures.append(
                        f"record {index}: EventID = {event_id_text!r}, not a decimal integer"
                    )

            # These three are what every real mapped event carries
            # (pkg/mapper.Map always sets them) and what the
            # -emit-test-events fixture omitted until v5.1.0 -- a shape no
            # real event has, and one that made Get-WinEvent throw
            # NullReferenceException on an empty ProviderName. Checking
            # only EventData, as this script did before, left both CI
            # jobs green while that fixture bug shipped: the
            # evtx-readback job's Get-WinEvent failure read like a
            # go-evtx/library fault rather than a fixture defect. Catch
            # it here, on Linux, before it ever reaches Windows.
            provider_el = root.find("./System/Provider")
            provider_name = provider_el.get("Name") if provider_el is not None else None
            if provider_name != "PowerStore-CEPA":
                failures.append(
                    f"record {index}: System/Provider Name = {provider_name!r}, want 'PowerStore-CEPA'"
                )

            computer_el = root.find("./System/Computer")
            if computer_el is None or not (computer_el.text or "").strip():
                failures.append(f"record {index}: System/Computer is missing or empty")

            time_created_el = root.find("./System/TimeCreated")
            if time_created_el is None or not (time_created_el.get("SystemTime") or "").strip():
                failures.append(f"record {index}: System/TimeCreated has no SystemTime attribute")

            # Channel was set by pkg/mapper on every event since v2 and
            # dropped by windowsEventToFields, so every record rendered as
            # <Channel></Channel> and Windows resolved LogName to the empty
            # string -- the record belonged to no log at all. Measured on
            # Windows Server 2025: passing it through moves LogName from
            # [] to [Security]. Nothing else in either CI job would notice
            # it going missing again.
            channel_el = root.find("./System/Channel")
            channel = (channel_el.text or "").strip() if channel_el is not None else None
            if channel != "Security":
                failures.append(
                    f"record {index}: System/Channel = {channel!r}, want 'Security'"
                )

            # Level and Keywords make this writer agree with the Win32 one,
            # which stamps every event Level 4 with the CLASSIC keyword.
            # Until v5.1.1 the file path emitted 0 and 0x0 for the same
            # events. Event Viewer hides the difference behind its own
            # defaults, so nothing but an assertion will notice a
            # regression here.
            # Compared numerically, not as strings: python-evtx renders
            # Keywords zero-padded to sixteen hex digits
            # (0x0080000000000000) while Windows renders 0x80000000000000.
            # A string comparison would pass on one reader and fail on the
            # other for no reason that concerns correctness.
            level_el = root.find("./System/Level")
            level = (level_el.text or "").strip() if level_el is not None else ""
            if _as_int(level) != 4:
                failures.append(
                    f"record {index}: System/Level = {level!r}, want 4 (EVENTLOG_INFORMATION_TYPE)"
                )

            keywords_el = root.find("./System/Keywords")
            keywords = (keywords_el.text or "").strip() if keywords_el is not None else ""
            if _as_int(keywords) != 0x80000000000000:
                failures.append(
                    f"record {index}: System/Keywords = {keywords!r}, "
                    f"want 0x80000000000000 (EVENTLOG_CLASSIC)"
                )

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
