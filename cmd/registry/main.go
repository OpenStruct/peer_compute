package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenStruct/peer_compute/serve"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := serve.Registry(ctx, serve.RegistryConfig{
		GRPCPort:  envOr("PORT", "50051"),
		STUNPort:  envOr("STUN_PORT", "3478"),
		RelayPort: envOr("RELAY_PORT", "3479"),
	})
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
