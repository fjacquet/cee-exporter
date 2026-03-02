# Roadmap: cee-exporter

## Overview

The core pipeline of cee-exporter is already implemented and compiles cleanly. This roadmap covers the three remaining phases needed to ship v1.0: hardening the code with unit tests and a bug fix, wrapping the build with a proper Makefile and cross-compile target, and writing the README so operators can install and configure the daemon without reading source code.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Quality** - Fix readBody bug and add unit tests for parser, mapper, queue, and GELF payload builder (completed 2026-03-02)
- [ ] **Phase 2: Build** - Makefile with build, test, lint, and cross-compile targets; Windows binary verified
- [ ] **Phase 3: Documentation** - README with quickstart, config reference, TLS setup, and CEPA registration

## Phase Details

### Phase 1: Quality
**Goal**: The codebase is safe from known panics and the core packages are verifiably correct via automated tests
**Depends on**: Nothing (first phase — core pipeline already implemented)
**Requirements**: QUAL-01, QUAL-02, QUAL-03, QUAL-04, QUAL-05
**Success Criteria** (what must be TRUE):
  1. `go test ./...` passes with no failures or panics on Linux
  2. The readBody function does not panic when a payload exceeds 64 MiB (nil ResponseWriter bug fixed)
  3. Parser tests cover single-event XML, VCAPS batch XML, malformed input, and RegisterRequest detection
  4. Mapper tests verify all 6 CEPA event types produce correct Windows EventID and access mask
  5. Queue tests confirm enqueue, drop-on-full, and drain-on-stop behaviour
**Plans**: 3 plans

Plans:
- [x] 01-01-PLAN.md — Fix readBody nil ResponseWriter panic and add regression test
- [x] 01-02-PLAN.md — Write unit tests for parser and mapper packages
- [ ] 01-03-PLAN.md — Write unit tests for queue and GELF payload builder

### Phase 2: Build
**Goal**: Any developer or CI system can build a Linux binary and a cross-compiled Windows binary with a single make command
**Depends on**: Phase 1
**Requirements**: BUILD-01, BUILD-02
**Success Criteria** (what must be TRUE):
  1. `make build` produces a Linux `cee-exporter` binary that runs and exits cleanly
  2. `make build-windows` produces a `cee-exporter.exe` binary for GOOS=windows/GOARCH=amd64 without error
  3. `make test` runs `go test ./...` and `make lint` runs `go vet ./...` with zero warnings
**Plans**: TBD

Plans:
- [ ] 02-01: Write Makefile with build, build-windows, test, lint, clean targets (BUILD-01, BUILD-02)

### Phase 3: Documentation
**Goal**: An operator unfamiliar with the codebase can install, configure, and connect cee-exporter to Graylog and a Dell PowerStore CEPA publisher using only the README
**Depends on**: Phase 2
**Requirements**: DOC-01, DOC-02, DOC-03, DOC-04
**Success Criteria** (what must be TRUE):
  1. README contains a working quickstart that takes the operator from binary to receiving GELF events in Graylog
  2. README documents every config.toml field with type, default, and example value
  3. README shows how to generate a self-signed TLS certificate and configure cee-exporter to use it
  4. README explains how to configure a Dell PowerStore Event Publishing Pool to send events to cee-exporter
**Plans**: TBD

Plans:
- [ ] 03-01: Write README quickstart and config reference sections (DOC-01, DOC-02)
- [ ] 03-02: Write README TLS setup and CEPA registration sections (DOC-03, DOC-04)

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Quality | 3/3 | Complete   | 2026-03-02 |
| 2. Build | 0/1 | Not started | - |
| 3. Documentation | 0/2 | Not started | - |
