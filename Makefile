BINARY  := deepseek-search
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST    := dist

PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: all build install test vet fmt cover clean release dist smoke schema lint

all: build

## build: compile the binary for the host platform into ./bin
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

## install: go install the command into $GOBIN
install:
	go install -trimpath -ldflags "$(LDFLAGS)" .

## test: run the unit tests
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go sources
fmt:
	gofmt -s -w .

## cover: run tests with coverage summary
cover:
	go test -cover ./...

## release: cross-compile every supported platform into ./dist
release: clean
	@mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		echo "building $$name"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o "$(DIST)/$$name$(BINARY)$$ext" . || exit 1; \
	done

## dist: build release archives with goreleaser (requires goreleaser)
dist:
	goreleaser release --snapshot --clean

## smoke: run the stdio and HTTP smoke tests against ./bin/$(BINARY)
smoke: build
	BIN=./bin/$(BINARY) ./scripts/smoke.sh

## schema: print the tool descriptions and schemas the server advertises
schema: build
	@python3 ./scripts/dump_schema.py --bin ./bin/$(BINARY)

clean:
	rm -rf bin $(DIST)

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'
