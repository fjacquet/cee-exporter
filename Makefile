BINARY_NAME    := cee-exporter
BINARY_WINDOWS := cee-exporter.exe
CMD_PATH       := ./cmd/cee-exporter
LDFLAGS        := -s -w

.PHONY: build build-windows test lint clean

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
