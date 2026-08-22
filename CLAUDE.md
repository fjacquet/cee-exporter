# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Requires Go 1.26.5 (see `go.mod`).

The canonical targets are the `fjacquet/ci` standard interface — the reusable
CI workflows call these names, so keep their behaviour stable.

```bash
make ci             # lint + test + build + vuln — the gate CI runs. Use this.
make lint           # golangci-lint run --timeout=5m  (NOT go vet); also fails on unformatted code
make test           # go test -race -coverprofile=coverage.out ./...
make build          # go build -v ./...  — compiles, produces NO binary
make format         # golangci-lint fmt — the fix for what `make lint` reports
make vuln           # govulncheck ./...
make docs           # mkdocs build --strict  (fails on any broken doc link)
make sbom           # CycloneDX SBOM → dist/
make release        # goreleaser release --clean
```

Binaries come from the repo-specific targets, not `make build`:

```bash
make build-linux    # Linux/amd64   → ./cee-exporter        (CGO_ENABLED=0, static)
make build-windows  # Windows/amd64 → ./cee-exporter.exe
make build-darwin   # macOS/native  → ./cee-exporter-darwin
make clean          # remove dist/, site/, coverage, *.sarif and all three binaries
```

```bash
make docker-build   # build ghcr.io/fjacquet/cee-exporter:VERSION locally
make docker-push    # local convenience only — releases publish via goreleaser
make docker-run     # run container with ./config.toml mounted
make install-systemd  # sudo; installs the unit (DynamicUser, no useradd needed)

# Single test
go test ./pkg/server/ -run TestReadBodyOversized
```

All build targets stamp the version: `-ldflags "-X main.version=$(VERSION)"`.
A binary reporting `dev` means the stamp did not reach it.

## Architecture

The pipeline is linear:

```text
CEPA HTTP PUT → pkg/server → pkg/parser → pkg/mapper → pkg/queue → pkg/evtx (writers)
```

- **`cmd/cee-exporter/main.go`** — wires config → writer → queue → HTTP server → signal handling. Config is TOML (`-config config.toml`); `CEE_LOG_LEVEL` and `CEE_LOG_FORMAT` env vars override the file.
- **`pkg/server`** — HTTP handler. `ServeHTTP` ACKs immediately (CEPA requires response within 3 s); delegates event processing to the queue. `RegisterRequest` must respond HTTP 200 with **strictly empty body** — any XML body is a fatal CEPA error.
- **`pkg/parser`** — CEE XML → `[]CEPAEvent`. Handles both single-event and VCAPS batch (`<EventBatch>`) payloads.
- **`pkg/mapper`** — `CEPAEvent` → `WindowsEvent` (CEPA event type → Windows EventID + access mask).
- **`pkg/queue`** — buffered channel + worker goroutines. Drops events with WARN log when full; exposes depth via `/health`.
- **`pkg/evtx`** — writer backends behind the `Writer` interface:
  - `writer_gelf.go` — GELF 1.1 JSON over UDP or TCP (all platforms)
  - `writer_syslog.go` — RFC 5424 syslog over UDP or TCP (all platforms)
  - `writer_beats.go` — Elastic Beats protocol over TCP (all platforms)
  - `writer_windows.go` — Win32 `ReportEvent` API (`//go:build windows`)
  - `writer_evtx_notwindows.go` — `BinaryEvtxWriter` (`//go:build !windows`). A thin adapter: all EVTX BinXML encoding lives in the external `github.com/fjacquet/go-evtx` library (see ADR-014). This file only maps `WindowsEvent` to `map[string]string` and forwards.
  - `writer_multi.go` — fan-out to multiple backends; forwards `Rotate()` to backends that support it
  - `writer_native_windows.go` / `writer_native_notwindows.go` — `NewNativeEvtxWriter` platform factory
  - Network writers share helpers in `writer.go`: `hostPort` (IPv6-safe host:port), `ShortMessage` (standard message format), `sendWithRetry` (reconnect-once retry loop)
- **`pkg/metrics`** — atomic in-process counters (events received/written/dropped/truncated, writer errors, queue depth, last fsync). Package-level singleton `metrics.M`.
- **`pkg/prometheus`** — `/metrics` endpoint on its own port (default `0.0.0.0:9228`, see ADR-006). Uses a private registry so Go runtime metrics stay out of the scrape.
- **`pkg/server/health.go`** — `GET /health` JSON status, including queue depth.
- **`pkg/log`** — slog initialisation.

The only non-stdlib runtime dependency in the event path is
`github.com/fjacquet/go-evtx`, which owns the EVTX binary format. Format bugs
are fixed there and consumed here via a version bump — see ADR-014.

## Platform file naming

**Never** use a `_linux.go` suffix — Go treats that as Linux-only. For non-Windows files use the `_notwindows.go` suffix with `//go:build !windows`. Windows-only files use `_windows.go` with `//go:build windows`.

## Testing conventions

- **White-box tests** — test files declare the same package as the code under test (e.g. `package server` in `server_test.go`) to access unexported symbols.
- **stdlib only** — no testify or external test libraries; `go.mod` has no test dependencies.
- **Table-driven** with `t.Run` for all multi-case tests.
- **No `time.Sleep`** for synchronisation in queue tests — use channel signals or `Stop()` drain guarantees.
- **Global state isolation** — reset `metrics.M` atomic counters before tests that assert on them.

## Linter gotchas

`.golangci.yml` (v2 format) enables `errcheck`, `govet`, `ineffassign`,
`staticcheck`, `unused`, `misspell`, `errorlint`, `copyloopvar`, `unconvert`
and `nilerr` under `linters:`, plus `gofmt` and `goimports` under a separate
`formatters:` section. Three things bite most often:

- **`errorlint` runs with `comparison: true`** — never compare errors with `==`
  or `!=`, including in tests. Use `errors.Is`. Only `errcheck` is excluded for
  `_test.go`; every other linter applies to test files too. That exclusion also
  makes `//nolint:errcheck` in a test file dead weight — drop it, or write
  `defer func() { _ = f.Close() }()`, which the codebase uses throughout.
- **`nilerr`** — returning `nil` on a path where `err != nil` is a build failure,
  not a warning.
- **Formatters are not linters in v2.** Putting `gofmt` under `linters:` is a
  config error, and omitting the `formatters:` section entirely means
  `golangci-lint run` checks no formatting at all — which is how `make ci` went
  green on unformatted code until v5.0.1. `run` executes formatters in
  check-only mode: it reports, it does not rewrite. `make format` rewrites.
  `issues.max-same-issues` is set to 0 because every gofmt violation carries
  the same message text, so the default cap of 3 would report five unformatted
  files as three.

## CEPA protocol constraints

- RegisterRequest handshake: HTTP 200 OK with a **`<RegisterResponse>` document**
  (`pkg/server/register.go`). This file said "empty body" until 2026-08-22 and
  that was the inverse of the truth — an empty body fails with `Top node is not
  RegisterResponse`, CEE registers no partner, and the array is told `0x16
  CEPP_NOT_FOUND` while every observable stays green.
- **CEE only registers an identity in its compiled-in allowlist** (`CGuidStore`,
  keyed by *(friendlyName, facility)* → GUID). A self-generated GUID never
  works; set `[cepa] friendly_name`/`guid` from that table. See
  cee-worker's `docs/cee-partner-allowlist.md` and `docs/cepa-protocol.md`.
- After registering, CEE probes with `<HeartBeatRequest />` and needs
  `hbStatus=0` (`pkg/server/heartbeat.go`), or the partner stays OFFLINE.
- Mirror the request encoding: CEE sends UTF-16LE to an unknown partner and
  **UTF-8 once the identity is allowlisted**.
- Heartbeat PUT timeout: 3 seconds — `ServeHTTP` must return before processing completes.
- VCAPS mode: batches of thousands of events per PUT; use `gelf_protocol = "tcp"` to avoid UDP loss.

## CGO and static linking

The **binary** targets (`build-linux`, `build-windows`, `build-darwin`) and the
release/Docker builds set `CGO_ENABLED=0`. `make test` does not, which is why it
can and does run `-race`.

- Cross-compilation to Windows requires no C toolchain.
- `golang.org/x/sys/windows` uses syscall (not CGO) — Win32 API calls work without a C compiler.
- The Docker builder image must be at least `golang:1.26-alpine`. The official
  Go images ship `GOTOOLCHAIN=local`, so a builder older than the `go` directive
  in `go.mod` fails unconditionally rather than downloading a toolchain.

## Docker

Final image is `scratch` (binary + CA certs only). Mount config at `/etc/cee-exporter/config.toml`. `ghcr.io/fjacquet/cee-exporter` (`linux/amd64` + `linux/arm64`) is published by the release pipeline via goreleaser's `dockers_v2:` block (`Dockerfile.goreleaser`, which packages the prebuilt binaries) — not by `make docker-push`, which remains a local-only convenience target.

## GitHub Actions

- `ci.yml` — the `ci`/`security` jobs delegate to the reusable `fjacquet/ci` workflows (`go-ci.yml`, `go-security.yml`), which run `make ci` on `ubuntu-24.04` — Linux only, so `//go:build windows` excludes `writer_windows.go`, `service_windows.go`, `sighup_windows.go` and `writer_native_windows.go` from that build entirely; those files are not merely uncompiled-and-discarded, the compiler never sees them there. **Since v5.0 (2026-08-09) there is also a `windows` job**, `runs-on: windows-latest`, triggered the same as the rest plus `workflow_dispatch`. It is the only thing that ever *executes* the Windows-tagged code, not just compiles it: `go test` (`-race` when a C compiler is present — GitHub's `windows-latest` image ships mingw gcc; the job degrades to a plain run with a `::warning::` otherwise, since `-race` needs cgo and cannot be assumed), builds `cee-exporter.exe`, runs it with `-emit-test-events` and asserts via `Get-WinEvent`/`Get-EventLog` that each mapped event ID (4660/4663/4670) renders **its own** message-resource description — not merely a non-null, non-placeholder message, which a resource with a missing or swapped description would also produce — and drives the service through a real SCM lifecycle (`install` → `sc.exe start` → poll for `Running` → `sc.exe stop` → poll for `Stopped` → `uninstall`), the only thing that ever calls `svcProgram.Start`/`Stop`. `release.yml`'s goreleaser run on a `v*` tag and a local `make build-windows` also build these files, but only the `windows` job runs them. Still uncovered: `golangci-lint` never runs with `GOOS=windows` (no CI job lints the Windows-tagged files — a manual run is clean today, but nothing guards it going forward); the actual release artifact (goreleaser's `-trimpath`, `CGO_ENABLED=0`, version-stamped build) differs from the job's own `go build -o cee-exporter.exe`, so nothing executes what ships; and the non-Administrator and genuinely-stale-pre-v5.0-registration paths are manual-only — see `docs/windows-verification.md`.
- `docs.yml` — deploys mkdocs-material to `gh-pages` on docs/README changes. Runs `mkdocs build --strict`, so a single broken internal link fails the build.
- `release.yml` — triggered by `v*` tags: goreleaser builds binaries for linux/darwin/windows × amd64/arm64 and publishes the multi-arch image to GHCR.

<!-- rtk-instructions v2 -->
# RTK (Rust Token Killer) - Token-Optimized Commands

## Golden Rule

**Always prefix commands with `rtk`**. If RTK has a dedicated filter, it uses it. If not, it passes through unchanged. This means RTK is always safe to use.

**Important**: Even in command chains with `&&`, use `rtk`:
```bash
# ❌ Wrong
git add . && git commit -m "msg" && git push

# ✅ Correct
rtk git add . && rtk git commit -m "msg" && rtk git push
```

## RTK Commands by Workflow

### Build & Compile (80-90% savings)
```bash
rtk cargo build         # Cargo build output
rtk cargo check         # Cargo check output
rtk cargo clippy        # Clippy warnings grouped by file (80%)
rtk tsc                 # TypeScript errors grouped by file/code (83%)
rtk lint                # ESLint/Biome violations grouped (84%)
rtk prettier --check    # Files needing format only (70%)
rtk next build          # Next.js build with route metrics (87%)
```

### Test (90-99% savings)
```bash
rtk cargo test          # Cargo test failures only (90%)
rtk vitest run          # Vitest failures only (99.5%)
rtk playwright test     # Playwright failures only (94%)
rtk test <cmd>          # Generic test wrapper - failures only
```

### Git (59-80% savings)
```bash
rtk git status          # Compact status
rtk git log             # Compact log (works with all git flags)
rtk git diff            # Compact diff (80%)
rtk git show            # Compact show (80%)
rtk git add             # Ultra-compact confirmations (59%)
rtk git commit          # Ultra-compact confirmations (59%)
rtk git push            # Ultra-compact confirmations
rtk git pull            # Ultra-compact confirmations
rtk git branch          # Compact branch list
rtk git fetch           # Compact fetch
rtk git stash           # Compact stash
rtk git worktree        # Compact worktree
```

Note: Git passthrough works for ALL subcommands, even those not explicitly listed.

### GitHub (26-87% savings)
```bash
rtk gh pr view <num>    # Compact PR view (87%)
rtk gh pr checks        # Compact PR checks (79%)
rtk gh run list         # Compact workflow runs (82%)
rtk gh issue list       # Compact issue list (80%)
rtk gh api              # Compact API responses (26%)
```

### JavaScript/TypeScript Tooling (70-90% savings)
```bash
rtk pnpm list           # Compact dependency tree (70%)
rtk pnpm outdated       # Compact outdated packages (80%)
rtk pnpm install        # Compact install output (90%)
rtk npm run <script>    # Compact npm script output
rtk npx <cmd>           # Compact npx command output
rtk prisma              # Prisma without ASCII art (88%)
```

### Files & Search (60-75% savings)
```bash
rtk ls <path>           # Tree format, compact (65%)
rtk read <file>         # Code reading with filtering (60%)
rtk grep <pattern>      # Search grouped by file (75%)
rtk find <pattern>      # Find grouped by directory (70%)
```

### Analysis & Debug (70-90% savings)
```bash
rtk err <cmd>           # Filter errors only from any command
rtk log <file>          # Deduplicated logs with counts
rtk json <file>         # JSON structure without values
rtk deps                # Dependency overview
rtk env                 # Environment variables compact
rtk summary <cmd>       # Smart summary of command output
rtk diff                # Ultra-compact diffs
```

### Infrastructure (85% savings)
```bash
rtk docker ps           # Compact container list
rtk docker images       # Compact image list
rtk docker logs <c>     # Deduplicated logs
rtk kubectl get         # Compact resource list
rtk kubectl logs        # Deduplicated pod logs
```

### Network (65-70% savings)
```bash
rtk curl <url>          # Compact HTTP responses (70%)
rtk wget <url>          # Compact download output (65%)
```

### Meta Commands
```bash
rtk gain                # View token savings statistics
rtk gain --history      # View command history with savings
rtk discover            # Analyze Claude Code sessions for missed RTK usage
rtk proxy <cmd>         # Run command without filtering (for debugging)
rtk init                # Add RTK instructions to CLAUDE.md
rtk init --global       # Add RTK to ~/.claude/CLAUDE.md
```

## Token Savings Overview

| Category | Commands | Typical Savings |
|----------|----------|-----------------|
| Tests | vitest, playwright, cargo test | 90-99% |
| Build | next, tsc, lint, prettier | 70-87% |
| Git | status, log, diff, add, commit | 59-80% |
| GitHub | gh pr, gh run, gh issue | 26-87% |
| Package Managers | pnpm, npm, npx | 70-90% |
| Files | ls, read, grep, find | 60-75% |
| Infrastructure | docker, kubectl | 85% |
| Network | curl, wget | 65-70% |

Overall average: **60-90% token reduction** on common development operations.
<!-- /rtk-instructions -->

<!-- code-review-graph MCP tools -->
## MCP Tools: code-review-graph

**IMPORTANT: This project has a knowledge graph. ALWAYS use the
code-review-graph MCP tools BEFORE using Grep/Glob/Read to explore
the codebase.** The graph is faster, cheaper (fewer tokens), and gives
you structural context (callers, dependents, test coverage) that file
scanning cannot.

### When to use graph tools FIRST

- **Exploring code**: `semantic_search_nodes` or `query_graph` instead of Grep
- **Understanding impact**: `get_impact_radius` instead of manually tracing imports
- **Code review**: `detect_changes` + `get_review_context` instead of reading entire files
- **Finding relationships**: `query_graph` with callers_of/callees_of/imports_of/tests_for
- **Architecture questions**: `get_architecture_overview` + `list_communities`

Fall back to Grep/Glob/Read **only** when the graph doesn't cover what you need.

### Key Tools

| Tool | Use when |
|------|----------|
| `detect_changes` | Reviewing code changes — gives risk-scored analysis |
| `get_review_context` | Need source snippets for review — token-efficient |
| `get_impact_radius` | Understanding blast radius of a change |
| `get_affected_flows` | Finding which execution paths are impacted |
| `query_graph` | Tracing callers, callees, imports, tests, dependencies |
| `semantic_search_nodes` | Finding functions/classes by name or keyword |
| `get_architecture_overview` | Understanding high-level codebase structure |
| `refactor_tool` | Planning renames, finding dead code |

### Workflow

1. The graph auto-updates on file changes (via hooks).
2. Use `detect_changes` for code review.
3. Use `get_affected_flows` to understand impact.
4. Use `query_graph` pattern="tests_for" to check coverage.
