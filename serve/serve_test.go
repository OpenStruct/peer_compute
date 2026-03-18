package serve_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/OpenStruct/peer_compute/plugin"
	"github.com/OpenStruct/peer_compute/serve"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}

func TestRegistry_StartsAndStops(t *testing.T) {
	grpcPort := freeTCPPort(t)
	stunPort := freeUDPPort(t)
	relayPort := freeUDPPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCtx, serverCancel := context.WithCancel(ctx)

	cfg := serve.RegistryConfig{
		GRPCPort:  fmt.Sprintf("%d", grpcPort),
		STUNPort:  fmt.Sprintf("%d", stunPort),
		RelayPort: fmt.Sprintf("%d", relayPort),
		Plugins:   plugin.Defaults(),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve.Registry(serverCtx, cfg)
	}()

	// Give the servers a moment to start.
	time.Sleep(200 * time.Millisecond)

	// Cancel the context to trigger shutdown.
	serverCancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Registry returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Registry did not shut down within 3 seconds after context cancellation")
	}
}
