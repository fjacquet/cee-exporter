# cee-exporter

Receives Dell PowerStore CEPA audit events over HTTP and forwards them as GELF (Graylog) or Windows EventLog entries.

## Overview

cee-exporter is a lightweight Go daemon that implements the Dell Common Event Publishing Agent (CEPA) protocol. It receives file-system audit events from Dell PowerStore NAS servers via HTTP PUT and forwards them to GELF-compatible SIEMs (e.g., Graylog) or to the native Windows Event Log. Linux sysadmins can ship PowerStore audit data directly into Graylog without any Windows infrastructure; Windows sysadmins can view the same events in Event Viewer. The binary has no runtime dependencies — drop it on a host, configure a TOML file, and run.

## Prerequisites

- **Go 1.21+** — only required if building from source; pre-built binaries have no Go dependency
- **Graylog** with a GELF UDP or TCP input configured (Linux/GELF path), or **Windows Event Viewer** (Windows path)
- **Dell PowerStore NAS** with CEPA/CEE enabled (see [Dell PowerStore CEPA Registration](#dell-powerstore-cepa-registration))
- **Firewall rule**: inbound TCP (and optionally UDP) port `12228` open from the PowerStore NAS server IP to the cee-exporter host

## Quick Start

Follow these steps to go from binary to receiving PowerStore audit events in Graylog.

### 1. Download or build the binary

```bash
# Build from source (requires Go 1.21+)
git clone https://github.com/fjacquet/cee-exporter.git
cd cee-exporter
make build          # produces ./cee-exporter (Linux/amd64)
```

Or download the pre-built release binary from the GitHub Releases page.

### 2. Configure cee-exporter

```bash
cp config.toml.example config.toml
```

Edit `config.toml` and set `gelf_host` to your Graylog server IP or hostname:

```toml
[output]
type          = "gelf"
gelf_host     = "192.168.1.50"   # <-- your Graylog IP
gelf_port     = 12201
gelf_protocol = "udp"
```

### 3. Run cee-exporter

```bash
./cee-exporter --config config.toml
```

cee-exporter logs startup details (listen address, output backend) in JSON format to stdout.

### 4. Set up a Graylog GELF input

In the Graylog web interface:

1. Navigate to **System > Inputs**
2. From the **Select input** dropdown, choose **GELF UDP**
3. Click **Launch new input**
4. Fill in the fields:
   - **Title:** `PowerStore CEPA Events`
   - **Bind address:** `0.0.0.0`
   - **Port:** `12201`
5. Click **Launch**

### 5. Test the Graylog connection

Send a test GELF message to verify Graylog is receiving data:

```bash
echo -n '{"version":"1.1","host":"test","short_message":"cee-exporter test","level":6}' \
  | nc -w0 -u <graylog-ip> 12201
```

Check the Graylog **Search** page — the test message should appear within a few seconds.

### 6. Verify cee-exporter health

```bash
curl http://localhost:12228/health
```

Expected response:

```json
{"status":"ok","uptime_seconds":12,"events_received":0,"events_written":0,"queue_depth":0}
```

### 7. Configure Dell PowerStore CEPA

Follow the steps in the [Dell PowerStore CEPA Registration](#dell-powerstore-cepa-registration) section to point your NAS server at cee-exporter. Once CEPA registration completes, file-system events will flow automatically.

---

## Configuration Reference

All configuration lives in `config.toml` (TOML format). Copy `config.toml.example` to get started.

| Field | Type | Default | Description | Example |
|-------|------|---------|-------------|---------|
| `hostname` | string | `os.Hostname()` | Hostname embedded in every generated event | `hostname = "powerstore-nas01"` |
| `[listen] addr` | string | `"0.0.0.0:12228"` | TCP address and port to listen on | `addr = "0.0.0.0:12228"` |
| `[listen] tls` | bool | `false` | Enable HTTPS listener | `tls = true` |
| `[listen] cert_file` | string | `""` | Path to TLS certificate file (PEM format) | `cert_file = "/etc/cee-exporter/tls/server.crt"` |
| `[listen] key_file` | string | `""` | Path to TLS private key file (PEM format) | `key_file = "/etc/cee-exporter/tls/server.key"` |
| `[output] type` | string | `"gelf"` | Output backend: `gelf` \| `evtx` \| `multi` | `type = "gelf"` |
| `[output] targets` | []string | `[]` | Backend list when `type = "multi"` | `targets = ["gelf", "evtx"]` |
| `[output] gelf_host` | string | `"localhost"` | Graylog or GELF-compatible host address | `gelf_host = "graylog.corp.local"` |
| `[output] gelf_port` | int | `12201` | GELF UDP/TCP port | `gelf_port = 12201` |
| `[output] gelf_protocol` | string | `"udp"` | Transport protocol: `udp` \| `tcp`. Use `tcp` for production. | `gelf_protocol = "tcp"` |
| `[output] gelf_tls` | bool | `false` | Enable TLS for GELF TCP transport | `gelf_tls = false` |
| `[output] evtx_path` | string | `""` | Output path for binary `.evtx` files (v1: stub on Linux) | `evtx_path = "/var/log/cee-exporter"` |
| `[queue] capacity` | int | `100000` | Maximum events buffered in memory before dropping | `capacity = 100000` |
| `[queue] workers` | int | `4` | Number of concurrent writer goroutines | `workers = 4` |
| `[logging] level` | string | `"info"` | Log verbosity: `debug` \| `info` \| `warn` \| `error` | `level = "info"` |
| `[logging] format` | string | `"json"` | Log format: `json` (production) \| `text` (development) | `format = "json"` |

> **Production note:** For high-volume NAS workloads (VCAPS mode with thousands of events per batch), set `gelf_protocol = "tcp"` to avoid silent UDP packet loss. UDP is acceptable for lab and testing environments.

### Environment Variable Overrides

Two environment variables override the corresponding `[logging]` config fields at runtime:

| Variable | Overrides | Values |
|----------|-----------|--------|
| `CEE_LOG_LEVEL` | `[logging] level` | `debug`, `info`, `warn`, `error` |
| `CEE_LOG_FORMAT` | `[logging] format` | `json`, `text` |

Example:

```bash
CEE_LOG_LEVEL=debug CEE_LOG_FORMAT=text ./cee-exporter --config config.toml
```

---

## TLS Setup

TLS secures data in transit between cee-exporter and its clients. For most default PowerStore CEPA configurations, the CEPA publisher sends plain HTTP — verify with Dell support whether your firmware supports HTTPS for event publishing. TLS is recommended when cee-exporter is accessed over an untrusted network.

### Generate a Self-Signed Certificate

Two methods are provided depending on your openssl version.

**Method 1 — openssl >= 1.1.1 (recommended):**

```bash
openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout server.key \
  -out server.crt \
  -subj "/CN=cee-exporter" \
  -addext "subjectAltName=IP:192.168.1.10,DNS:cee-exporter.corp.local"
```

Replace the IP and DNS values with the actual address of your cee-exporter host. The `-addext subjectAltName` flag is required — Go TLS (>= 1.15) rejects certificates without Subject Alternative Names (SANs).

**Method 2 — openssl < 1.1.1 (using extfile):**

```bash
cat > san.cnf <<'EOF'
[req]
distinguished_name = req_dn
x509_extensions = v3_req
prompt = no
[req_dn]
CN = cee-exporter
[v3_req]
subjectAltName = IP:192.168.1.10,DNS:cee-exporter.corp.local
EOF

openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout server.key -out server.crt -config san.cnf
```

### Configure cee-exporter for TLS

Update `config.toml` to enable TLS and point to your certificate files:

```toml
[listen]
addr      = "0.0.0.0:12228"
tls       = true
cert_file = "/etc/cee-exporter/tls/server.crt"
key_file  = "/etc/cee-exporter/tls/server.key"
```

> **Note:** cee-exporter logs a `WARN` at startup if the certificate expires within 30 days.

---

## Dell PowerStore CEPA Registration

CEPA (Common Event Publishing Agent) is configured per-NAS-server in the PowerStore Web UI. Each NAS server must have its own Event Publishing Pool.

### Prerequisites

Port `12228` (TCP) must be open from the PowerStore NAS server IP to the cee-exporter host. Verify connectivity before configuring CEPA:

```bash
nc -zv <cee-exporter-ip> 12228
```

### Create an Event Publishing Pool

1. Navigate to **Storage > NAS Servers** in the PowerStore Web UI
2. Select the target NAS Server
3. Go to the **Security & Events** tab > **Events Publishing** sub-tab
4. Click **Create new event publisher** (or **Modify** if one already exists)
5. Set a **Publisher Name** (e.g., `cee-exporter`)
6. Under **Publishing Pools**, click **Create Pool**
7. Set a **Pool Name** (e.g., `default-pool`)
8. Add a CEPA Server:
   - **Host:** cee-exporter IP or FQDN
   - **Port:** `12228`
9. Under **Event Configuration > Post-Events** tab:
   - Click **Select all**
   - Uncheck: `CloseDir`, `OpenDir`, `FileRead`, `OpenFileReadOffline`, `OpenFileWriteOffline`
   - Leave Pre-Events and Post-Error-Events unchecked
10. Click **Apply**

### Protocol Notes

- cee-exporter responds to the RegisterRequest handshake with HTTP 200 OK and an **empty body** — this is required by the CEPA protocol. Any XML in the response causes a fatal parse error in the CEPA publisher. Do not modify this behavior.
- Heartbeat PUT requests are acknowledged within 3 seconds. cee-exporter processes events asynchronously via its internal queue.
- If using VCAPS mode (thousands of events per batch), set `gelf_protocol = "tcp"` to avoid UDP packet loss.
- CEPA is configured per NAS server. If events are not arriving, verify that the Event Publishing Pool is configured on the specific NAS server that owns the target file systems.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `SDNAS_CEPP_ALL_SERVERS_UNREACHABLE` alert on PowerStore | Port 12228 unreachable from NAS | Verify firewall: `nc -zv <cee-exporter-ip> 12228` |
| TLS handshake fails: `certificate has no suitable IP SANs` | Certificate missing Subject Alternative Name | Regenerate cert with `-addext "subjectAltName=IP:...,DNS:..."` |
| No events in Graylog despite CEPA configured | GELF UDP packet loss on high-volume NAS | Switch to `gelf_protocol = "tcp"` in config.toml |
| RegisterRequest causes fatal CEPA error | Non-empty HTTP response body sent to CEPA | Do not modify the RegisterRequest handler; cee-exporter returns an empty HTTP 200 OK by design |
| Events arrive but from wrong NAS server | CEPA configured on wrong NAS | Verify CEPA Event Publishing Pool is on the NAS server that owns the target file system |
| `/health` returns connection refused | cee-exporter not running or wrong port | Check process: `ps aux | grep cee-exporter`; check listen addr in config.toml |

---

## Building from Source

Requires Go 1.21+. No CGO dependencies (`CGO_ENABLED=0` — produces fully static binaries).

```bash
git clone https://github.com/fjacquet/cee-exporter.git
cd cee-exporter

make build          # Linux/amd64 binary: ./cee-exporter
make build-windows  # Windows/amd64 binary: ./cee-exporter.exe
make test           # Run unit tests
make lint           # Run go vet
```

### Windows Deployment

The Windows binary (`cee-exporter.exe`) uses the native Win32 EventLog API (`ReportEvent`). When running on Windows:

- Events are written to the Windows Application Event Log
- No additional configuration is required beyond setting `[output] type = "evtx"` in config.toml
- The GELF output path is also fully supported on Windows (both `gelf` and `multi` output types work)

---

## License

See [LICENSE](LICENSE) for details.
