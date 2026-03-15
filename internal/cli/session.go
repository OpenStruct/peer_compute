package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/internal/agent"
	"github.com/OpenStruct/peer_compute/internal/nat"
	"github.com/OpenStruct/peer_compute/internal/relay"
)

var (
	flagProvider string
	flagImage    string
	flagCPU      uint32
	flagMemory   uint64
	flagConnect  bool
	flagSTUN     string
)

func init() {
	rootCmd.AddCommand(sessionCmd)
	sessionCmd.AddCommand(createSessionCmd)
	sessionCmd.AddCommand(getSessionCmd)
	sessionCmd.AddCommand(terminateSessionCmd)
	sessionCmd.AddCommand(connectSessionCmd)

	createSessionCmd.Flags().StringVar(&flagProvider, "provider", "", "provider ID")
	createSessionCmd.Flags().StringVar(&flagImage, "image", "ubuntu:22.04", "Docker image")
	createSessionCmd.Flags().Uint32Var(&flagCPU, "cpu", 1, "CPU cores")
	createSessionCmd.Flags().Uint64Var(&flagMemory, "memory", 1024, "memory in MB")
	createSessionCmd.Flags().BoolVar(&flagConnect, "connect", false, "establish WireGuard tunnel after creation")
	createSessionCmd.Flags().StringVar(&flagSTUN, "stun", "localhost:3478", "STUN server address")
	_ = createSessionCmd.MarkFlagRequired("provider")

	connectSessionCmd.Flags().StringVar(&flagSTUN, "stun", "localhost:3478", "STUN server address")
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage compute sessions",
}

var createSessionCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new compute session",
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := dialRegistry()
		if err != nil {
			return err
		}
		defer conn.Close()

		// Generate renter WireGuard keypair
		kp, err := agent.GenerateKeyPair()
		if err != nil {
			return fmt.Errorf("keygen: %w", err)
		}

		client := computev1.NewRegistryServiceClient(conn)
		resp, err := client.CreateSession(context.Background(), &computev1.CreateSessionRequest{
			ProviderId: flagProvider,
			RenterId:   "cli-user",
			Requested: &computev1.Resources{
				CpuCores: flagCPU,
				MemoryMb: flagMemory,
			},
			Image:       flagImage,
			WgPublicKey: kp.PublicKey,
		})
		if err != nil {
			return err
		}

		sess := resp.Session
		fmt.Printf("Session created: %s (status: %s)\n", sess.Id, sess.Status)

		if flagConnect {
			return doConnect(client, sess.Id, kp)
		}

		fmt.Printf("Run 'peerctl session connect %s' to establish the tunnel.\n", sess.Id)
		return nil
	},
}

var connectSessionCmd = &cobra.Command{
	Use:   "connect [session-id]",
	Short: "Establish WireGuard tunnel to a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := dialRegistry()
		if err != nil {
			return err
		}
		defer conn.Close()

		// Generate renter WireGuard keypair
		kp, err := agent.GenerateKeyPair()
		if err != nil {
			return fmt.Errorf("keygen: %w", err)
		}

		client := computev1.NewRegistryServiceClient(conn)
		return doConnect(client, args[0], kp)
	},
}

func doConnect(client computev1.RegistryServiceClient, sessionID string, kp *agent.WGKeyPair) error {
	ctx := context.Background()
	wgPort := 51821 // renter WG port

	// 1. Gather candidates
	fmt.Println("Discovering network endpoints...")
	candidates, err := nat.GatherCandidates(ctx, flagSTUN, wgPort)
	if err != nil {
		log.Printf("candidate gathering: %v", err)
	}

	protoCandidates := make([]*computev1.EndpointCandidate, len(candidates))
	for i, c := range candidates {
		protoCandidates[i] = &computev1.EndpointCandidate{
			Address:  c.Address,
			Type:     c.Type,
			Priority: c.Priority,
		}
	}

	// 2. Exchange candidates with provider via registry
	fmt.Println("Exchanging endpoints with provider...")
	exchResp, err := client.ExchangeCandidates(ctx, &computev1.ExchangeCandidatesRequest{
		SessionId:   sessionID,
		PeerId:      "cli-user",
		Candidates:  protoCandidates,
		WgPublicKey: kp.PublicKey,
	})
	if err != nil {
		return fmt.Errorf("candidate exchange: %w", err)
	}

	// If no peer candidates yet, poll briefly
	if len(exchResp.PeerCandidates) == 0 {
		fmt.Println("Waiting for provider to submit candidates...")
		for i := 0; i < 10; i++ {
			time.Sleep(2 * time.Second)
			exchResp, err = client.ExchangeCandidates(ctx, &computev1.ExchangeCandidatesRequest{
				SessionId:   sessionID,
				PeerId:      "cli-user",
				Candidates:  protoCandidates,
				WgPublicKey: kp.PublicKey,
			})
			if err != nil {
				return err
			}
			if len(exchResp.PeerCandidates) > 0 {
				break
			}
		}
	}

	peerWGKey := exchResp.GetPeerWgPublicKey()
	var peerEndpoint string
	var connMode string

	// 3. Attempt hole-punching
	if len(exchResp.GetPeerCandidates()) > 0 {
		fmt.Println("Attempting direct connection...")
		remoteCandidates := make([]nat.Candidate, len(exchResp.PeerCandidates))
		for i, c := range exchResp.PeerCandidates {
			remoteCandidates[i] = nat.Candidate{
				Address:  c.Address,
				Type:     c.Type,
				Priority: c.Priority,
			}
		}

		addr, err := nat.Probe(ctx, wgPort, remoteCandidates, 10*time.Second)
		if err == nil {
			peerEndpoint = addr
			connMode = "holepunch"
			fmt.Printf("Direct connection established: %s\n", addr)
		} else if errors.Is(err, nat.ErrHolePunchFailed) {
			fmt.Println("Direct connection failed, using relay...")
		}
	}

	// 4. Relay fallback
	if connMode == "" && exchResp.GetRelayAddress() != "" {
		proxyPort := wgPort + 10000
		rc, err := relay.NewRelayClient(exchResp.RelayAddress, exchResp.RelayToken, proxyPort, wgPort)
		if err != nil {
			return fmt.Errorf("relay client: %w", err)
		}

		relayCtx, relayCancel := context.WithCancel(ctx)
		go rc.Run(relayCtx)

		peerEndpoint = fmt.Sprintf("127.0.0.1:%d", proxyPort)
		connMode = "relay"

		client.ReportConnectionResult(ctx, &computev1.ReportConnectionResultRequest{
			SessionId: sessionID,
			PeerId:    "cli-user",
			UseRelay:  true,
		})

		// Clean up relay on exit
		defer relayCancel()
	}

	if connMode == "" {
		connMode = "direct"
	}

	// Report holepunch result
	if connMode == "holepunch" {
		client.ReportConnectionResult(ctx, &computev1.ReportConnectionResultRequest{
			SessionId:        sessionID,
			PeerId:           "cli-user",
			SelectedEndpoint: peerEndpoint,
		})
	}

	// 5. Write WireGuard config
	confPath, err := agent.WriteConfig(sessionID, agent.WGConfig{
		PrivateKey:    kp.PrivateKey,
		TunnelIP:      "10.99.0.2",
		ListenPort:    wgPort,
		PeerPublicKey: peerWGKey,
		PeerIP:        "10.99.0.1",
		PeerEndpoint:  peerEndpoint,
	})
	if err != nil {
		return fmt.Errorf("write wg config: %w", err)
	}

	fmt.Printf("\nConnection established (%s)\n", connMode)
	fmt.Printf("WireGuard config: %s\n", confPath)
	fmt.Printf("Activate with:    sudo wg-quick up %s\n", confPath)
	fmt.Printf("Tunnel IP:        10.99.0.2 -> 10.99.0.1 (provider)\n")

	if connMode == "relay" {
		fmt.Println("\nRelay is active. Keep this process running to maintain the tunnel.")
		// Block until interrupt
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\nDisconnecting...")
	}

	return nil
}

var getSessionCmd = &cobra.Command{
	Use:   "get [session-id]",
	Short: "Get session details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := dialRegistry()
		if err != nil {
			return err
		}
		defer conn.Close()

		client := computev1.NewRegistryServiceClient(conn)
		resp, err := client.GetSession(context.Background(), &computev1.GetSessionRequest{
			SessionId: args[0],
		})
		if err != nil {
			return err
		}

		s := resp.Session
		fmt.Printf("ID:         %s\n", s.Id)
		fmt.Printf("Provider:   %s\n", s.ProviderId)
		fmt.Printf("Status:     %s\n", s.Status)
		fmt.Printf("Image:      %s\n", s.Image)
		fmt.Printf("Connection: %s\n", s.ConnectionMode)
		if s.Allocated != nil {
			fmt.Printf("CPU:        %d cores\n", s.Allocated.CpuCores)
			fmt.Printf("Memory:     %d MB\n", s.Allocated.MemoryMb)
		}
		if s.SshEndpoint != "" {
			fmt.Printf("SSH:        %s\n", s.SshEndpoint)
		}
		fmt.Printf("Created:    %s\n", s.CreatedAt.AsTime())
		return nil
	},
}

var terminateSessionCmd = &cobra.Command{
	Use:   "terminate [session-id]",
	Short: "Terminate a compute session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := dialRegistry()
		if err != nil {
			return err
		}
		defer conn.Close()

		client := computev1.NewRegistryServiceClient(conn)
		resp, err := client.TerminateSession(context.Background(), &computev1.TerminateSessionRequest{
			SessionId: args[0],
		})
		if err != nil {
			return err
		}

		if resp.Success {
			fmt.Println("Session terminated.")
		}
		return nil
	},
}

func dialRegistry() (*grpc.ClientConn, error) {
	return grpc.NewClient(registryAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}
