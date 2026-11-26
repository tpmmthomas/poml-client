GO ?= go

.PHONY: all build test vet proto

all: test build

build:
	$(GO) build ./cmd/...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

proto:
	protoc --go_out=. --go_opt=paths=source_relative api/proto/poml.proto
