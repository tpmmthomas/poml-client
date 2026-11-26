GO ?= go
PYTHON ?= .venv/bin/python

.PHONY: all build test vet proto stage2-test stage2-benchmark

all: test build

build:
	$(GO) build ./cmd/...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

proto:
	protoc --go_out=. --go_opt=paths=source_relative api/proto/poml.proto

stage2-test:
	taskset -c 0-15 $(PYTHON) stage2/test_model_worker.py

stage2-benchmark:
	taskset -c 0-15 $(PYTHON) scripts/benchmark_stage2.py \
		--profile diffusion-small --device cpu --threads 16 --requests 8 \
		--output /tmp/poml-stage2-diffusion-benchmark.json
