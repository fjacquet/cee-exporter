; // cee-exporter Windows event log message resource.
; //
; // Defines message text for the three event IDs pkg/mapper emits. Without
; // this resource, Event Viewer renders "The description for Event ID N from
; // source PowerStore-CEPA cannot be found" and appends the payload as a raw
; // insertion string.
; //
; // Regenerate with `make winres` after editing. The generated .syso is
; // committed; the Go linker picks it up by filename for windows/amd64.

MessageIdTypedef=DWORD

LanguageNames=(English=0x409:MSG00409)

MessageId=4660
SymbolicName=MSG_OBJECT_DELETED
Language=English
An object was deleted.
%1
.

MessageId=4663
SymbolicName=MSG_OBJECT_ACCESS
Language=English
An attempt was made to access an object.
%1
.

MessageId=4670
SymbolicName=MSG_PERMISSIONS_CHANGED
Language=English
Permissions on an object were changed.
%1
.
