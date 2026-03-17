package relay

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"log/slog"
	"net"
	"sync"
	"time"
)

var relayMagic = []byte("PCRL") // Peer Compute ReLay

// RelayServer proxies encrypted UDP packets between two peers that
// cannot establish a direct connection (symmetric NAT). Both peers
// send WireGuard UDP traffic here; the server forwards it to the other side.
type RelayServer struct {
	mu       sync.RWMutex
	sessions map[uint32]*relaySession // token hash -> session
	conn     *net.UDPConn
	addr     string
	log      *slog.Logger
}

type relaySession struct {
	sessionID string
	token     string
	peerA     *net.UDPAddr // first peer to send a packet
	peerB     *net.UDPAddr // second peer
	created   time.Time
}

func NewRelayServer(listenAddr string, log *slog.Logger) *RelayServer {
	return &RelayServer{
		sessions: make(map[uint32]*relaySession),
		addr:     listenAddr,
		log:      log,
	}
}

// Addr returns the relay's listen address.
func (rs *RelayServer) Addr() string {
	return rs.addr
}

// RegisterSession activates relaying for a session with the given token.
func (rs *RelayServer) RegisterSession(token, sessionID string) {
	h := tokenHash(token)
	rs.mu.Lock()
	rs.sessions[h] = &relaySession{
		sessionID: sessionID,
		token:     token,
		created:   time.Now(),
	}
	rs.mu.Unlock()
	rs.log.Info("relay session registered", "session_id", sessionID[:8], "token_hash", h)
}

// RemoveSession deactivates relaying for a token.
func (rs *RelayServer) RemoveSession(token string) {
	h := tokenHash(token)
	rs.mu.Lock()
	delete(rs.sessions, h)
	rs.mu.Unlock()
}

// Run starts the relay UDP listener. Blocks until ctx is cancelled.
func (rs *RelayServer) Run(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", rs.addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	rs.conn = conn
	defer conn.Close()

	rs.log.Info("relay server listening", "addr", rs.addr)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65536) // max UDP payload
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				rs.log.Warn("relay read error", "error", err)
				continue
			}
		}

		// Packet format: magic (4) + token hash (4) + payload
		if n < 8 || string(buf[:4]) != string(relayMagic) {
			continue
		}

		tokHash := binary.BigEndian.Uint32(buf[4:8])
		payload := buf[8:n]

		rs.mu.Lock()
		sess, ok := rs.sessions[tokHash]
		if !ok {
			rs.mu.Unlock()
			continue
		}

		var target *net.UDPAddr
		switch {
		case sess.peerA == nil:
			sess.peerA = raddr
			rs.mu.Unlock()
			continue // no peer to forward to yet
		case sess.peerB == nil && raddr.String() != sess.peerA.String():
			sess.peerB = raddr
			target = sess.peerA
		case raddr.String() == sess.peerA.String():
			target = sess.peerB
		case raddr.String() == sess.peerB.String():
			target = sess.peerA
		default:
			rs.mu.Unlock()
			continue
		}
		rs.mu.Unlock()

		if target != nil {
			conn.WriteToUDP(payload, target)
		}
	}
}

// tokenHash computes a 4-byte hash of a relay token.
func tokenHash(token string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(token))
	return h.Sum32()
}

// TokenHashBytes returns the 4-byte big-endian token hash for use in packet headers.
func TokenHashBytes(token string) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, tokenHash(token))
	return b
}
