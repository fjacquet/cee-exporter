# Phase 2: Build - Research

**Researched:** 2026-03-02
**Domain:** Go Makefile, cross-compilation (Linux + Windows), go vet, go test
**Confidence:** HIGH

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-----------------|
| BUILD-01 | Makefile with `build`, `build-windows`, `test`, `lint`, `clean` targets | Makefile pattern with PHONY targets, variable declarations, and platform-specific build env vars documented below |
| BUILD-02 | Cross-compiled Windows binary (`GOOS=windows GOARCH=amd64`) produced by `make build-windows` | Pure-Go cross-compilation confirmed: `golang.org/x/sys/windows` has no CGO, so `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` works from any Linux host |
</phase_requirements>

---

## Summary

Phase 2 delivers a Makefile that gives any developer or CI system a single entry point for building Linux and Windows binaries of `cee-exporter`. The project is pure Go (`go.mod` uses only `github.com/BurntSushi/toml` and `golang.org/x/sys`). Both dependencies are CGO-free, which means cross-compiling to Windows requires nothing beyond the standard Go toolchain — no MinGW, no Zig, no Docker.

The critical fact to verify before planning was whether `golang.org/x/sys/windows/svc/eventlog` requires CGO. It does not: both `log.go` and `install.go` carry `//go:build windows` constraints with no `import "C"` statement (verified against the upstream golang/sys repository). This makes `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build` the complete and correct cross-compile command.

The `make lint` requirement is `go vet ./...` with zero warnings (stated in BUILD-01 success criteria). This is a deliberate minimal choice — no golangci-lint or staticcheck installation required, keeping the build self-contained. The Makefile should follow standard Go project conventions: tab-indented recipes, `.PHONY` declarations, variables for binary names and paths, and `-o` output flags targeting a consistent location.

**Primary recommendation:** Write a conventional Go Makefile with five targets — `build`, `build-windows`, `test`, `lint`, `clean` — using `CGO_ENABLED=0` for both platform targets and `./cmd/cee-exporter` as the build package path.

---

## Standard Stack

### Core
| Tool | Version | Purpose | Why Standard |
|------|---------|---------|--------------|
| GNU Make | system (3.81+) | Task runner / build orchestration | Universal on Linux/macOS, available in all CI environments |
| Go toolchain | 1.24.0 (from go.mod) | Compiler, cross-compiler, tester, vet | Self-contained: no external tools needed for pure-Go projects |

### Supporting
| Tool | Version | Purpose | When to Use |
|------|---------|---------|-------------|
| `go vet ./...` | built-in | Static analysis (lint target) | Satisfies `make lint` requirement; zero external dependencies |
| `go test ./...` | built-in | Test runner | Satisfies `make test` requirement |
| `-ldflags="-s -w"` | built-in | Strip debug symbols for smaller binary | Production builds; not needed for test/dev |
| `-trimpath` | built-in (Go 1.13+) | Remove absolute paths from binary | Production builds; good for reproducible builds |

### Alternatives Considered
| Standard | Alternative | Tradeoff |
|----------|-------------|----------|
| `go vet ./...` (lint target) | `golangci-lint run` | golangci-lint catches more issues but requires external installation; BUILD-01 requirement says "go vet ./..." explicitly |
| Pure-Go cross-compile (`CGO_ENABLED=0`) | MinGW/Zig cross-compile with CGO | Only needed if CGO is required; this project has no CGO dependencies |
| Explicit `./cmd/cee-exporter` build path | `go build ./...` | `./...` builds all packages but produces no output binary; the cmd path with `-o` is required for a usable binary |

**No installation command needed** — all tools are part of the Go toolchain already in go.mod.

---

## Architecture Patterns

### Recommended Makefile Structure

```makefile
# cee-exporter Makefile

BINARY_NAME    := cee-exporter
BINARY_WINDOWS := cee-exporter.exe
CMD_PATH       := ./cmd/cee-exporter
BUILD_FLAGS    := -trimpath -ldflags="-s -w"

.PHONY: build build-windows test lint clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY_NAME) $(CMD_PATH)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY_WINDOWS) $(CMD_PATH)

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY_NAME) $(BINARY_WINDOWS)
```

### Pattern 1: CGO_ENABLED=0 for Both Targets

**What:** Set `CGO_ENABLED=0` on both the Linux and Windows build targets.
**When to use:** Always, for this project — neither `github.com/BurntSushi/toml` nor `golang.org/x/sys` uses CGO.
**Why:** `CGO_ENABLED=0` produces a statically linked binary with no runtime C dependencies, which is the correct default for a server daemon deployed to arbitrary Linux hosts. For the Windows cross-compile it is required (CGO cannot cross-compile without a matching cross-compiler toolchain).

```makefile
# Source: Go official docs https://go.dev/wiki/WindowsCrossCompiling
build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o cee-exporter.exe ./cmd/cee-exporter
```

### Pattern 2: Explicit cmd Package Path with -o Flag

**What:** Always specify the exact package path (`./cmd/cee-exporter`) and output file (`-o BINARY_NAME`).
**When to use:** Any project following the standard `cmd/<name>/main.go` layout.
**Why:** `go build .` in the Makefile root would fail (no `package main` at root). `go build ./...` compiles all packages but does not produce a named binary. The explicit `-o` flag ensures the binary has the expected name.

```makefile
# Source: Go standard project layout, verified with cmd/go docs
build:
	go build -o cee-exporter ./cmd/cee-exporter
```

### Pattern 3: .PHONY for All Targets

**What:** Declare every target in `.PHONY`.
**When to use:** Any target that does not produce a file matching its target name.
**Why:** Without `.PHONY`, if a file named `build` or `test` exists in the directory, Make will skip running the recipe assuming the target is up-to-date.

```makefile
.PHONY: build build-windows test lint clean
```

### Pattern 4: Platform-Gated Build Tags (Already in Codebase)

**What:** The project already uses `//go:build windows` and `//go:build !windows` constraints.
**When to use:** Cross-compilation will respect these automatically — no special Makefile handling required.
**Why:** Go's build system already gates `pkg/evtx/writer_windows.go` (Win32 API) behind `//go:build windows`. When `GOOS=linux` it is excluded; when `GOOS=windows` it is included. The Makefile just sets the env var.

### Anti-Patterns to Avoid
- **`go build ./...` without `-o`:** Compiles everything but produces no named binary; silent non-failure makes this confusing.
- **Omitting `CGO_ENABLED=0` on the Windows target:** Cross-compilation silently disables CGO anyway, but being explicit documents intent and prevents future breakage if a CGO dependency is accidentally added.
- **Using spaces instead of tabs in Makefile recipes:** Make requires tabs. This is the single most common Makefile beginner error — it produces the cryptic `Makefile:N: *** missing separator. Stop.` error.
- **Running `go vet` only on the native platform:** The lint target should run on the Linux host. Windows-specific packages are tagged and will be skipped correctly; no need to `GOOS=windows go vet`.
- **Not listing `BINARY_WINDOWS` in clean:** Leaving `.exe` files behind causes confusion in cross-platform CI environments.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Windows cross-compilation | Custom shell script calling MinGW | `GOOS=windows GOARCH=amd64 go build` | Go toolchain supports this natively for pure-Go projects |
| Version injection | Sed or manual file edits | `-ldflags="-X main.version=$(git describe)"` | ldflags are the standard Go mechanism; side-effect-free |
| Binary stripping | `strip` command post-build | `-ldflags="-s -w"` | Built into go build; platform-agnostic including Windows |

**Key insight:** For pure-Go projects, the Go toolchain is the complete build system. A Makefile is a thin task wrapper, not a build orchestration layer.

---

## Common Pitfalls

### Pitfall 1: Tab vs. Space in Makefile Recipe
**What goes wrong:** `Makefile:N: *** missing separator. Stop.` — build fails with a cryptic error.
**Why it happens:** Editors that auto-convert tabs to spaces, or copy-pasting Makefile content from web sources.
**How to avoid:** Verify with `cat -A Makefile | grep "^I"` (tab shows as `^I`). Configure editor to use literal tabs in `.mk` files.
**Warning signs:** Build works in one environment but fails in another (different editor settings).

### Pitfall 2: Forgetting `CGO_ENABLED=0` Still Needed on Linux Target
**What goes wrong:** On some Linux systems the C toolchain is absent (Alpine, minimal Docker images). `go build` with CGO implicitly enabled will fail with `gcc not found`.
**Why it happens:** CGO is enabled by default on native builds. The project has no CGO code, but the Go toolchain checks for a C compiler anyway unless disabled.
**How to avoid:** Set `CGO_ENABLED=0` on the Linux build target too — this project is pure Go.
**Warning signs:** Build passes on developer workstation but fails in CI Docker container.

### Pitfall 3: `go vet ./...` Runs Windows-Tagged Files on Linux
**What goes wrong:** Confusion about whether `pkg/evtx/writer_windows.go` is vetted. It is NOT — build tags exclude it.
**Why it happens:** `//go:build windows` means the file is only compiled when `GOOS=windows`. On a Linux host, `go vet ./...` skips it.
**How to avoid:** This is correct behavior. Accept that the Windows-specific code path is not vetted on Linux. Cross-vet (`GOOS=windows go vet ./...`) is optional but not required by BUILD-01.
**Warning signs:** None — this is intentional. Only matters if Windows-only code contains vet-detectable bugs.

### Pitfall 4: Make Target Named `build` Conflicts with File Named `build`
**What goes wrong:** `make build` succeeds silently but does nothing if a file/directory named `build` exists in the project root.
**Why it happens:** Make interprets target names as file names by default. If the file is newer than its dependencies, the recipe is skipped.
**How to avoid:** Always declare `build` in `.PHONY`.
**Warning signs:** `make build` produces no output, no errors, and no binary.

### Pitfall 5: `go build ./cmd/cee-exporter` Works but `go build ./cmd/cee-exporter/` (trailing slash) May Not
**What goes wrong:** Some Make invocations or shell variables append a trailing slash. Go's toolchain behavior with trailing slashes is implementation-defined.
**Why it happens:** Variable interpolation in Makefiles.
**How to avoid:** Use `./cmd/cee-exporter` (no trailing slash) as the canonical path.

### Pitfall 6: `GOOS=windows go vet ./...` Requires Windows SDK Headers for CGO
**What goes wrong:** If someone tries to run `GOOS=windows go vet ./...` from Linux with CGO-using code, it fails.
**Why it happens:** Vet triggers compilation. For CGO code, it needs the cross-toolchain.
**How to avoid:** This project has no CGO. If CGO is ever introduced, this will break cross-vet.
**Warning signs:** vet errors mentioning C include files.

---

## Code Examples

Verified patterns from official sources and project inspection:

### Complete Makefile for This Project

```makefile
# Source: Go official docs (go.dev/wiki/WindowsCrossCompiling),
# earthly.dev/blog/golang-makefile, sohlich.github.io/post/go_makefile

BINARY_NAME    := cee-exporter
BINARY_WINDOWS := cee-exporter.exe
CMD_PATH       := ./cmd/cee-exporter
LDFLAGS        := -s -w

.PHONY: build build-windows test lint clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) $(CMD_PATH)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_WINDOWS) $(CMD_PATH)

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY_NAME) $(BINARY_WINDOWS)
```

### Verify Linux Binary Runs and Exits
```bash
# After make build
./cee-exporter --help 2>&1; echo "Exit: $?"
# Should print usage and exit 0 (or 2 for --help with flag package)
# Must NOT produce "exec format error" or missing library errors
```

### Verify Windows Binary Was Produced
```bash
# After make build-windows
file cee-exporter.exe
# Expected: PE32+ executable (console) x86-64, for MS Windows
```

### go vet Clean Verification
```bash
# After make lint (should produce no output on success)
go vet ./...
echo "Exit: $?"  # Must be 0
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Manual `go build` per platform | `make build` / `make build-windows` with env vars | Go 1.5 (2015) | Cross-compile is built-in; no toolchain installation |
| `golint` (deprecated) | `go vet` + `staticcheck` or `golangci-lint` | 2021 | golint removed; vet is the built-in baseline |
| `//go:build` before Go 1.17 used `// +build` | `//go:build` directive | Go 1.17 (2021) | Codebase already uses new syntax correctly |
| `CGO_ENABLED` was implicit | Explicit `CGO_ENABLED=0` in Makefile | Always recommended | Prevents accidental C-dependency introduction |

**Deprecated/outdated:**
- `golint`: Archived in 2022; replaced by `staticcheck` or `golangci-lint`. Not relevant here since BUILD-01 specifies `go vet`.
- `// +build` constraint syntax: Replaced by `//go:build` in Go 1.17. The project already uses the new form.

---

## Open Questions

1. **Should `make build` target the host platform or always force linux/amd64?**
   - What we know: Developers may be on macOS or Linux. The success criterion says "Linux `cee-exporter` binary that runs and exits cleanly."
   - What's unclear: Whether developers need a native-arch binary for local testing.
   - Recommendation: Always emit `GOOS=linux GOARCH=amd64` from `make build` to match the success criterion exactly. A developer can run `go run ./cmd/cee-exporter` for local testing. This keeps the Makefile deterministic.

2. **Should `make test` include the race detector?**
   - What we know: BUILD-01 says `go test ./...` with zero warnings. Race detector (`-race`) adds value but requires CGO (it uses a C runtime).
   - What's unclear: Whether CI will have CGO available. With `CGO_ENABLED=0`, `-race` cannot be used.
   - Recommendation: Use `go test ./...` without `-race` for now (consistent with CGO=0 build posture). Race detection can be added as an optional CI step separately.

3. **Should `make lint` also vet for the Windows target?**
   - What we know: `go vet ./...` on Linux skips `//go:build windows` files.
   - What's unclear: Whether that is acceptable or if Windows-specific code needs separate vetting.
   - Recommendation: Stay with `go vet ./...` as specified in BUILD-01. Windows code paths are exercised by tests (GELF writer, queue, parser, mapper all compile cross-platform). A separate `GOOS=windows go vet ./...` step can be added in CI later.

---

## Sources

### Primary (HIGH confidence)
- Go official wiki: https://go.dev/wiki/WindowsCrossCompiling — CGO disabled during cross-compilation, `GOOS=windows GOARCH=amd64 go build` is sufficient for pure Go
- golang/sys GitHub repo: https://github.com/golang/sys/blob/master/windows/svc/eventlog/log.go — confirmed `//go:build windows`, no `import "C"`
- golang/sys GitHub repo: https://github.com/golang/sys/blob/master/windows/svc/eventlog/install.go — confirmed `//go:build windows`, no `import "C"`
- Project go.mod (read directly): `go 1.24.0`, dependencies are `BurntSushi/toml` and `golang.org/x/sys` — both pure Go

### Secondary (MEDIUM confidence)
- https://earthly.dev/blog/golang-makefile/ — Makefile structure for Go, verified against Go toolchain docs
- https://sohlich.github.io/post/go_makefile/ — Go Makefile patterns including cross-compilation variables
- https://www.alexedwards.net/blog/a-time-saving-makefile-for-your-go-projects — `main_package_path` pattern, `go vet` + `staticcheck` audit target

### Tertiary (LOW confidence)
- https://dasroot.net/posts/2026/03/go-cross-platform-builds-docker-github-actions/ — general 2026 cross-platform build patterns (single-source, not deeply verified)

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — Go toolchain is the only dependency; verified against go.mod and official docs
- Architecture: HIGH — Makefile pattern is well-established; CGO status verified against upstream source
- Pitfalls: HIGH for tab/CGO/PHONY (classic, well-documented); MEDIUM for Windows-vet gap (project-specific)

**Research date:** 2026-03-02
**Valid until:** 2026-09-02 (Go cross-compilation mechanics are stable; Makefile conventions do not change rapidly)
