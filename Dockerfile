# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 — Build
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

# ca-certificates: needed at build time for go mod download (HTTPS) and
# copied to the final scratch image to support TLS listener cert validation.
RUN apk add --no-cache ca-certificates

WORKDIR /src

# Download dependencies first (cached layer unless go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w" \
    -o cee-exporter ./cmd/cee-exporter

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 — Final image (scratch — no OS, no shell, minimal attack surface)
# ─────────────────────────────────────────────────────────────────────────────
FROM scratch

# CA certificates — required if the binary makes TLS client connections
# (e.g. GELF over TLS to a remote Graylog instance).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Binary
COPY --from=builder /src/cee-exporter /cee-exporter

# CEPA listener port (TCP)
EXPOSE 12228

# Mount your config at /etc/cee-exporter/config.toml:
#   docker run -v ./config.toml:/etc/cee-exporter/config.toml ...
# OR override GELF target via environment:
#   -e CEE_LOG_LEVEL=debug
ENTRYPOINT ["/cee-exporter"]
CMD ["-config", "/etc/cee-exporter/config.toml"]
