# dhcp-tee — build and test targets.
#
# Portable targets (build/test/vet/fmt) run on any OS. The service binary itself
# targets Linux (AF_PACKET), so `build` cross-compiles for Linux by default.
# `test-integration` runs the real pipeline and is Linux + root only; use
# `test-integration-docker` to run it reproducibly from macOS/Windows.

BIN        := bin/dhcp-tee
PKG        := ./...
GOARCH     ?= arm64          # t4g default; use amd64 for x86 instances
GOOS       ?= linux
CGO_ENABLED ?= 0

.PHONY: all build test test-race vet fmt fmt-check tidy lint \
        test-integration test-integration-docker clean

all: fmt-check vet test build

## build: static Linux binary for the target arch
build:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags="-s -w" -o $(BIN) .

## test: portable unit tests (run on any OS)
test:
	go test $(PKG)

## test-race: unit tests with the race detector (used in CI)
test-race:
	go test -race $(PKG)

## vet: go vet static checks
vet:
	go vet $(PKG)

## fmt: format all Go files
fmt:
	gofmt -w .

## fmt-check: fail if any file is not gofmt-clean (used in CI)
fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then \
		echo "these files are not gofmt-clean:"; echo "$$out"; exit 1; fi

## tidy: sync go.mod/go.sum
tidy:
	go mod tidy

## test-integration: real end-to-end pipeline (Linux + root only)
test-integration:
	bash testdata/integration-test.sh

## test-integration-docker: run the integration test in a privileged container
test-integration-docker:
	docker build -f testdata/Dockerfile -t dhcp-tee-itest .
	docker run --rm --privileged dhcp-tee-itest

## clean: remove build artifacts
clean:
	rm -rf bin
