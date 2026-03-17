package nat

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"
)

var (
	probeMagic         = []byte("PCPR") // Peer Compute PRobe
	probeAckMagic      = []byte("PCPA") // Peer Compute Probe Ack
	ErrHolePunchFailed = errors.New("hole punch failed: no candidates reachable")
)

// Probe attempts UDP hole-punching against remote candidates.
// It sends probe packets to all remote candidates and listens for responses.
// Returns the first working remote address, or ErrHolePunchFailed on timeout.
func Probe(ctx context.Context, localPort int, remoteCandidates []Candidate, timeout time.Duration, log *slog.Logger) (string, error) {
	if len(remoteCandidates) == 0 {
		return "", ErrHolePunchFailed
	}

	laddr := &net.UDPAddr{Port: localPort}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return "", fmt.Errorf("bind probe port %d: %w", localPort, err)
	}
	defer conn.Close()

	// Generate a random probe ID
	probeID := make([]byte, 8)
	rand.Read(probeID)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Result channel
	result := make(chan string, 1)

	// Listener goroutine: wait for incoming probes or acks
	go func() {
		buf := make([]byte, 64)
		for {
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}

			if n >= 4 {
				magic := string(buf[:4])
				switch magic {
				case string(probeMagic):
					// Received a probe — send ack back
					ack := append(probeAckMagic, probeID...)
					conn.WriteToUDP(ack, raddr)
					// Also treat receiving a probe as success
					select {
					case result <- raddr.String():
					default:
					}
				case string(probeAckMagic):
					// Received an ack — hole punch succeeded
					select {
					case result <- raddr.String():
					default:
					}
				}
			}
		}
	}()

	// Sender: periodically send probes to all candidates
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		sendProbes := func() {
			for _, c := range remoteCandidates {
				raddr, err := net.ResolveUDPAddr("udp", c.Address)
				if err != nil {
					continue
				}
				pkt := append(probeMagic, probeID...)
				conn.WriteToUDP(pkt, raddr)
			}
		}

		sendProbes() // send immediately
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendProbes()
			}
		}
	}()

	select {
	case addr := <-result:
		log.Debug("hole punch succeeded", "addr", addr)
		return addr, nil
	case <-ctx.Done():
		return "", ErrHolePunchFailed
	}
}
