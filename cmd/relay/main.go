package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/OpenStruct/peer_compute/internal/logging"
	"github.com/OpenStruct/peer_compute/internal/relay"
)

func main() {
	port := flag.Int("port", 3479, "UDP relay port")
	healthPort := flag.Int("health-port", 9092, "HTTP health check port")
	flag.Parse()

	logger := logging.New("relay")

	addr := fmt.Sprintf(":%d", *port)
	rs := relay.NewRelayServer(addr, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start health check endpoint.
	go func() {
		if err := rs.ServeHealthz(*healthPort); err != nil {
			log.Fatalf("healthz server failed: %v", err)
		}
	}()

	// Start the relay server.
	go func() {
		if err := rs.Run(ctx); err != nil {
			log.Fatalf("relay server failed: %v", err)
		}
	}()

	logger.Info("relay server started", "port", *port, "health_port", *healthPort)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down relay server")
	cancel()
}
