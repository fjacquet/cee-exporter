# Canonical Go Makefile — fjacquet/ci standard interface
# Go 1.24 compat: pin tool versions that don't require Go 1.25+
.DEFAULT_GOAL := all

BINARY_NAME    := cee-exporter
BINARY_WINDOWS := cee-exporter.exe
BINARY_DARWIN  := cee-exporter-darwin
CMD_PATH       := ./cmd/cee-exporter
LDFLAGS        := -s -w

REGISTRY       := ghcr.io/fjacquet
IMAGE          := $(REGISTRY)/cee-exporter
VERSION        := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

SYSTEMD_UNIT_SRC := deploy/systemd/cee-exporter.service
SYSTEMD_UNIT_DST := /etc/systemd/system/cee-exporter.service

DIST  ?= dist
COVER ?= coverage.out

# Go 1.24-compatible tool versions (golangci-lint/goreleaser v2.9+ require Go 1.25)
GOLANGCI_VERSION   ?= v2.8.0
GORELEASER_VERSION ?= v2.7.0
GOVULNCHECK_VERSION ?= v1.1.4

.PHONY: all clean install tools lint format test build vuln sbom security docs coverage-upload release ci \
        build-windows build-darwin test-race lint-full coverage docker-build docker-push docker-run install-systemd

# ── Canonical targets (called by fjacquet/ci reusable workflows) ────────────

all: clean lint test build

clean:
	rm -rf $(DIST) site $(COVER) *.sarif $(BINARY_NAME) $(BINARY_WINDOWS) $(BINARY_DARWIN)

install:
	go mod download

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

lint:
	golangci-lint run --timeout=5m

format:
	golangci-lint fmt

test:
	go test -race -coverprofile=$(COVER) -covermode=atomic ./...

build:
	go build -v ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

sbom:
	mkdir -p $(DIST)
	go run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest mod -json -output $(DIST)/sbom.cdx.json

security:
	uvx semgrep scan --config auto --error --skip-unknown-extensions

docs:
	uvx --with mkdocs-material --with pymdown-extensions mkdocs build --strict --site-dir site

coverage-upload:
	uvx --from codecov-cli codecov upload-process --file $(COVER) || true

release:
	goreleaser release --clean

ci: lint test build vuln

# ── Repo-specific targets (preserved) ───────────────────────────────────────

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) $(CMD_PATH)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_WINDOWS) $(CMD_PATH)

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=$(shell go env GOARCH) \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_DARWIN) $(CMD_PATH)

# Race detector requires CGO; run separately from the default make test which
# uses CGO_ENABLED=0 for static binary builds.
test-race:
	CGO_ENABLED=1 go test -race ./...

# Full lint: golangci-lint with the repo config.
lint-full:
	golangci-lint run --timeout=5m

coverage:
	go test -coverprofile=$(COVER) ./...
	go tool cover -func=$(COVER) | tail -1

# Requires root. Run as: sudo make install-systemd
install-systemd: $(SYSTEMD_UNIT_SRC)
	@echo "NOTE: Create the cee-exporter system user first if it does not exist:"
	@echo "  useradd --system --no-create-home --shell /usr/sbin/nologin cee-exporter"
	install -m 644 $(SYSTEMD_UNIT_SRC) $(SYSTEMD_UNIT_DST)
	systemctl daemon-reload
	@echo "Unit installed. Run: systemctl enable --now cee-exporter"

docker-build:
	docker build --build-arg VERSION=$(VERSION) \
	  -t $(IMAGE):$(VERSION) \
	  -t $(IMAGE):latest .

docker-push: docker-build
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

docker-run:
	docker run --rm \
	  -p 12228:12228 \
	  -v $(PWD)/config.toml:/etc/cee-exporter/config.toml:ro \
	  $(IMAGE):latest
