package nat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSTUNServerAndDiscover(t *testing.T) {
	port := freeUDPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunSTUNServer(serverCtx, addr, discardLogger())
	}()

	// Give the server a moment to bind.
	time.Sleep(100 * time.Millisecond)

	localPort := freeUDPPort(t)
	observed, err := DiscoverEndpoint(ctx, addr, localPort)
	if err != nil {
		t.Fatalf("DiscoverEndpoint failed: %v", err)
	}

	host, portStr, err := net.SplitHostPort(observed)
	if err != nil {
		t.Fatalf("observed address %q is not valid host:port: %v", observed, err)
	}
	if net.ParseIP(host) == nil {
		t.Fatalf("observed host %q is not a valid IP", host)
	}
	if portStr == "" {
		t.Fatal("observed port is empty")
	}

	serverCancel()
}

func TestGatherCandidates_LocalOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	localPort := freeUDPPort(t)
	candidates, err := GatherCandidates(ctx, "", localPort, discardLogger())
	if err != nil {
		t.Fatalf("GatherCandidates failed: %v", err)
	}

	if len(candidates) == 0 {
		t.Fatal("expected at least one host candidate, got none")
	}

	for _, c := range candidates {
		if c.Type != "host" {
			t.Errorf("expected Type \"host\" for local-only gathering, got %q", c.Type)
		}
		if c.Priority != 100 {
			t.Errorf("expected Priority 100 for host candidate, got %d", c.Priority)
		}
		// The address may be IPv6 without brackets, so try SplitHostPort first,
		// and if it fails due to too many colons, parse as IPv6:port manually.
		host, portStr, err := net.SplitHostPort(c.Address)
		if err != nil {
			// Try wrapping IPv6 in brackets: find the last colon as port separator.
			lastColon := len(c.Address) - 1
			for lastColon >= 0 && c.Address[lastColon] != ':' {
				lastColon--
			}
			if lastColon <= 0 {
				t.Errorf("candidate address %q is not valid host:port: %v", c.Address, err)
				continue
			}
			host = c.Address[:lastColon]
			portStr = c.Address[lastColon+1:]
		}
		if net.ParseIP(host) == nil {
			t.Errorf("candidate host %q is not a valid IP", host)
		}
		if portStr == "" {
			t.Errorf("candidate port is empty")
		}
	}
}

func TestCandidate_Types(t *testing.T) {
	tests := []struct {
		name     string
		candidate Candidate
	}{
		{"host", Candidate{Address: "1.2.3.4:5000", Type: "host", Priority: 100}},
		{"srflx", Candidate{Address: "5.6.7.8:6000", Type: "srflx", Priority: 200}},
		{"relay", Candidate{Address: "9.10.11.12:7000", Type: "relay", Priority: 50}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.candidate.Type != tt.name {
				t.Errorf("expected Type %q, got %q", tt.name, tt.candidate.Type)
			}
		})
	}
}

func TestProbe_EmptyCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	localPort := freeUDPPort(t)
	_, err := Probe(ctx, localPort, nil, 100*time.Millisecond, discardLogger())
	if !errors.Is(err, ErrHolePunchFailed) {
		t.Fatalf("expected ErrHolePunchFailed, got %v", err)
	}
}

func TestProbe_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	localPort := freeUDPPort(t)
	unreachable := []Candidate{
		{Address: "192.0.2.1:9999", Type: "host", Priority: 100},
	}

	_, err := Probe(ctx, localPort, unreachable, 100*time.Millisecond, discardLogger())
	if !errors.Is(err, ErrHolePunchFailed) {
		t.Fatalf("expected ErrHolePunchFailed, got %v", err)
	}
}

func TestMagicBytes(t *testing.T) {
	tests := []struct {
		name     string
		got      []byte
		expected string
	}{
		{"stunRequestMagic", stunRequestMagic, "PCST"},
		{"stunResponseMagic", stunResponseMagic, "PCSR"},
		{"probeMagic", probeMagic, "PCPR"},
		{"probeAckMagic", probeAckMagic, "PCPA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.got) != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, string(tt.got))
			}
		})
	}
}
