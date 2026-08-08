<p align="center">
  <img src="public/logo.svg" alt="cee-exporter logo" width="120" height="120">
</p>

# cee-exporter

[![CI](https://github.com/fjacquet/cee-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/fjacquet/cee-exporter/actions/workflows/ci.yml)
[![Release](https://github.com/fjacquet/cee-exporter/actions/workflows/release.yml/badge.svg)](https://github.com/fjacquet/cee-exporter/actions/workflows/release.yml)
[![Docs](https://github.com/fjacquet/cee-exporter/actions/workflows/docs.yml/badge.svg)](https://github.com/fjacquet/cee-exporter/actions/workflows/docs.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/fjacquet/cee-exporter)](https://goreportcard.com/report/github.com/fjacquet/cee-exporter)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Go daemon that receives Dell PowerStore CEPA audit events (HTTP PUT / XML) and forwards them to a SIEM (GELF, syslog, or Beats) or writes them as Windows EventLog entries — native `ReportEvent` calls on Windows, binary `.evtx` files everywhere else. No external dependencies — single static binary.

## Features

- CEPA protocol compliance — RegisterRequest handshake, heartbeat ACK within 3 s
- GELF 1.1 output over UDP or TCP → Graylog (Linux primary path)
- RFC 5424 syslog output over UDP or TCP
- Beats/Lumberjack v2 output → Logstash or Graylog, with optional TLS
- Win32 EventLog via `ReportEvent` API on Windows; native `.evtx` files on other platforms
- Multi-target fan-out: write to multiple backends simultaneously
- TLS listener with four modes: off, manual (operator-supplied cert), ACME (automatic Let's Encrypt), and self-signed (runtime-generated, air-gapped-friendly); certificate expiry surfaced via `/health` on every request, with a warning logged when fewer than 30 days remain
- Prometheus `/metrics` endpoint on a dedicated port (default 9228)
- Windows Service registration (`cee-exporter.exe install`) and a hardened Linux systemd unit
- Async queue — ACKs the HTTP request immediately, processes events in background
- Structured JSON logging (`slog`) and `/health` endpoint

## Quick Start

**Docker (recommended):**

```bash
docker run -d --name cee-exporter \
  -p 12228:12228 \
  -v ./config.toml:/etc/cee-exporter/config.toml:ro \
  ghcr.io/fjacquet/cee-exporter:latest
```

**Binary:**

```bash
# Download from GitHub Releases, then:
./cee-exporter -config config.toml
```

Minimal `config.toml`:

```toml
[output]
type          = "gelf"
gelf_host     = "192.168.1.50"   # your Graylog IP
gelf_port     = 12201
gelf_protocol = "tcp"            # use tcp for production

[listen]
addr = "0.0.0.0:12228"
```

Check health:

```bash
curl http://localhost:12228/health
```

## Building from Source

Requires Go 1.26.5, no CGO.

```bash
make build-linux    # Linux/amd64   → ./cee-exporter
make build-windows  # Windows/amd64 → ./cee-exporter.exe
make build-darwin   # macOS/native  → ./cee-exporter-darwin
make test
make lint
```

(`make build` alone runs `go build -v ./...` to check compilation — it produces no binary.)

## Documentation

Full operator guide — config reference, TLS setup, CEPA registration, troubleshooting:

**[fjacquet.github.io/cee-exporter](https://fjacquet.github.io/cee-exporter/)**

## License

See [LICENSE](LICENSE).
