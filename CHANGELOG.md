# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-03-03

### Added
- CEPA HTTP listener with RegisterRequest handshake and heartbeat ACK (3-second window)
- CEE XML parser for single-event and VCAPS bulk batch payloads
- Semantic mapping of 6 CEPA event types to Windows Event IDs (4660, 4663, 4670)
- GELF 1.1 writer over UDP and TCP with automatic TCP reconnection
- Win32 EventLog writer for Windows hosts (ReportEvent API, "PowerStore-CEPA" event source)
- MultiWriter fan-out to multiple output backends simultaneously
- HTTPS/TLS listener with configurable x509 certificate and key
- TLS certificate expiry warning at startup (WARN logged when < 30 days remain)
- GET /health JSON endpoint returning uptime, queue depth, and event counters
- Structured JSON logging via slog (level and format configurable via TOML or env)
- Async worker queue with configurable capacity and worker count
- TOML configuration file with CEE_LOG_LEVEL / CEE_LOG_FORMAT environment variable overrides
- Makefile with build, build-windows, test, lint, clean targets
- Cross-compiled Windows/amd64 binary (CGO_ENABLED=0, GOOS=windows GOARCH=amd64)
- GET /health endpoint with JSON status (uptime, queue_depth, events_received, events_written, events_dropped)

### Fixed
- http.MaxBytesReader nil ResponseWriter panic: payloads > 64 MiB now close the connection gracefully instead of panicking
