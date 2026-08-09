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

## Cleanup

Every file copied to the VM and the registry key created by these steps must
be removed afterward and their absence verified — do not leave `winverify`
directories or a `PowerStore-CEPA` registration on a shared VM:

```powershell
Remove-Item -Recurse -Force C:\Windows\Temp\winverify
reg delete "HKLM\SYSTEM\CurrentControlSet\Services\EventLog\Application\PowerStore-CEPA" /f
Test-Path C:\Windows\Temp\winverify   # expect False
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
