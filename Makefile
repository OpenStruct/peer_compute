.PHONY: proto build test clean lint dev migrate-up migrate-down

DATABASE_URL ?= postgres://peercompute:peercompute@localhost:5432/peercompute?sslmode=disable

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
	go build -o bin/relay ./cmd/relay

agent-cross:
	GOOS=darwin GOARCH=arm64 go build -o bin/pcp-agent-darwin-arm64 ./cmd/agent
	GOOS=darwin GOARCH=amd64 go build -o bin/pcp-agent-darwin-amd64 ./cmd/agent
	GOOS=linux GOARCH=amd64 go build -o bin/pcp-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o bin/pcp-agent-linux-arm64 ./cmd/agent

test:
	go test ./...


clean:
	rm -rf bin/ $(GEN_DIR)/

lint:
	golangci-lint run ./...

dev:
	docker compose up -d postgres redis

migrate-up:
	psql "$(DATABASE_URL)" -f migrations/001_init.up.sql

migrate-down:
	psql "$(DATABASE_URL)" -f migrations/001_init.down.sql

.DEFAULT_GOAL := build
