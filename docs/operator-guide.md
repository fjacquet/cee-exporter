# Operator Guide

This guide covers installation, configuration, TLS setup, and PowerStore CEPA registration.

## Installation

### Docker (recommended)

```bash
docker run -d --name cee-exporter \
  -p 12228:12228 \
  -v ./config.toml:/etc/cee-exporter/config.toml:ro \
  ghcr.io/fjacquet/cee-exporter:latest
```

Verify it is running:

```bash
curl http://localhost:12228/health
```

### Binary

Download the latest release for your platform from the [GitHub Releases](https://github.com/fjacquet/cee-exporter/releases) page.

```bash
# Linux
chmod +x cee-exporter
./cee-exporter -config config.toml

# Windows (run as Administrator for Win32 EventLog output)
cee-exporter.exe -config config.toml
```

### Build from source

Requires Go 1.21+. No CGO required.

```bash
git clone https://github.com/fjacquet/cee-exporter.git
cd cee-exporter

make build          # Linux/amd64  → ./cee-exporter
make build-windows  # Windows/amd64 → ./cee-exporter.exe
```

---

## Configuration reference

All configuration is stored in a TOML file (default: `config.toml`).

Two environment variables override the config file:

| Variable | Values | Default |
|----------|--------|---------|
| `CEE_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `CEE_LOG_FORMAT` | `json`, `text` | `json` |

### Minimal config (GELF output)

```toml
[output]
type          = "gelf"
gelf_host     = "192.168.1.50"   # your Graylog IP
gelf_port     = 12201
gelf_protocol = "tcp"

[listen]
addr = "0.0.0.0:12228"
```

### Full config reference

```toml
# Optional: override the hostname embedded in every event.
# Defaults to os.Hostname() if not set.
hostname = ""

[listen]
addr      = "0.0.0.0:12228"  # TCP address and port to listen on
tls       = false             # Enable HTTPS/TLS
cert_file = ""                # Path to TLS certificate (PEM)
key_file  = ""                # Path to TLS private key (PEM)

[output]
type          = "gelf"        # Output type: "gelf" | "evtx" | "multi"
targets       = []            # For type="multi": list of types to fan-out to
evtx_path     = ""            # For type="evtx": path to .evtx output directory
gelf_host     = "localhost"   # GELF receiver hostname/IP
gelf_port     = 12201         # GELF receiver port
gelf_protocol = "udp"         # "tcp" or "udp"
gelf_tls      = false         # Wrap TCP in TLS (requires gelf_protocol = "tcp")

[queue]
capacity = 100000             # Maximum events buffered in memory
workers  = 4                  # Concurrent writer goroutines

[logging]
level  = "info"               # debug | info | warn | error
format = "json"               # json | text
```

### Output types

| Type | Description |
|------|-------------|
| `gelf` | GELF 1.1 JSON over UDP or TCP → Graylog (works on all platforms) |
| `evtx` | Write to Windows Event Log via Win32 `ReportEvent` API (Windows only) |
| `multi` | Fan-out to multiple backends simultaneously |

### Multi-target example

```toml
[output]
type    = "multi"
targets = ["gelf", "evtx"]

gelf_host     = "192.168.1.50"
gelf_port     = 12201
gelf_protocol = "tcp"
```

---

## TLS / HTTPS setup

Production deployments must use HTTPS to protect audit data in transit. The certificate must include the daemon's hostname as a Subject Alternative Name (SAN) — PowerStore validates the SAN, not the Common Name.

### Step 1 — Generate a self-signed SAN certificate

```bash
openssl req -x509 -nodes -newkey rsa:4096 \
  -keyout server.key \
  -out server.crt \
  -days 825 \
  -subj "/CN=cee-exporter" \
  -addext "subjectAltName=DNS:cee-exporter.example.com,IP:192.168.1.10"
```

Replace `cee-exporter.example.com` and `192.168.1.10` with the actual DNS name and IP address that PowerStore will use to reach this host.

### Step 2 — Enable TLS in config

```toml
[listen]
addr      = "0.0.0.0:12228"
tls       = true
cert_file = "/etc/cee-exporter/server.crt"
key_file  = "/etc/cee-exporter/server.key"
```

### Step 3 — Import the certificate into PowerStore

PowerStore must trust the certificate. Upload `server.crt` to PowerStore's trusted certificate store before configuring CEPA. See your PowerStore documentation for the certificate import procedure.

---

## Registering with PowerStore CEPA

CEPA (Common Event Publishing Agent) is the PowerStore mechanism that sends file-system audit events to external consumers over HTTP.

### Prerequisites

- cee-exporter is running and reachable from the PowerStore management network
- If using TLS, the certificate is imported into PowerStore
- Port 12228/TCP is open between PowerStore and the cee-exporter host

### Registration steps

1. Log in to the PowerStore Manager web UI as an administrator.
2. Navigate to **Settings → Security → Audit & CEE**.
3. In the **CEE servers** section, click **Add**.
4. Enter the cee-exporter endpoint URL:
   - Plain HTTP: `http://192.168.1.10:12228`
   - HTTPS: `https://cee-exporter.example.com:12228`
5. Set **Protocol** to **HTTP** (even for HTTPS — the URL scheme controls TLS).
6. Leave the **Username** and **Password** fields empty (cee-exporter uses no authentication).
7. Under **Events to publish**, select all audit event types you want to capture (file create, read, write, delete, ACL change, etc.).
8. Click **Save**.
9. PowerStore sends a `RegisterRequest` to cee-exporter to confirm connectivity. The daemon logs `cepa_register_request` at INFO level.
10. Verify events are flowing:
    ```bash
    curl http://localhost:12228/health | jq .events_received_total
    ```

### Troubleshooting registration

| Symptom | Likely cause |
|---------|-------------|
| PowerStore shows server as **Unreachable** | Network/firewall blocking port 12228, or cee-exporter not running |
| PowerStore shows **TLS handshake error** | Certificate SAN mismatch, or cert not imported into PowerStore trust store |
| No events in Graylog after registration | Check `gelf_host`/`gelf_port` in config; verify Graylog GELF Input is active |
| `cepa_parse_error` in cee-exporter logs | Unsupported CEPA payload format — open an issue with the raw payload |

---

## Health endpoint

`GET /health` returns a JSON object with operational status. HTTP 200 = healthy; HTTP 503 = degraded.

```bash
curl http://localhost:12228/health
```

Example response:

```json
{
  "status": "ok",
  "uptime": "2h34m",
  "writer_type": "gelf",
  "writer_addr": "192.168.1.50:12201",
  "tls_enabled": false,
  "events_received_total": 14823,
  "events_written_total": 14823,
  "events_dropped_total": 0,
  "queue_depth": 0,
  "last_event_at": "2026-03-03T08:15:42Z"
}
```

---

## Graceful shutdown

cee-exporter handles `SIGTERM` and `SIGINT`. On receiving a signal it:

1. Stops accepting new HTTP connections (30-second drain).
2. Waits for the event queue to flush.
3. Closes the writer connection.

```bash
# systemd / Docker / Kubernetes: SIGTERM is sent automatically
# Manual:
kill -TERM $(pgrep cee-exporter)
```
