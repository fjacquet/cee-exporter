# cee-exporter

[![CI](https://github.com/fjacquet/cee-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/fjacquet/cee-exporter/actions/workflows/ci.yml)
[![Release](https://github.com/fjacquet/cee-exporter/actions/workflows/release.yml/badge.svg)](https://github.com/fjacquet/cee-exporter/actions/workflows/release.yml)
[![Docs](https://github.com/fjacquet/cee-exporter/actions/workflows/docs.yml/badge.svg)](https://github.com/fjacquet/cee-exporter/actions/workflows/docs.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/fjacquet/cee-exporter)](https://goreportcard.com/report/github.com/fjacquet/cee-exporter)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Go daemon that receives Dell PowerStore CEPA audit events (HTTP PUT / XML) and forwards them as GELF to Graylog or as native Windows EventLog entries. No external dependencies — single static binary.

## Features

- CEPA protocol compliance — RegisterRequest handshake, heartbeat ACK within 3 s
- GELF 1.1 output over UDP or TCP → Graylog (Linux primary path)
- Win32 EventLog via `ReportEvent` API on Windows
- Multi-target fan-out: write to multiple backends simultaneously
- HTTPS/TLS listener with certificate expiry warnings
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

Requires Go 1.21+, no CGO.

```bash
make build          # Linux/amd64 → ./cee-exporter
make build-windows  # Windows/amd64 → ./cee-exporter.exe
make test
make lint
```

## Documentation

Full operator guide — config reference, TLS setup, CEPA registration, troubleshooting:

**[fjacquet.github.io/cee-exporter](https://fjacquet.github.io/cee-exporter/)**

## License

See [LICENSE](LICENSE).
