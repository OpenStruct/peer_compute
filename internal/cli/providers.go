package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
)

func init() {
	rootCmd.AddCommand(providersCmd)
}

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List available compute providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := grpc.NewClient(registryAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return err
		}
		defer conn.Close()

		client := computev1.NewRegistryServiceClient(conn)
		resp, err := client.ListProviders(context.Background(), &computev1.ListProvidersRequest{})
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCPU\tMEMORY\tGPU")
		for _, p := range resp.Providers {
			id := p.Id
			if len(id) > 8 {
				id = id[:8]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d cores\t%d MB\t%d\n",
				id, p.Name, p.Status,
				p.Available.CpuCores, p.Available.MemoryMb, p.Available.GpuCount)
		}
		return w.Flush()
	},
}
