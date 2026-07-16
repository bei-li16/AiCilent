.PHONY: build run clean test vet

APP_NAME = ai-proxy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "unknown")
LDFLAGS  = -ldflags "-X ai-proxy/internal/version.Version=$(VERSION) -X ai-proxy/internal/version.Commit=$(COMMIT) -X ai-proxy/internal/version.BuildDate=$(DATE)"

build:
	go build $(LDFLAGS) -o $(APP_NAME).exe ./cmd/proxy/

run:
	go run $(LDFLAGS) ./cmd/proxy/ --config config/providers.yaml

clean:
	rm -f $(APP_NAME).exe
	rm -f $(APP_NAME)-linux
	rm -f $(APP_NAME)-macos
	rm -f $(APP_NAME)-macos-arm64

test:
	go test ./...

vet:
	go vet ./...

# Cross-compilation
build-linux:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(APP_NAME)-linux ./cmd/proxy/

build-macos:
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(APP_NAME)-macos ./cmd/proxy/

build-macos-arm64:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(APP_NAME)-macos-arm64 ./cmd/proxy/

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(APP_NAME)-linux-arm64 ./cmd/proxy/

build-all: build build-linux build-linux-arm64 build-macos build-macos-arm64