package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computev1 "peer-compute/gen/compute/v1"
)

var (
	flagProvider string
	flagImage    string
	flagCPU      uint32
	flagMemory   uint64
)

func init() {
	rootCmd.AddCommand(sessionCmd)
	sessionCmd.AddCommand(createSessionCmd)
	sessionCmd.AddCommand(getSessionCmd)
	sessionCmd.AddCommand(terminateSessionCmd)

	createSessionCmd.Flags().StringVar(&flagProvider, "provider", "", "provider ID")
	createSessionCmd.Flags().StringVar(&flagImage, "image", "ubuntu:22.04", "Docker image")
	createSessionCmd.Flags().Uint32Var(&flagCPU, "cpu", 1, "CPU cores")
	createSessionCmd.Flags().Uint64Var(&flagMemory, "memory", 1024, "memory in MB")
	_ = createSessionCmd.MarkFlagRequired("provider")
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

		client := computev1.NewRegistryServiceClient(conn)
		resp, err := client.CreateSession(context.Background(), &computev1.CreateSessionRequest{
			ProviderId: flagProvider,
			RenterId:   "cli-user",
			Requested: &computev1.Resources{
				CpuCores: flagCPU,
				MemoryMb: flagMemory,
			},
			Image: flagImage,
		})
		if err != nil {
			return err
		}

		fmt.Printf("Session created: %s (status: %s)\n", resp.Session.Id, resp.Session.Status)
		return nil
	},
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
		fmt.Printf("ID:        %s\n", s.Id)
		fmt.Printf("Provider:  %s\n", s.ProviderId)
		fmt.Printf("Status:    %s\n", s.Status)
		fmt.Printf("Image:     %s\n", s.Image)
		if s.Allocated != nil {
			fmt.Printf("CPU:       %d cores\n", s.Allocated.CpuCores)
			fmt.Printf("Memory:    %d MB\n", s.Allocated.MemoryMb)
		}
		fmt.Printf("Created:   %s\n", s.CreatedAt.AsTime())
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
