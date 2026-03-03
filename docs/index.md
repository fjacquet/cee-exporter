# cee-exporter

**Dell PowerStore CEPA audit events → Windows EventLog / GELF**

`cee-exporter` is a lightweight Go daemon that receives Dell PowerStore file-system audit events via the CEPA (Common Event Publishing Agent) HTTP protocol and forwards them to a SIEM as native Windows Event Log entries or GELF (Graylog Extended Log Format) messages.

## Quick links

- [**Operator Guide**](operator-guide.md) — installation, configuration, TLS, CEPA registration
- [GitHub repository](https://github.com/fjacquet/cee-exporter)
- [CHANGELOG](https://github.com/fjacquet/cee-exporter/blob/main/CHANGELOG.md)
- [Releases & binaries](https://github.com/fjacquet/cee-exporter/releases)
- [Docker image](https://ghcr.io/fjacquet/cee-exporter)

## Architecture overview

```mermaid
flowchart TD
    PS["Dell PowerStore\n(CEPA HTTP PUT)"]

    subgraph server["pkg/server — :12228"]
        H["Handler\nRegisterRequest handshake\nHeartbeat ACK < 3 s"]
        HH["HealthHandler\nGET /health"]
    end

    subgraph proc["Processing"]
        P["pkg/parser\nCEE XML → []CEPAEvent"]
        M["pkg/mapper\nCEPAEvent → WindowsEvent\n(EventID, access mask)"]
    end

    Q["pkg/queue\nAsync worker pool\n(capacity + workers)"]

    subgraph writers["pkg/evtx — Writers"]
        GW["GELFWriter\nUDP / TCP\nLinux + Windows"]
        WW["Win32Writer\nApplication EventLog\nWindows only"]
        MW["MultiWriter\nfan-out to ≥1 backend"]
    end

    PS --> H
    H --> P
    P --> M
    M --> Q
    Q --> GW
    Q --> WW
    Q --> MW
```

## Key properties

| Property | Value |
|----------|-------|
| Listen port | 12228/TCP (configurable) |
| Default output | GELF UDP → localhost:12201 |
| Binary size | ~6 MB (stripped, CGO_ENABLED=0) |
| Dependencies | None — fully static binary |
| Platforms | Linux/amd64, Windows/amd64 |
| Go version | 1.24+ |

## Documentation

- [**Operator Guide**](operator-guide.md) — installation, all config fields, TLS setup, CEPA registration, troubleshooting
- [**Product Requirements (PRD)**](PRD.md) — problem statement, goals, personas, architecture
- [**Architecture Decision Records**](adr/ADR-001-language-go.md) — key design decisions with context and consequences
