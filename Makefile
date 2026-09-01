BINARY := package-checker
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
DIST_DIR ?= dist

GO ?= go
GOFLAGS ?=
GOLANGCI_LINT ?= golangci-lint
HOST_OS := $(shell uname -s | tr A-Z a-z)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
LINUX_LDFLAGS := $(LDFLAGS) -bindnow

ifeq ($(HOST_OS),linux)
BUILD_FLAGS := -buildmode=pie -ldflags "$(LINUX_LDFLAGS)"
else
BUILD_FLAGS := -ldflags "$(LDFLAGS)"
endif

.PHONY: all build fmt-check lint test vet fix check clean release linux linux-amd64 linux-arm64 darwin darwin-amd64 darwin-arm64 windows windows-amd64

all: build

build:
	@mkdir -p $(DIST_DIR)
	$(GO) build $(GOFLAGS) $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY) .

fmt-check:
	$(GOLANGCI_LINT) fmt --diff

lint:
	$(GOLANGCI_LINT) run ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fix:
	$(GO) fix ./...

check: fmt-check lint test vet

release: linux darwin windows

linux: linux-amd64 linux-arm64

linux-amd64:
	@mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -buildmode=pie -ldflags "$(LINUX_LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-linux-amd64 .

linux-arm64:
	@mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -buildmode=pie -ldflags "$(LINUX_LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-linux-arm64 .

darwin: darwin-amd64 darwin-arm64

darwin-amd64:
	@mkdir -p $(DIST_DIR)
	GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-darwin-amd64 .

darwin-arm64:
	@mkdir -p $(DIST_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-darwin-arm64 .

windows: windows-amd64

windows-amd64:
	@mkdir -p $(DIST_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-windows-amd64.exe .

clean:
	rm -rf $(DIST_DIR)
