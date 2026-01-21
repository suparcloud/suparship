# Makefile for suparship

# Build variables
BINARY_NAME := suparship
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X github.com/suparcloud/suparship/internal/version.Version=$(VERSION) \
           -X github.com/suparcloud/suparship/internal/version.Commit=$(COMMIT) \
           -X github.com/suparcloud/suparship/internal/version.Date=$(DATE)

# Go settings
GOBIN := $(shell go env GOPATH)/bin

.PHONY: all build test lint fmt clean help

all: build

## build: Build the suparship binary
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) ./cmd/suparship

## test: Run all tests
test:
	go test -race -coverprofile=coverage.out ./...

## lint: Run linters (requires golangci-lint)
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping lint"; \
		echo "Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

## fmt: Format code with gofumpt (falls back to gofmt)
fmt:
	@if command -v gofumpt >/dev/null 2>&1; then \
		gofumpt -w .; \
	else \
		gofmt -w .; \
	fi

## clean: Remove build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
