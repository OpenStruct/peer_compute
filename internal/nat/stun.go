package nat

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"time"
)

// Magic prefixes for our lightweight STUN-like protocol.
var (
	stunRequestMagic  = []byte("PCST") // Peer Compute STun
	stunResponseMagic = []byte("PCSR") // Peer Compute Stun Response
)

// DiscoverEndpoint sends a STUN-like request to the given server and returns
// the observed public IP:port. The localPort is the UDP port to bind to
// (should match the WireGuard listen port for accurate NAT mapping).
func DiscoverEndpoint(ctx context.Context, stunAddr string, localPort int) (string, error) {
	raddr, err := net.ResolveUDPAddr("udp", stunAddr)
	if err != nil {
		return "", fmt.Errorf("resolve stun addr: %w", err)
	}

	laddr := &net.UDPAddr{Port: localPort}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return "", fmt.Errorf("bind udp :%d: %w", localPort, err)
	}
	defer conn.Close()

	// Build request: magic (4 bytes) + txn ID (8 bytes)
	txnID := make([]byte, 8)
	rand.Read(txnID)
	req := append(stunRequestMagic, txnID...)

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	conn.SetDeadline(deadline)

	if _, err := conn.WriteToUDP(req, raddr); err != nil {
		return "", fmt.Errorf("send stun request: %w", err)
	}

	buf := make([]byte, 256)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return "", fmt.Errorf("read stun response: %w", err)
	}

	resp := buf[:n]
	if len(resp) < 12 || string(resp[:4]) != string(stunResponseMagic) {
		return "", fmt.Errorf("invalid stun response")
	}

	// Verify txn ID matches
	for i := 0; i < 8; i++ {
		if resp[4+i] != txnID[i] {
			return "", fmt.Errorf("stun txn ID mismatch")
		}
	}

	// Remaining bytes are the observed address as a string
	addr := string(resp[12:])
	return addr, nil
}
