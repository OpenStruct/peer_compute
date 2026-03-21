package relay

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"
)

// RelayClient sits between the local WireGuard interface and the relay server.
// WireGuard is configured with Endpoint = 127.0.0.1:<ProxyPort>.
// The client forwards packets bidirectionally, adding/stripping the relay header.
type RelayClient struct {
	relayAddr *net.UDPAddr
	tokenID   []byte // 16-byte token identifier
	proxyPort int    // local port WireGuard connects to
	wgPort    int    // local WireGuard listen port to forward incoming packets to
	log       *slog.Logger
}

// NewRelayClient creates a relay client.
// proxyPort: the local port that WireGuard's Peer Endpoint points to.
// wgPort: the local WireGuard listen port for forwarding inbound traffic.
func NewRelayClient(relayAddr string, token string, proxyPort, wgPort int, log *slog.Logger) (*RelayClient, error) {
	raddr, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve relay addr: %w", err)
	}

	return &RelayClient{
		relayAddr: raddr,
		tokenID:   TokenIDBytes(token),
		proxyPort: proxyPort,
		wgPort:    wgPort,
		log:       log,
	}, nil
}

// ProxyPort returns the local port that WireGuard should use as its peer endpoint.
func (rc *RelayClient) ProxyPort() int {
	return rc.proxyPort
}

// Run starts bidirectional forwarding. Blocks until ctx is cancelled.
func (rc *RelayClient) Run(ctx context.Context) error {
	// Local proxy socket — WireGuard sends packets here
	proxyAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: rc.proxyPort}
	proxyConn, err := net.ListenUDP("udp", proxyAddr)
	if err != nil {
		return fmt.Errorf("bind proxy port %d: %w", rc.proxyPort, err)
	}
	defer proxyConn.Close()

	// Connection to relay server
	relayConn, err := net.DialUDP("udp", nil, rc.relayAddr)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer relayConn.Close()

	rc.log.Info("relay client running", "proxy_port", rc.proxyPort, "relay_addr", rc.relayAddr)

	go func() {
		<-ctx.Done()
		proxyConn.Close()
		relayConn.Close()
	}()

	// Track the WireGuard peer address (learned from first packet)
	var wgPeer *net.UDPAddr

	// Build relay header: magic (4) + tokenID (16) = 20 bytes
	header := make([]byte, 0, headerLen)
	header = append(header, relayMagic...)
	header = append(header, rc.tokenID...)

	// WireGuard -> Relay (outbound)
	go func() {
		buf := make([]byte, 65536)

		for {
			proxyConn.SetReadDeadline(time.Now().Add(30 * time.Second))
			n, raddr, err := proxyConn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}

			wgPeer = raddr

			// Prepend relay header and send to relay server
			pkt := make([]byte, 0, headerLen+n)
			pkt = append(pkt, header...)
			pkt = append(pkt, buf[:n]...)
			relayConn.Write(pkt)
		}
	}()

	// Relay -> WireGuard (inbound)
	buf := make([]byte, 65536)
	for {
		relayConn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := relayConn.Read(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}

		if wgPeer == nil {
			continue
		}

		// Forward raw payload to WireGuard
		proxyConn.WriteToUDP(buf[:n], wgPeer)
	}
}
