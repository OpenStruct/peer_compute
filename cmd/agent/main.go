package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"peer-compute/internal/agent"
)

func main() {
	registryAddr := envOr("REGISTRY_ADDR", "localhost:50051")

	conn, err := grpc.NewClient(registryAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect to registry: %v", err)
	}
	defer conn.Close()

	registryHost := registryAddr
	if h, _, err := net.SplitHostPort(registryAddr); err == nil {
		registryHost = h
	}

	daemon, err := agent.NewDaemon(agent.Config{
		Name:              envOr("PROVIDER_NAME", hostname()),
		Address:           envOr("PROVIDER_ADDR", "localhost:50052"),
		RegistryConn:      conn,
		HeartbeatInterval: 10 * time.Second,
		CPUCores:          4,
		MemoryMB:          8192,
		DiskGB:            100,
		STUNAddr:          envOr("STUN_ADDR", registryHost+":3478"),
		RelayAddr:         envOr("RELAY_ADDR", registryHost+":3479"),
	})
	if err != nil {
		log.Fatalf("failed to create daemon: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Run(ctx); err != nil {
		log.Fatalf("daemon error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
