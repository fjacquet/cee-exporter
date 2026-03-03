# ADR-005: Disable CGO for all build targets (CGO_ENABLED=0)

**Status:** accepted

## Context

Go's CGO facility allows calling C code from Go, but it introduces several complications:
cross-compilation from Linux to Windows requires a Windows-capable C cross-compiler
(mingw-w64); CGO binaries are dynamically linked against glibc by default, creating
runtime dependency on the host's C library version; and the -race detector requires CGO,
conflicting with our static-linking posture. The Win32 EventLog API calls use
golang.org/x/sys/windows which uses syscall (not CGO) on Windows.

## Decision

We will set CGO_ENABLED=0 for all build targets, including both the Linux and Windows
binaries. All system calls will use Go's syscall package and golang.org/x/sys, not CGO.

## Consequences

All binaries are fully statically linked with no C library dependency. Cross-compilation
from Linux to Windows/amd64 works without a Windows C cross-compiler. Binary size is
slightly larger than a dynamically-linked build but deployment is simpler (copy binary,
run). The trade-off is that CGO-dependent libraries cannot be used; this is acceptable
because no CGO dependencies exist in the current codebase. The -race detector cannot
be used in test targets (documented in Makefile comment and STATE.md).
