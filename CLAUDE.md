# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build          # Linux/amd64 binary → ./cee-exporter (CGO_ENABLED=0, static)
make build-windows  # Windows/amd64 binary → ./cee-exporter.exe
make test           # go test ./...
make lint           # go vet ./...
make clean          # remove both binaries

make docker-build   # build ghcr.io/fjacquet/cee-exporter:VERSION
make docker-push    # build + push to GHCR
make docker-run     # run container with ./config.toml mounted

# Single test
go test ./pkg/server/ -run TestReadBodyOversized

# With race detector (requires CGO — not available via make test)
go test -race ./...

# Full linter (project uses golangci-lint in addition to go vet)
golangci-lint run
```

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
  - `writer_windows.go` — Win32 `ReportEvent` API (`//go:build windows`)
  - `writer_evtx_stub.go` — `BinaryEvtxWriter` placeholder, no output (`//go:build !windows`)
  - `writer_multi.go` — fan-out to multiple backends
  - `writer_native_windows.go` / `writer_native_notwindows.go` — `NewNativeEvtxWriter` platform factory
- **`pkg/metrics`** — atomic in-process counters (events received/written/dropped).
- **`pkg/log`** — slog initialisation.

## Platform file naming

**Never** use a `_linux.go` suffix — Go treats that as Linux-only. For non-Windows files use the `_notwindows.go` suffix with `//go:build !windows`. Windows-only files use `_windows.go` with `//go:build windows`.

## Testing conventions

- **White-box tests** — test files declare the same package as the code under test (e.g. `package server` in `server_test.go`) to access unexported symbols.
- **stdlib only** — no testify or external test libraries; `go.mod` has no test dependencies.
- **Table-driven** with `t.Run` for all multi-case tests.
- **No `time.Sleep`** for synchronisation in queue tests — use channel signals or `Stop()` drain guarantees.
- **Global state isolation** — reset `metrics.M` atomic counters before tests that assert on them.

## CEPA protocol constraints

- RegisterRequest handshake: HTTP 200 OK, **empty body** (enforced in `server.go`).
- Heartbeat PUT timeout: 3 seconds — `ServeHTTP` must return before processing completes.
- VCAPS mode: batches of thousands of events per PUT; use `gelf_protocol = "tcp"` to avoid UDP loss.

## CGO and static linking

All targets set `CGO_ENABLED=0`. Consequences:

- `-race` detector cannot be used in `make test` (requires CGO); run `go test -race ./...` separately when needed.
- Cross-compilation from Linux to Windows requires no C toolchain.
- `golang.org/x/sys/windows` uses syscall (not CGO) — Win32 API calls work without a C compiler.

## Docker

Final image is `scratch` (binary + CA certs only). Mount config at `/etc/cee-exporter/config.toml`. Image is published to `ghcr.io/fjacquet/cee-exporter`.

## GitHub Actions

- `ci.yml` — test + lint + build (Linux + Windows) on every push/PR to `main`.
- `docs.yml` — deploys mkdocs-material site to `gh-pages` on docs/README changes.
- `release.yml` — triggered by `v*` tags: builds binaries, pushes Docker image to GHCR, creates GitHub Release with attached archives.
