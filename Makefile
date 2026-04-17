BINARY_NAME := intern
MODULE      := github.com/abhishekjha17/intern

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -ldflags "\
	-X main.Version=$(VERSION) \
	-X main.Commit=$(COMMIT) \
	-X main.Date=$(DATE)"

.PHONY: build install test lint fmt clean

build:
	go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/intern

install:
	go install $(LDFLAGS) ./cmd/intern

test:
	go test -v -race ./...

lint:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/
