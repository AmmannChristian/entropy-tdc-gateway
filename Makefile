# Makefile for entropy-tdc-gateway
# IMPORTANT: This file uses TAB characters for indentation, not spaces!

.PHONY: all proto build build-arm64 run clean test test-ci tests test-cover test-race cover cover-html cover-threshold perpkg-threshold coverage-ci coverage deploy deps dev fmt fmt-fix lint staticcheck gosec govulncheck vet tools

# ========================================
# Variables
# ========================================
BINARY_NAME=entropy-tdc-gateway
PROTO_DIR=api/proto/v1
PB_DIR=pkg/pb
BUILD_DIR=build
DOCKER_REGISTRY=your-registry.com
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

GOTESTFLAGS ?= -count=1 -timeout=2m
RACE_TESTFLAGS ?= -count=1 -timeout=3m
UNIT_PKGS ?= ./cmd/... ./internal/...
UNIT_SHUFFLE ?= on
RACE_SHUFFLE ?= on
COVER_SHUFFLE ?= off
JUNIT_FILE ?=
COVERAGE_MIN ?= 90
COVERMODE ?= atomic
COVERPROFILE ?= $(BUILD_DIR)/coverage.out

DEV_TOOLS=\
	github.com/golangci/golangci-lint/cmd/golangci-lint@latest \
	honnef.co/go/tools/cmd/staticcheck@latest \
	golang.org/x/vuln/cmd/govulncheck@latest \
	golang.org/x/tools/cmd/goimports@latest \
	mvdan.cc/gofumpt@latest \
	github.com/securego/gosec/v2/cmd/gosec@latest \
	google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 \
	google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1


FMT_FIND := find . -type f -name '*.go' -not -path './pkg/pb/*' -not -path './build/*'

# Auto-add GOPATH/bin to PATH for protoc plugins
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

# ========================================
# Default target
# ========================================
all: proto build

# ========================================
# Generate protobuf code
# ========================================
proto:
	@set -eu; \
	echo "Generating protobuf code..."; \
	MOD=$$(go list -m -f '{{.Path}}'); \
	mkdir -p $(PB_DIR); \
	protoc \
	  --go_out=. --go_opt=module=$$MOD \
	  --go-grpc_out=. --go-grpc_opt=module=$$MOD \
	  $(PROTO_DIR)/entropy.proto; \
	echo "Protobuf generation complete"; \
	find $(PB_DIR) -maxdepth 5 -type f -name '*.pb.go' -print



# ========================================
# Build for local development
# ========================================
build: proto
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/$(BINARY_NAME)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# ========================================
# Build for Raspberry Pi (ARM64)
# ========================================
build-arm64: proto
	@echo "Building for ARM64 (Raspberry Pi)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-arm64 ./cmd/$(BINARY_NAME)
	@echo "ARM64 build complete: $(BUILD_DIR)/$(BINARY_NAME)-arm64"

# ========================================
# Run locally
# ========================================
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BUILD_DIR)/$(BINARY_NAME)

# ========================================
# Development mode (no build, direct run)
# ========================================
dev: proto
	@echo "Starting development mode..."
	go run cmd/entropy-tdc-gateway/main.go

# ========================================
# Clean build artifacts
# ========================================
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)/
	rm -f $(PB_DIR)/*.pb.go
	@echo "Clean complete"

# ========================================
# Run tests
# ========================================
test:
	@echo "Running tests..."
	go test $(GOTESTFLAGS) -shuffle=$(UNIT_SHUFFLE) ./...

# Deterministic list of packages used in CI; keeps tests/coverage aligned
test-ci:
	@echo "Running CI unit tests (packages: $(UNIT_PKGS))..."
	@echo "Shuffle: $(UNIT_SHUFFLE)"
	@if [ -n "$(JUNIT_FILE)" ] && command -v gotestsum >/dev/null; then \
		mkdir -p $(dir $(JUNIT_FILE)); \
		gotestsum --junitfile $(JUNIT_FILE) --format testname -- \
			$(GOTESTFLAGS) -shuffle=$(UNIT_SHUFFLE) $(UNIT_PKGS); \
	else \
		go test $(GOTESTFLAGS) -shuffle=$(UNIT_SHUFFLE) $(UNIT_PKGS); \
	fi

# Alias for convenience (e.g., `make tests`)
tests: test

# Run tests with coverage reporting
test-cover:
	@$(MAKE) coverage

# Run tests with the race detector enabled
test-race:
	@echo "Running tests with race detector..."
	go test $(RACE_TESTFLAGS) -race -shuffle=$(RACE_SHUFFLE) $(UNIT_PKGS)

cover:
	@$(MAKE) coverage-ci

cover-html:
	@go tool cover -html=$(COVERPROFILE) -o $(BUILD_DIR)/coverage.html

cover-threshold:
	@echo "Checking total coverage ≥ $(COVERAGE_MIN)%..."
	@test -f $(COVERPROFILE) || { echo "Coverage profile $(COVERPROFILE) not found; run 'make coverage-ci' first."; exit 1; }
	@total=$$(go tool cover -func=$(COVERPROFILE) | awk '/^total:/ {print $$3}'); \
	awk -v cov=$$total -v min=$(COVERAGE_MIN) 'BEGIN { cov+=0; if (cov < min) { printf "Coverage %.2f%% < %.0f%%\n", cov, min; exit 1 } else { printf "Coverage %.2f%% ≥ %.0f%%\n", cov, min } }'

perpkg-threshold:
	@echo "Checking per-package coverage ≥ 80%..."
	@test -f $(COVERPROFILE) || { echo "Coverage profile $(COVERPROFILE) not found; run 'make coverage-ci' first."; exit 1; }
	@go tool cover -func=$(COVERPROFILE) | awk '/\.go:/ { next } /^total:/ { next } { gsub(/%/,"",$$3); if ($$3+0 < 80.0) { bad=1; printf "  %s -> %.2f%%\n", $$1, $$3 } } END { exit(bad) }'

staticcheck:
	@echo "Running staticcheck..."
	staticcheck ./...

gosec:
	@echo "Running gosec..."
	gosec -exclude-generated -conf tools/ci/.gosec.json ./...

govulncheck:
	@echo "Running govulncheck..."
	govulncheck ./...

coverage-ci:
	@echo "Generating deterministic coverage profile..."
	@mkdir -p $(BUILD_DIR)
	go test $(GOTESTFLAGS) -shuffle=$(COVER_SHUFFLE) -covermode=$(COVERMODE) -coverprofile=$(COVERPROFILE) $(UNIT_PKGS)
	@go tool cover -func=$(COVERPROFILE) | tail -n 1

coverage:
	@$(MAKE) coverage-ci
	@$(MAKE) cover-threshold

# ========================================
# Install dependencies
# ========================================
deps:
	@echo "Installing Go dependencies..."
	go mod download
	@echo "Tidying modules..."
	go mod tidy
	@$(MAKE) tools
	@echo "Dependencies installed"
	@echo ""
	@echo "Note: You also need 'protoc' compiler:"
	@echo "  Linux: sudo apt install protobuf-compiler"
	@echo "  macOS: brew install protobuf"

tools:
	@echo "Installing developer tools..."
	@set -e; for tool in $(DEV_TOOLS); do \
		echo "  $$tool"; \
		GOFLAGS='-mod=readonly -tags=tools' go install $$tool; \
	done

tools-update:
	@echo "Checking for newer tool versions..."
	@for tool in $(DEV_TOOLS); do \
		name=$${tool%@*}; \
		current=$${tool#*@}; \
		module=$$(echo $$name | sed -E 's|/cmd/.*$$||'); \
		[ -n "$$module" ] || module=$$name; \
		latest=$$(go list -m -versions $$module 2>/dev/null | tr ' ' '\n' | tail -n 1); \
		printf "%s: current=%s latest=%s\n" "$$name" "$$current" "$$latest"; \
	done

# ========================================
# Deploy to Raspberry Pi
# ========================================
deploy: build-arm64
	@echo "Deploying to Raspberry Pi..."
	ssh pi@raspi2 "mkdir -p /home/pi/entropy-tdc-gateway"
	scp $(BUILD_DIR)/$(BINARY_NAME)-arm64 pi@raspi2:/home/pi/entropy-tdc-gateway/$(BINARY_NAME)
	ssh pi@raspi2 "chmod +x /home/pi/entropy-tdc-gateway/$(BINARY_NAME)"
	@echo "Restarting service..."
	-ssh pi@raspi2 "sudo systemctl restart entropy-tdc-gateway 2>/dev/null || echo 'Service not configured'"
	@echo "Deployment complete"

# ========================================
# Format code
# ========================================
fmt: fmt-fix

fmt-fix:
	@echo "Running gofumpt..."
	@$(FMT_FIND) -print0 | xargs -0 gofumpt -w
	@echo "Running gofmt -s..."
	@$(FMT_FIND) -print0 | xargs -0 gofmt -s -w
	@echo "Running goimports..."
	@$(FMT_FIND) -print0 | xargs -0 goimports -w
	@echo "Formatting complete"

# ========================================
# Lint code
# ========================================
lint:
	@echo "Running linters..."
	golangci-lint run ./...

vet:
	@echo "Running go vet..."
	go vet ./...

# ========================================
# Docker build
# ========================================
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_REGISTRY)/$(BINARY_NAME):$(VERSION) .
	@echo "Docker build complete"

# ========================================
# Mock TDC publisher for testing
# ========================================
mock-publisher:
	@echo "Starting mock TDC publisher..."
	python3 scripts/mock-tdc-publisher.py

# ========================================
# Help
# ========================================
help:
	@echo "Available targets:"
	@echo "  make proto         - Generate protobuf code"
	@echo "  make build         - Build for local development"
	@echo "  make build-arm64   - Build for Raspberry Pi (ARM64)"
	@echo "  make run           - Build and run locally"
	@echo "  make dev           - Run without building (development)"
	@echo "  make deploy        - Deploy to Raspberry Pi"
	@echo "  make clean         - Remove build artifacts"
	@echo "  make test          - Run tests"
	@echo "  make tests         - Alias for 'make test'"
	@echo "  make test-cover    - Run tests with coverage reporting"
	@echo "  make test-race     - Run tests with the race detector"
	@echo "  make cover         - Generate coverage for internal packages"
	@echo "  make cover-html    - Render coverage HTML report"
	@echo "  make cover-threshold - Fail if coverage falls below 90%"
	@echo "  make deps          - Install dependencies"
	@echo "  make fmt           - Format code"
	@echo "  make fmt-fix       - Apply gofumpt/gofmt/goimports"
	@echo "  make lint          - Run linters"
	@echo "  make vet           - Run go vet"
	@echo "  make tools         - Install pinned developer tools"
