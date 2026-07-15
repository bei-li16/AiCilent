.PHONY: build run clean test vet

APP_NAME = ai-proxy

build:
	go build -o $(APP_NAME).exe ./cmd/proxy/

run:
	go run ./cmd/proxy/ --config config/providers.yaml

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
	GOOS=linux GOARCH=amd64 go build -o $(APP_NAME)-linux ./cmd/proxy/

build-macos:
	GOOS=darwin GOARCH=amd64 go build -o $(APP_NAME)-macos ./cmd/proxy/

build-macos-arm64:
	GOOS=darwin GOARCH=arm64 go build -o $(APP_NAME)-macos-arm64 ./cmd/proxy/

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -o $(APP_NAME)-linux-arm64 ./cmd/proxy/

build-all: build build-linux build-linux-arm64 build-macos build-macos-arm64