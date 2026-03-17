// Package serve provides a public API for running the Peer Compute registry server.
// This is the integration point for external projects (e.g., peer_compute_pro)
// that need to compose the registry with custom plugin implementations.
package serve

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/internal/logging"
	internalnat "github.com/OpenStruct/peer_compute/internal/nat"
	"github.com/OpenStruct/peer_compute/internal/registry"
	"github.com/OpenStruct/peer_compute/internal/relay"
	"github.com/OpenStruct/peer_compute/plugin"
)

// RegistryConfig holds configuration for the registry server.
type RegistryConfig struct {
	// GRPCPort is the port for the gRPC server (default: 50051).
	GRPCPort string

	// STUNPort is the UDP port for the STUN server (default: 3478).
	STUNPort string

	// RelayPort is the UDP port for the relay server (default: 3479).
	RelayPort string

	// Plugins provides custom implementations for auth, reputation, and storage.
	// If nil, open-source defaults are used.
	Plugins *plugin.Bundle

	// GRPCOptions are additional gRPC server options (e.g., interceptors).
	GRPCOptions []grpc.ServerOption
}

// Registry starts the full registry server (gRPC + STUN + Relay) and blocks until ctx is cancelled.
func Registry(ctx context.Context, cfg RegistryConfig) error {
	log := logging.New("registry")

	if cfg.GRPCPort == "" {
		cfg.GRPCPort = "50051"
	}
	if cfg.STUNPort == "" {
		cfg.STUNPort = "3478"
	}
	if cfg.RelayPort == "" {
		cfg.RelayPort = "3479"
	}

	// Start relay server
	relaySrv := relay.NewRelayServer(":"+cfg.RelayPort, log.With("sub", "relay"))
	go func() {
		if err := relaySrv.Run(ctx); err != nil {
			log.Error("relay server error", "error", err)
		}
	}()

	// Start STUN server
	go func() {
		if err := internalnat.RunSTUNServer(ctx, ":"+cfg.STUNPort, log.With("sub", "stun")); err != nil {
			log.Error("stun server error", "error", err)
		}
	}()

	// Build registry server with plugins
	var opts []registry.Option
	if cfg.Plugins != nil {
		opts = append(opts, registry.WithPlugins(cfg.Plugins))
	}
	srv := registry.NewServer(relaySrv, opts...)

	// Build gRPC server
	grpcServer := grpc.NewServer(cfg.GRPCOptions...)
	computev1.RegisterRegistryServiceServer(grpcServer, srv)
	reflection.Register(grpcServer)

	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down registry")
		grpcServer.GracefulStop()
	}()

	log.Info("registry listening", "grpc_port", cfg.GRPCPort, "stun_port", cfg.STUNPort, "relay_port", cfg.RelayPort)
	return grpcServer.Serve(lis)
}
