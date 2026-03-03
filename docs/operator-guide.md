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

### TLS config reference

```toml
# Optional: override the hostname embedded in every event.
# Defaults to os.Hostname() if not set.
hostname = ""

[listen]
addr          = "0.0.0.0:12228"  # TCP address and port to listen on
tls_mode      = "off"            # "off" | "manual" | "acme" | "self-signed"
cert_file     = ""               # tls_mode="manual": path to TLS certificate (PEM)
key_file      = ""               # tls_mode="manual": path to TLS private key (PEM)
acme_domains  = []               # tls_mode="acme": domain names for Let's Encrypt
acme_email    = ""               # tls_mode="acme": contact email for Let's Encrypt
acme_cache_dir = "/var/cache/cee-exporter/acme"  # tls_mode="acme": cert cache dir
acme_staging  = false            # tls_mode="acme": use LE staging (dev/testing)

[output]
type           = "gelf"         # Output type — see table below
targets        = []             # type="multi": list of types to fan-out to
evtx_path      = ""             # type="evtx" or "binary-evtx": output path
# GELF
gelf_host      = "localhost"
gelf_port      = 12201
gelf_protocol  = "udp"          # "tcp" or "udp"
gelf_tls       = false          # Wrap TCP in TLS (requires gelf_protocol="tcp")
# Syslog (RFC 5424)
syslog_host    = "localhost"
syslog_port    = 514
syslog_protocol = "udp"         # "tcp" or "udp"
syslog_app_name = "cee-exporter"
# Beats / Lumberjack v2
beats_host     = "localhost"
beats_port     = 5044
beats_tls      = false

[queue]
capacity = 100000               # Maximum events buffered in memory
workers  = 4                    # Concurrent writer goroutines

[logging]
level  = "info"                 # debug | info | warn | error
format = "json"                 # json | text

[metrics]
addr = "0.0.0.0:9228"          # Prometheus /metrics listener
```

### Output types

| Type | Description | Platform |
|------|-------------|----------|
| `gelf` | GELF 1.1 JSON over UDP or TCP → Graylog | All |
| `evtx` | Win32 `ReportEvent` → Windows Application Event Log | Windows |
| `syslog` | RFC 5424 structured syslog over UDP or TCP (RFC 6587 framing for TCP) | All |
| `beats` | Lumberjack v2 to Logstash / Graylog Beats Input (± TLS) | All |
| `binary-evtx` | Native `.evtx` files readable by Windows Event Viewer | Non-Windows |
| `multi` | Fan-out to any combination of the above | All |

### Multi-target example

```toml
[output]
type    = "multi"
targets = ["gelf", "syslog"]

gelf_host      = "192.168.1.50"
gelf_port      = 12201
gelf_protocol  = "tcp"

syslog_host    = "syslog.corp.local"
syslog_port    = 514
syslog_protocol = "udp"
```

### Beats (Lumberjack v2) output

```toml
[output]
type       = "beats"
beats_host = "logstash.corp.local"
beats_port = 5044
beats_tls  = true
```

Logstash must have a [Beats input](https://www.elastic.co/guide/en/logstash/current/plugins-inputs-beats.html) configured on port 5044. Graylog also supports the Beats protocol via its Beats Input plugin.

### Binary EVTX output (Linux)

```toml
[output]
type      = "binary-evtx"
evtx_path = "/var/log/cee-exporter/audit.evtx"
```

Generates a native Windows `.evtx` file that can be opened directly in Windows Event Viewer or parsed by forensics tools (Splunk, Elastic Agent, Velociraptor). Only available on non-Windows platforms — on Windows, use `type = "evtx"` for direct Win32 Event Log writing.

---

## Windows Service management

On Windows, `cee-exporter.exe` can register itself with the Windows Service Control
Manager (SCM) for automatic startup and restart on failure.

> Run all service management commands from an **Administrator** command prompt.

```powershell
# Register the service (Delayed Auto-Start, restarts on failure after 5 s)
cee-exporter.exe install -config C:\cee-exporter\config.toml

# Start the service
sc start cee-exporter

# Check status
sc query cee-exporter

# Verify recovery actions are configured
sc qfailure cee-exporter

# Stop the service
sc stop cee-exporter

# Remove the service
cee-exporter.exe uninstall
```

The service runs as `LocalSystem` by default. If your CEPA configuration or cert files
require network access to SMB shares, change the service account via `services.msc`.

### Automatic restart behavior

cee-exporter registers with these recovery settings automatically at install:

| Failure | Action | Delay |
|---------|--------|-------|
| First failure | Restart service | 5 seconds |
| Second failure | Restart service | 5 seconds |
| Subsequent | Restart service | 5 seconds |
| Reset failure count after | — | 24 hours |

---

## Linux systemd service

A systemd unit file is included for production deployments:

```bash
# Copy the binary and unit file
install -m 755 cee-exporter /usr/local/bin/
install -m 644 systemd/cee-exporter.service /etc/systemd/system/

# Place your config
mkdir -p /etc/cee-exporter
cp config.toml /etc/cee-exporter/config.toml

# Enable and start
systemctl daemon-reload
systemctl enable --now cee-exporter

# Check status
systemctl status cee-exporter
journalctl -u cee-exporter -f
```

---

## Prometheus metrics

`cee-exporter` exposes a Prometheus-compatible `/metrics` endpoint on port 9228
(separate from the CEPA listener to allow independent TLS configuration):

```bash
curl http://localhost:9228/metrics
```

Available metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `cee_events_received_total` | Counter | Events received from PowerStore |
| `cee_events_written_total` | Counter | Events successfully forwarded |
| `cee_events_dropped_total` | Counter | Events dropped (queue full) |
| `cee_writer_errors_total` | Counter | Writer backend errors |
| `cee_queue_depth` | Gauge | Current event queue depth |

Example Prometheus scrape config:

```yaml
- job_name: cee-exporter
  static_configs:
    - targets: ['cee-exporter-host:9228']
```

---

## TLS / HTTPS setup

> **Reminder:** The CEPA protocol uses HTTP only. TLS on the cee-exporter CEPA listener
> (port 12228) does NOT encrypt the PowerStore-to-exporter event channel. TLS here is
> only meaningful when a TLS-capable reverse proxy sits in front.

cee-exporter supports four TLS modes controlled by the `tls_mode` config field.

### Mode: `off` (default)

No TLS. Correct for all standard CEPA deployments.

```toml
[listen]
addr     = "0.0.0.0:12228"
tls_mode = "off"
```

### Mode: `manual` — operator-supplied certificates

Use when you have certificates from an internal CA or a public CA.

```toml
[listen]
addr      = "0.0.0.0:12228"
tls_mode  = "manual"
cert_file = "/etc/cee-exporter/server.crt"
key_file  = "/etc/cee-exporter/server.key"
```

To generate a self-signed certificate for testing:

```bash
openssl req -x509 -nodes -newkey rsa:4096 \
  -keyout server.key \
  -out server.crt \
  -days 825 \
  -subj "/CN=cee-exporter" \
  -addext "subjectAltName=DNS:cee-exporter.example.com,IP:192.168.1.10"
```

### Mode: `acme` — automatic Let's Encrypt certificate

Use when the cee-exporter host has a public DNS name and port 443 is reachable from
the internet (or from Let's Encrypt's servers).

```toml
[listen]
addr           = "0.0.0.0:12228"
tls_mode       = "acme"
acme_domains   = ["cee-exporter.example.com"]
acme_email     = "ops@example.com"
acme_cache_dir = "/var/cache/cee-exporter/acme"  # must be persistent
```

cee-exporter starts a TLS-ALPN-01 challenge listener on port 443 in addition to the
CEPA listener on 12228. The certificate is automatically renewed 30 days before expiry.

**Prerequisites:**

- Port 443/TCP must be reachable from the internet
- `acme_cache_dir` must be on persistent storage (mount a volume if using Docker)
- On Linux: the systemd unit must have `AmbientCapabilities=CAP_NET_BIND_SERVICE`
- During development: set `acme_staging = true` to avoid Let's Encrypt rate limits

```toml
acme_staging = true  # Remove this line for production
```

### Mode: `self-signed` — runtime-generated certificate

Use for air-gapped networks, private deployments, or testing. No network access needed.

```toml
[listen]
addr     = "0.0.0.0:12228"
tls_mode = "self-signed"
```

cee-exporter generates an ECDSA-P256 certificate at startup (valid 1 year). The
certificate is not persisted — it changes on every restart. Clients must disable
certificate verification or add the cert to their trust store.

---

## Registering with PowerStore CEPA

CEPA (Common Event Publishing Agent) is the PowerStore mechanism that sends file-system audit events to external consumers over HTTP.

> **Protocol constraint:** The Dell PowerStore CEPA client sends events over **plain HTTP only**. The endpoint URL in the PowerStore configuration must always use the `http://` scheme. Configuring `tls = true` on the cee-exporter CEPA listener (port 12228) does _not_ encrypt the PowerStore-to-exporter connection — CEPA will fail to connect if it encounters a TLS handshake. TLS on port 12228 is only useful if a TLS-capable reverse proxy sits in front of cee-exporter.

### Prerequisites

- cee-exporter is running and reachable from the PowerStore management network
- Port 12228/TCP is open between PowerStore and the cee-exporter host (firewall rules for HTTP)

### Registration steps

1. Log in to the PowerStore Manager web UI as an administrator.
2. Navigate to **Settings → Security → Audit & CEE**.
3. In the **CEE servers** section, click **Add**.
4. Enter the cee-exporter endpoint URL using `http://` — CEPA supports HTTP only:

   ```text
   http://192.168.1.10:12228
   ```

5. Leave the **Username** and **Password** fields empty (cee-exporter uses no authentication).
6. Under **Events to publish**, select all audit event types you want to capture (file create, read, write, delete, ACL change, etc.).
7. Click **Save**.
8. PowerStore sends a `RegisterRequest` to cee-exporter to confirm connectivity. The daemon logs `cepa_register_request` at INFO level.
9. Verify events are flowing:

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
