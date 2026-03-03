BINARY_NAME    := cee-exporter
BINARY_WINDOWS := cee-exporter.exe
CMD_PATH       := ./cmd/cee-exporter
LDFLAGS        := -s -w

REGISTRY       := ghcr.io/fjacquet
IMAGE          := $(REGISTRY)/cee-exporter
VERSION        := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

.PHONY: build build-windows test lint clean docker-build docker-push docker-run

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) $(CMD_PATH)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY_WINDOWS) $(CMD_PATH)

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -f $(BINARY_NAME) $(BINARY_WINDOWS)

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
