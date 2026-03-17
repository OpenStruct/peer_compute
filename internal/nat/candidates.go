package nat

import (
	"context"
	"fmt"
	"log/slog"
	"net"
)

// Candidate represents a network endpoint that a peer can be reached at.
type Candidate struct {
	Address  string // ip:port
	Type     string // "host", "srflx" (server-reflexive), "relay"
	Priority int64  // higher = preferred
}

// GatherCandidates discovers local and public endpoints for a given port.
// It enumerates local interfaces (host candidates) and queries the STUN
// server for a server-reflexive candidate.
func GatherCandidates(ctx context.Context, stunAddr string, localPort int, log *slog.Logger) ([]Candidate, error) {
	var candidates []Candidate

	// 1. Host candidates from local interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}

			candidates = append(candidates, Candidate{
				Address:  fmt.Sprintf("%s:%d", ip.String(), localPort),
				Type:     "host",
				Priority: 100,
			})
		}
	}

	// 2. Server-reflexive candidate via STUN
	if stunAddr != "" {
		pubAddr, err := DiscoverEndpoint(ctx, stunAddr, 0)
		if err != nil {
			log.Debug("stun discovery failed", "error", err)
		} else {
			candidates = append(candidates, Candidate{
				Address:  pubAddr,
				Type:     "srflx",
				Priority: 200,
			})
		}
	}

	return candidates, nil
}
