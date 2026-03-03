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

.PHONY: all build build-windows build-darwin test lint clean docker-build docker-push docker-run install-systemd

all: build build-windows build-darwin

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) $(CMD_PATH)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_WINDOWS) $(CMD_PATH)

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=$(shell go env GOARCH) \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_DARWIN) $(CMD_PATH)

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY_NAME) $(BINARY_WINDOWS) $(BINARY_DARWIN)

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
