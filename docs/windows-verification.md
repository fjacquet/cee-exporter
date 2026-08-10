# Windows verification protocol

CI's `windows` job (`.github/workflows/ci.yml`) asserts via `Get-WinEvent` and
`Get-EventLog` that the message resource resolves and the placeholder string
never appears. Three things it cannot check, all requiring a real Windows
host:

1. Whether the rendered event is *legible* to an operator, in the actual
   words Event Viewer / `Get-WinEvent` produce — not just "not null".
2. The upgrade path from a source that was actually registered the old,
   broken way — CI's `TestEnsureEventSource_RepointsStaleSource` seeds the
   stale state via the registry directly; nothing proves the real
   `NewWin32EventLogWriter` startup path repoints a source it did not itself
   just create.
3. Degradation when registration is denied — CI runs as Administrator on
   every GitHub-hosted Windows runner, so the failure path is untested there
   by construction.

Run this protocol on a Windows host before shipping a release that changes
the message resource or the registration logic.

**Platform note:** the product's real deployment target is **Windows
Server**, not a desktop SKU. This protocol was executed on Windows Server
2025 Datacenter (build confirmed via `winvm`, 2026-08-09). An earlier draft
of this document said "Windows 11 VM" — that was aspirational, not a
description of any host this was ever run against; corrected here.

## Reproducing the pre-v5.0 baseline

A real pre-v5.0 release binary (e.g. `v4.1.3`) does **not** have
`-emit-test-events` — that flag was added in v5.0 alongside the fix, so it
cannot be used to drive an old build. To get a binary that reproduces the
*old registration behaviour* while still being able to drive it with
`-emit-test-events`, build one from a worktree with only
`pkg/evtx/writer_windows.go` swapped for the pre-v5.0 version (the file this
plan replaced, `InstallAsEventCreate` against `EventCreate.exe`) — everything
else, including the `-emit-test-events` flag in `main.go`, comes from the
current tree:

```bash
git worktree add --detach /tmp/legacy-worktree HEAD
cd /tmp/legacy-worktree
git show <commit-before-the-registration-fix>:pkg/evtx/writer_windows.go > pkg/evtx/writer_windows.go
GOOS=windows GOARCH=amd64 go build -o cee-exporter-legacy.exe ./cmd/cee-exporter
```

(For this v5.0 run, `<commit-before-the-registration-fix>` was `7df6d58`,
the pre-v5.0 HEAD.) Build the real, current binary the normal way for
comparison:

```bash
GOOS=windows GOARCH=amd64 go build -o cee-exporter.exe ./cmd/cee-exporter
```

`scp` both `.exe` files and a `config.toml` (`type = "evtx"`, any non-empty
`evtx_path` — the Win32 writer ignores the path) to `C:/Windows/Temp/`, then
drive every step below with a `.ps1` file run via
`pwsh -NoProfile -ExecutionPolicy Bypass -File` — quoting through
`ssh` → `cmd` → `powershell` does not survive multiple layers of escaping.

## 1. Baseline — reproduce the defect

```powershell
.\cee-exporter-legacy.exe -config config.toml -emit-test-events
```

This registers `PowerStore-CEPA` via `InstallAsEventCreate` (pointing
`EventMessageFile` at `EventCreate.exe`) and writes one event each for IDs
4660, 4663, 4670.

**Actual output, captured 2026-08-09 on `winvm` (Windows Server 2025):**

`Get-WinEvent` — the message is `$null`, not placeholder text:

```
--- Event 4663 (Get-WinEvent) ---
Message is null: True
Message: []
```

`Get-EventLog` — this is where the placeholder text actually appears, verbatim:

```
The description for Event ID '4663' in Source 'PowerStore-CEPA' cannot be found.  The local computer may not have the necessary registry information or message DLL files to display the message, or you may not have permission to access them.  The following information is part of the event:'Subject:
	Security ID:	
	Account Name:	test-user
	Account Domain:	TEST
	Logon ID:	

Object:
	Object Server:	Security
	Object Type:	File
	Object Name:	C:\test\emit-test-events.txt

Process Information:
	Process ID:	0x0
	Process Name:	CEPA

Access Request Information:
	Transaction ID:	{00000000-0000-0000-0000-000000000000}
	Accesses:	ReadData
	Access Mask:	0x1

Network:
	Client Address:	127.0.0.1

I/O Statistics:
	Bytes Read:	0
	Bytes Written:	0'
```

Identical shape confirmed for 4660 and 4670. This is the before-image: it
confirms the host reproduces the defect rather than starting from an
already-fixed state, so the "confirm the fix" step below is evidence of a
real change, not a host that was fine all along.

## 2. Upgrade in place

Without touching the registry, replace the binary and run the real v5.0
build once as Administrator:

```powershell
.\cee-exporter.exe -config config.toml -emit-test-events
```

Check the log for `win32_source_repointing` — that line is the proof the
upgrade path fired against a source this run did not itself create, rather
than the source being registered fresh.

**Actual output, captured 2026-08-09:**

```json
{"time":"2026-08-09T13:30:42Z","level":"INFO","msg":"win32_source_repointing","source":"PowerStore-CEPA","from":"%SystemRoot%\\System32\\EventCreate.exe","to":"C:\\Windows\\Temp\\winverify\\cee-exporter.exe","reason":"registered message file does not match this executable"}
```

`EventMessageFile` after this run: `C:\Windows\Temp\winverify\cee-exporter.exe`
(was `%SystemRoot%\System32\EventCreate.exe`).

## 3. Confirm the fix

Refresh and re-query. **Expected:** the description now reads the real text
— `An attempt was made to access an object.` for 4663, `An object was
deleted.` for 4660, `Permissions on an object were changed.` for 4670 —
followed by the `Subject:` / `Object:` / `Access Request Information:` body,
with no placeholder text anywhere.

**Actual output, captured 2026-08-09** (`Get-WinEvent`, event 4663):

```
Message is null: False
An attempt was made to access an object. Subject:
	Security ID:	
	Account Name:	test-user
	Account Domain:	TEST
	Logon ID:	

Object:
	Object Server:	Security
	Object Type:	File
	Object Name:	C:\test\emit-test-events.txt

Process Information:
	Process ID:	0x0
	Process Name:	CEPA

Access Request Information:
	Transaction ID:	{00000000-0000-0000-0000-000000000000}
	Accesses:	ReadData
	Access Mask:	0x1

Network:
	Client Address:	127.0.0.1

I/O Statistics:
	Bytes Read:	0
	Bytes Written:	0
```

All three IDs (4660, 4663, 4670) confirmed with their own message header and
no placeholder text, matching CI's assertion exactly.

## 4. Non-Administrator degradation

**Expected:** with registration denied, the exporter logs
`win32_source_registration_failed` naming the consequence, then still writes
events and exits normally. It must not crash or refuse to run.

**What was actually verified, 2026-08-09, and what was not:**

The straightforward approach — create a genuine non-administrator local
user and launch `cee-exporter.exe -emit-test-events` as that user — was
attempted and did not work on `winvm`: every attempt (`Start-Process
-Credential`, and raw `ProcessStartInfo` with `LoadUserProfile = $true`)
failed at the OS process-creation layer with `STATUS_DLL_INIT_FAILED`
(`0xC0000142`), **before any of the exporter's own code ran**. This was
confirmed to be a host/session limitation, not specific to this binary, by
reproducing the identical failure launching plain `whoami.exe` as the same
user the same way. `winvm` is accessed only over SSH with no interactive
desktop session; cross-user process creation into a session-0-only host
appears to be broken here independent of this project. That specific
sub-case — a truly separate OS logon session — was **not verified**.

What was verified instead, as the closest faithful substitute: the actual
permission `ensureEventSource` needs is `SET_VALUE`/`CREATE_SUB_KEY` on the
source's registry key. Denying exactly that (while still running the
`cee-exporter.exe` process as Administrator, avoiding the broken cross-user
launch entirely) exercises the identical code path a non-administrator would
hit, without depending on the host's broken user-session machinery:

```powershell
New-Item -Path $keyPath -Force | Out-Null
$acl = Get-Acl $keyPath
$deny = New-Object System.Security.AccessControl.RegistryAccessRule("Everyone", "FullControl", "Deny")
$acl.SetAccessRule($deny)
Set-Acl -Path $keyPath -AclObject $acl
.\cee-exporter.exe -config config.toml -emit-test-events
```

**Actual output:**

```json
{"time":"2026-08-09T13:37:40Z","level":"WARN","msg":"win32_source_registration_failed","source":"PowerStore-CEPA","err":"win32 install event source \"PowerStore-CEPA\": Access is denied.","consequence":"events will render as \"The description for Event ID N cannot be found\" until the exporter is run once as Administrator"}
{"time":"2026-08-09T13:37:40Z","level":"INFO","msg":"win32_writer_ready","source":"PowerStore-CEPA","message_file":"C:\\Windows\\Temp\\winverify\\cee-exporter.exe"}
{"time":"2026-08-09T13:37:40Z","level":"INFO","msg":"test_events_emitted","count":3}
```

Exit code `0`. The warning was logged, naming the consequence, and the
process went on to open the writer, write all three events, and exit
cleanly — it did not stop after the registration failure. This confirms the
degrade-not-die behaviour under a genuine `ACCESS_DENIED` from the OS, which
is the actual condition a non-administrator hits; it does not confirm the
unrelated cross-user-logon mechanics also work on every host, since those
failed here for reasons this project's code has no part in.

Removing that Deny ACE afterward required taking ownership of the key
(`SeTakeOwnershipPrivilege`) — a `Deny(Everyone, FullControl)` rule blocks
even the object's own reads/writes to its ACL, not just data access, so
undoing it is not a plain `Set-Acl`. See the cleanup note below.

## 5. Saved-log rendering — the part CI cannot see

`evtx-readback` proves `Get-WinEvent -Path` reads a Linux-generated `.evtx`.
It does not open Event Viewer, and it deliberately does not assert on
`.Message` or `.LogName`. This section covers what is left.

**Prerequisite — register the event source first, with an `evtx`-type
config.** On a host where `PowerStore-CEPA` is not registered, `Message` is
null and `LogName` is empty for reasons that have nothing to do with the
file itself, and that would look exactly like a rendering defect.

`.\cee-exporter.exe -emit-test-events` with no `-config` flag does **not**
register anything on Windows: it falls back to the built-in default config,
whose output type is `gelf`, so the Win32 writer never runs. Measured
2026-08-10 on winvm: `AFTER registered: False` under the default config. A
config with `type = "evtx"` is required to reach the Win32 writer at all —
Windows routes that type to `Win32EventLogWriter` regardless of `evtx_path`,
but the config loader still requires that field to be non-empty:

```toml
[listen]
addr = "0.0.0.0:12228"

[output]
type      = "evtx"
evtx_path = "C:\\evtxman\\audit.evtx"

[metrics]
addr = "0.0.0.0:9228"
```

```powershell
# As Administrator. This registers the source against the binary carrying the
# message resource; see ADR-015.
.\cee-exporter.exe -config config.toml -emit-test-events
```

Expected log line: `win32_writer_ready source=PowerStore-CEPA
message_file=C:\evtxman\cee-exporter.exe` — confirmed on winvm 2026-08-10.
Then confirm the registry key:

```powershell
Test-Path 'HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\PowerStore-CEPA'
```

Expected: `True`. If it is `False`, stop — anything observed below is
meaningless.

### Generate the file on Linux and copy it over

```bash
cat > /tmp/evtx-manual.toml <<'TOML'
[listen]
addr = "127.0.0.1:12997"
[output]
type = "evtx"
evtx_path = "/tmp/manual.evtx"
[metrics]
addr = "127.0.0.1:19997"
TOML

go run ./cmd/cee-exporter -config /tmp/evtx-manual.toml -emit-test-events
scp /tmp/manual.evtx winvm:C:/manual.evtx
```

### Question 1 — does it open, and where do the events land?

Open Event Viewer, **Action → Open Saved Log…**, select `C:\manual.evtx`.

**The empty `Channel` this question started from is fixed.** Our records
used to carry an empty `Channel`, so `LogName` resolved to the empty string.
The cause was not that the value was unknown — `pkg/mapper` has set
`Channel: "Security"` on every mapped event since v2 — it was that nothing
read the field: `windowsEventToFields` in
`pkg/evtx/writer_evtx_notwindows.go` never put it in the map handed to
go-evtx, and no other writer referenced it either. So `WindowsEvent.Channel`
was populated and then dropped, for real events as much as for the
`-emit-test-events` fixture. That is now corrected — `Channel` is passed
through, with `evtx.DefaultChannel = "Security"` covering
`-emit-test-events`, which names no channel of its own. Measured on Windows
Server 2025, same three records, source registered:

```
before   LogName=[]           Message: An object was deleted. test-user
after    LogName=[Security]   Message: An object was deleted. test-user
```

See the `[5.1.0]` entry in `CHANGELOG.md` for the fix and the mutation
testing behind it.

What is left of this question is narrower than it was: not whether `LogName`
resolves — it now does — but what Event Viewer's GUI does with a saved log
that carries `LogName=Security`: does it open, does it prompt to convert,
under what node do the three events appear, and are all three listed?

**Run on 2026-08-10, over RDP.** The SSH-only limitation recorded above no
longer blocks this: this session reached `winvm` over RDP, which supplies the
interactive desktop the GUI needs. The file under test was not a local
build — it was the `.evtx` produced by the **released v5.1.0 binary
downloaded from GitHub**, so this measures what ships, not what a worktree
produces.

Opened via **Action → Open Saved Log…**. The log appears in the tree under
**Saved Logs → released-v5.1.0**, "Number of events: 3":

```text
Level         Date and Time          Source            Event ID  Task Category
Information   8/10/2026 6:48:56 AM   PowerStore-CEPA   4670      None
Information   8/10/2026 6:48:56 AM   PowerStore-CEPA   4663      None
Information   8/10/2026 6:48:56 AM   PowerStore-CEPA   4660      None
```

The General pane for event 4660:

```text
An object was deleted. test-user

Log Name:       Security
Source:         PowerStore-CEPA        Logged:         8/10/2026 6:48:56 AM
Event ID:       4660                   Task Category:  None
Level:          Information            Keywords:       None
User:           N/A                    Computer:       mbp-fj.local
OpCode:         Info
```

Three things worth drawing out:

- `Log Name: Security` is the `Channel` fix from v5.1.0, now visible in the
  GUI itself rather than only in `Get-WinEvent -Path`'s `.LogName` property.
- `User: N/A` is expected, not a defect: the `-emit-test-events` fixture
  leaves `SubjectUserSid` empty.
- `Computer: mbp-fj.local` is the host that generated the file — correct,
  since generation happens on the machine running the exporter, not on
  `winvm`.

All three events are listed under the one saved log, matching the event ID
set {4660, 4663, 4670} `evtx-readback` already asserts. This answers the
question this section opened with: it opens, without a convert prompt, under
**Saved Logs**, and all three events land there.

**A prediction made ahead of this run was falsified, and that is recorded
here rather than quietly corrected.** Before this session, a headless
measurement via `Get-WinEvent` found `.LevelDisplayName`, `.TaskDisplayName`,
`.OpcodeDisplayName` and `.KeywordsDisplayNames` all returning empty strings
for these records, and predicted the Level/Task Category/Keywords columns
above — and the equivalent fields in the General pane — would render blank.
They did not: Event Viewer supplies its own defaults for the zero values
those properties reflect, and rendered `Information` / `None` / `Info` /
`None`, exactly as shown above. The two observations are not in tension —
they are answers to different questions. The PowerShell properties are empty
because there is no provider metadata (no registered message-resource
entries for these enum values) for `Get-WinEvent` to resolve a display name
against, and it returns an empty string rather than guessing; that is a
statement about what `Get-WinEvent` can resolve headlessly, not about what
the GUI displays. An upstream issue was filed against go-evtx on the
strength of the headless measurement and has since been corrected, with two
follow-up comments. It never had an operator-visible symptom, which this run
confirms directly. Its API-surface half — `Level`, `Task`, `Opcode` and
`Keywords` emitted as literal `0` with their fields-map keys silently
dropped, while `Channel` was honoured — described go-evtx **before v0.7.4**,
which honours all five and which cee-exporter pins from v5.1.1 onward.

### Question 2 — does the Description pane show our text?

Select the 4663 record. The Description pane should read *"An attempt was
made to access an object."* followed by the payload.

**First measured 2026-08-10 on winvm**, in the same session as the
registration confirmed above: the saved log opened and all three records
enumerated, but none rendered a description (`LogName=[]`, `Message:
<null>` for all three). That reading was mis-attributed. The `-emit-test-events`
fixture used to generate the file left `ProviderName` empty — something no
event `pkg/mapper` ever produces — and this project's own code, not
go-evtx or the saved-log format, was the cause. See the `[5.1.0]` entry in
`CHANGELOG.md` for the isolation on Windows Server 2025 that found it: three
records with `ProviderName` set read back cleanly under `Get-WinEvent`
regardless of how many `EventData` fields were empty; the same three
records with `ProviderName` empty threw a `NullReferenceException`. The
empty-`Channel` hypothesis this section previously recorded, for *why
descriptions did not render*, was wrong and is not replaced with a new one —
that stays traced to the empty `ProviderName` alone. The empty `Channel`
itself was real and separate: diagnosed and fixed later the same day, see
Question 1 above and the `[5.1.0]` entry in `CHANGELOG.md`.

With `-emit-test-events` fixed to set `ProviderName` (as every real mapped
event already does) and the event source registered on the host, re-measured
2026-08-10 on winvm: the same saved `.evtx` generated on Linux renders all
three descriptions correctly.

```text
saved log (.evtx generated on Linux):
  id 4660  Message: An object was deleted. test-user
  id 4663  Message: An attempt was made to access an object. test-user
  id 4670  Message: Permissions on an object were changed. test-user
```

`LogName` was still empty in this measurement — that turned out not to
matter for description resolution, which is a separate mechanism. The empty
`LogName` was itself a real, separate defect (`windowsEventToFields` dropping
the `Channel` field `pkg/mapper` sets on every event), diagnosed and fixed
later the same day — see Question 1 above and the `[5.1.0]` entry in
`CHANGELOG.md`. Repeating this same saved log after that fix resolves
`LogName=[Security]` instead of `LogName=[]`.

So, as of this section: the file opens, all three records enumerate, all
twelve `EventData` fields carry correct values (the same fields
`evtx-readback`'s `ObjectName` assertion checks one of), descriptions render,
and `LogName` resolves to `Security` — the full set OUT-06 promises.

### Record the outcome

`Qualifiers='2727'` appears in the rendered XML; Windows echoes it back
without objecting, so it is recorded as observed-and-unexplained. Chasing it
would mean changing go-evtx, which is out of scope for this repository.

"Did not investigate" is an acceptable recorded outcome. Silence is not.

| Date | Host | Q1 — opens / placement | Q2 — description | Notes |
|---|---|---|---|---|
| 2026-08-10 | winvm (Windows Server 2025 Datacenter), SSH-only session | Not run — needs GUI access, unreachable over the SSH-only connection available in this session | Renders correctly: all three records enumerate and each shows its own description text (e.g. `An attempt was made to access an object.` for 4663), with the event source registered on the host | An earlier reading the same day recorded all three as `Message: <null>`. That was this project's own `-emit-test-events` fixture leaving `ProviderName` empty, not a go-evtx or saved-log defect — see the `[5.1.0]` `CHANGELOG.md` entry for the isolation. `LogName` was also empty in that reading, traced separately to `windowsEventToFields` dropping the `Channel` field `pkg/mapper` sets on every event; fixed the same day — measured before/after on the same three records: `LogName=[]` → `LogName=[Security]` |
| 2026-08-10 | winvm (Windows Server 2025 Datacenter), RDP session | Run, and it opens: **Action → Open Saved Log…** places the log under **Saved Logs → released-v5.1.0**, "Number of events: 3", all three IDs {4660, 4663, 4670} listed, no convert prompt | (same as row above — description rendering was already confirmed; this row is scoped to the open/placement question) | Run against the **released v5.1.0 binary downloaded from GitHub**, not a local build. A pre-run prediction — that the Level/Task Category/Keywords columns would render blank, based on `Get-WinEvent`'s `*DisplayName` properties returning empty strings headlessly — was falsified: Event Viewer supplies its own defaults (`Information`/`None`/`Info`/`None`) for the zero values those properties reflect; see the falsified-prediction paragraph above for why the headless properties mislead. `User: N/A` is expected (the `-emit-test-events` fixture leaves `SubjectUserSid` empty, not a defect); `Computer: mbp-fj.local` correctly names the generating host, not `winvm`. With this row, both Q1 and Q2 are answered — see `CHANGELOG.md`'s Unreleased entry and `docs/PROMISES.md`/`docs/requirements.md`/`docs/PRD.md`'s OUT-06 rows, all moved to **Verified** |

## Cleanup

Every file copied to the VM and the registry key created by these steps must
be removed afterward and their absence verified — do not leave `winverify`
directories or a `PowerStore-CEPA` registration on a shared VM:

```powershell
Remove-Item -Recurse -Force C:\Windows\Temp\winverify
Remove-Item -Recurse -Force C:\evtxman
Remove-Item -Force C:\manual.evtx
reg delete "HKLM\SYSTEM\CurrentControlSet\Services\EventLog\Application\PowerStore-CEPA" /f
Test-Path C:\Windows\Temp\winverify   # expect False
Test-Path C:\evtxman   # expect False
Test-Path C:\manual.evtx   # expect False
Test-Path "HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\PowerStore-CEPA"   # expect False
```

If step 4 used the ACL-deny substitute above, `reg delete` will itself fail
with access denied on the key it needs to remove — the deny rule blocks
cleanup the same way it blocked the exporter. Take ownership first (via
`SeTakeOwnershipPrivilege`), replace the DACL with an explicit
`Allow(Administrators, FullControl)` rule (removing the deny rule and
leaving an *empty* DACL is not enough — an empty explicit DACL grants nobody
access, it does not fall back to inheriting the parent key's permissions),
then delete.

All of the above was executed and verified gone on `winvm` on 2026-08-09:
the registry key, the `winverify` directory, and the non-administrator test
user created for the attempted cross-user step.
