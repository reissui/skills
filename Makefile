# clex — Makefile
# Build/test/lint targets for the clex CLI and clexd daemon.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/reissui/clex/internal/version.Version=$(VERSION)
GO      ?= go

.PHONY: all build test lint vet fmt tidy clean

all: build

## build: compile clex and clexd into ./bin
build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/clex  ./cmd/clex
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/clexd ./cmd/clexd

## test: run the full test suite
test:
	$(GO) test ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## lint: static checks (go vet; gofmt must report no diffs)
lint: vet
	@diff=$$(gofmt -l .); if [ -n "$$diff" ]; then \
		echo "gofmt needs to run on:"; echo "$$diff"; exit 1; \
	fi

## fmt: format all Go source
fmt:
	gofmt -w .

## tidy: tidy module dependencies
tidy:
	$(GO) mod tidy

## clean: remove build artifacts
clean:
	rm -rf bin
