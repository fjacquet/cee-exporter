# Canonical Go Makefile — fjacquet/ci standard interface
# Go 1.24 compat: pin tool versions that don't require Go 1.25+
.DEFAULT_GOAL := all

BINARY_NAME    := cee-exporter
BINARY_WINDOWS := cee-exporter.exe
BINARY_DARWIN  := cee-exporter-darwin
CMD_PATH       := ./cmd/cee-exporter

REGISTRY       := ghcr.io/fjacquet
IMAGE          := $(REGISTRY)/cee-exporter
VERSION        := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS        := -s -w -X main.version=$(VERSION)

SYSTEMD_UNIT_SRC := deploy/systemd/cee-exporter.service
SYSTEMD_UNIT_DST := /etc/systemd/system/cee-exporter.service
# Must match ExecStart= in $(SYSTEMD_UNIT_SRC).
BINARY_INSTALL_PATH := /usr/local/bin/cee-exporter

DIST  ?= dist
COVER ?= coverage.out

GOLANGCI_VERSION    ?= v2.12.2
GORELEASER_VERSION  ?= v2.12.0
GOVULNCHECK_VERSION ?= latest

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
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

lint:
	go mod tidy -diff
	golangci-lint run --timeout=5m

format:
	golangci-lint fmt

test:
	go test -race -coverprofile=$(COVER) -covermode=atomic ./...

build:
	go build -v ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

sbom:
	mkdir -p $(DIST)
	go run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest mod -json -output $(DIST)/sbom.cdx.json

security:  # advisory: reports findings but never blocks the build (CodeQL/osv are the blocking gates)
	uvx semgrep scan --config auto --skip-unknown-extensions || true

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
# The unit uses DynamicUser, so no system account needs to be created.
# Builds for the host architecture — build-linux pins amd64, and a wrong-arch
# binary at ExecStart fails as status=203/EXEC, indistinguishable from a
# missing file.
install-systemd: $(SYSTEMD_UNIT_SRC)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(shell go env GOARCH) \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) $(CMD_PATH)
	install -d -m 755 /etc/cee-exporter
	install -m 755 $(BINARY_NAME) $(BINARY_INSTALL_PATH)
	install -m 644 $(SYSTEMD_UNIT_SRC) $(SYSTEMD_UNIT_DST)
	systemctl daemon-reload
	@echo "Binary installed to $(BINARY_INSTALL_PATH)"
	@echo "Unit installed. Place your config, keeping it world-readable (DynamicUser"
	@echo "requires this — config.toml has no secrets; put those in env instead):"
	@echo "  install -m 644 config.toml /etc/cee-exporter/config.toml"
	@echo "  systemctl enable --now cee-exporter"

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

## winres: regenerate the Windows message resource (.syso)
##
## Uses a local mingw-w64 toolchain when present (brew install mingw-w64 on
## macOS, binutils-mingw-w64-x86-64 + gcc-mingw-w64-x86-64 on Debian), and
## falls back to a container otherwise, so this works on a machine that has
## only Docker.
.PHONY: winres
winres:
ifeq ($(shell command -v x86_64-w64-mingw32-windres 2>/dev/null),)
	docker run --rm -v "$(PWD)/pkg/evtx":/w -w /w ubuntu:24.04 sh -c '\
		apt-get update -qq && \
		apt-get install -y -qq binutils-mingw-w64-x86-64 gcc-mingw-w64-x86-64 && \
		x86_64-w64-mingw32-windmc -h . -r . messages.mc && \
		x86_64-w64-mingw32-windres -i messages.rc -O coff -o rsrc_windows_amd64.syso'
else
	cd pkg/evtx && x86_64-w64-mingw32-windmc -h . -r . messages.mc
	cd pkg/evtx && x86_64-w64-mingw32-windres -i messages.rc -O coff -o rsrc_windows_amd64.syso
endif
	@echo "regenerated pkg/evtx/rsrc_windows_amd64.syso"
