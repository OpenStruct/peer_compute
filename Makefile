.PHONY: proto build test clean lint dev

PROTO_DIR := proto
GEN_DIR   := gen

proto:
	@mkdir -p $(GEN_DIR)/compute/v1
	protoc \
		--proto_path=$(PROTO_DIR) \
		--go_out=$(GEN_DIR) --go_opt=paths=source_relative \
		--go-grpc_out=$(GEN_DIR) --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/compute/v1/compute.proto

build: proto
	go build -o bin/registry ./cmd/registry
	go build -o bin/agent ./cmd/agent
	go build -o bin/peerctl ./cmd/peerctl

test:
	go test ./...

clean:
	rm -rf bin/ $(GEN_DIR)/

lint:
	golangci-lint run ./...

dev:
	docker compose up -d postgres redis

.DEFAULT_GOAL := build
