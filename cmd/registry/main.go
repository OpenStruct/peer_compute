package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenStruct/peer_compute/internal/store"
	"github.com/OpenStruct/peer_compute/plugin"
	"github.com/OpenStruct/peer_compute/serve"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := serve.RegistryConfig{
		GRPCPort:  envOr("PORT", "50051"),
		STUNPort:  envOr("STUN_PORT", "3478"),
		RelayPort: envOr("RELAY_PORT", "3479"),
	}

	// If DATABASE_URL is set, use PostgreSQL; otherwise default to in-memory store.
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pgStore, err := store.New(dsn)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer pgStore.Close()

		// Auto-apply schema on startup
		if err := store.Migrate(pgStore.DB()); err != nil {
			log.Fatalf("migrations: %v", err)
		}

		cfg.Plugins = &plugin.Bundle{
			Auth:       plugin.OpenAuth{},
			Reputation: plugin.OpenReputation{},
			Store:      pgStore,
		}
		log.Println("using PostgreSQL store")
	}

	err := serve.Registry(ctx, cfg)
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
