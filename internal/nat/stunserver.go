package nat

import (
	"context"
	"log/slog"
	"net"
)

// RunSTUNServer listens on the given UDP address and echoes back the
// observed source IP:port to callers. This is a minimal STUN-like service
// (not RFC 5389 compliant) used only for endpoint discovery.
func RunSTUNServer(ctx context.Context, listenAddr string, log *slog.Logger) error {
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Info("stun server listening", "addr", listenAddr)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 256)
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Warn("stun read error", "error", err)
				continue
			}
		}

		if n < 12 || string(buf[:4]) != string(stunRequestMagic) {
			continue
		}

		txnID := buf[4:12]

		// Response: magic (4) + txn ID (8) + observed addr (string)
		observedAddr := raddr.String()
		resp := make([]byte, 0, 12+len(observedAddr))
		resp = append(resp, stunResponseMagic...)
		resp = append(resp, txnID...)
		resp = append(resp, []byte(observedAddr)...)

		conn.WriteToUDP(resp, raddr)
	}
}
