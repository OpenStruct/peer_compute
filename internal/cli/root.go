package cli

import (
	"github.com/spf13/cobra"
)

var registryAddr string

var rootCmd = &cobra.Command{
	Use:   "peerctl",
	Short: "CLI for the peer compute network",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&registryAddr, "registry", "localhost:50051", "registry gRPC address")
}

func Execute() error {
	return rootCmd.Execute()
}
