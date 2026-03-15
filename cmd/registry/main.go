package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/internal/nat"
	"github.com/OpenStruct/peer_compute/internal/registry"
	"github.com/OpenStruct/peer_compute/internal/relay"
)

func main() {
	port := envOr("PORT", "50051")
	stunPort := envOr("STUN_PORT", "3478")
	relayPort := envOr("RELAY_PORT", "3479")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start relay server
	relayAddr := ":" + relayPort
	relaySrv := relay.NewRelayServer(relayAddr)
	go func() {
		if err := relaySrv.Run(ctx); err != nil {
			log.Printf("relay server error: %v", err)
		}
	}()

	// Start STUN server
	go func() {
		if err := nat.RunSTUNServer(ctx, ":"+stunPort); err != nil {
			log.Printf("stun server error: %v", err)
		}
	}()

	// Start gRPC server
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	srv := registry.NewServer(relaySrv)

	grpcServer := grpc.NewServer()
	computev1.RegisterRegistryServiceServer(grpcServer, srv)
	reflection.Register(grpcServer)

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		grpcServer.GracefulStop()
	}()

	log.Printf("registry listening on :%s (stun=:%s, relay=:%s)", port, stunPort, relayPort)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
