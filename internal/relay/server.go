package relay

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net"
	"sync"
	"time"
)

var relayMagic = []byte("PCRL") // Peer Compute ReLay

const (
	// tokenIDLen is the fixed length of the token identifier in the packet header.
	// We use the first 16 bytes of SHA-256(token), which is sufficient to avoid
	// collisions (birthday bound ~2^64 sessions) while keeping headers compact.
	tokenIDLen = 16

	// magicLen is the length of the relay magic bytes ("PCRL").
	magicLen = 4

	// headerLen is magic (4) + tokenID (16) = 20 bytes.
	headerLen = magicLen + tokenIDLen

	// idleTimeout is how long a relay session can go without forwarding a packet
	// before it is automatically deallocated.
	idleTimeout = 60 * time.Second
)

// RelayServer proxies encrypted UDP packets between two peers that
// cannot establish a direct connection (symmetric NAT). Both peers
// send WireGuard UDP traffic here; the server forwards it to the other side.
type RelayServer struct {
	mu       sync.RWMutex
	sessions map[string]*relaySession // full token -> session
	conn     *net.UDPConn
	addr     string
	log      *slog.Logger
}

type relaySession struct {
	sessionID string
	token     string
	tokenID   [tokenIDLen]byte // first 16 bytes of SHA-256(token)
	peerA     *net.UDPAddr     // first peer to send a packet
	peerB     *net.UDPAddr     // second peer
	created   time.Time
	lastActive time.Time
}

func NewRelayServer(listenAddr string, log *slog.Logger) *RelayServer {
	return &RelayServer{
		sessions: make(map[string]*relaySession),
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
	now := time.Now()
	rs.mu.Lock()
	rs.sessions[token] = &relaySession{
		sessionID:  sessionID,
		token:      token,
		tokenID:    tokenID(token),
		created:    now,
		lastActive: now,
	}
	rs.mu.Unlock()
	rs.log.Info("relay session registered", "session_id", sessionID[:8])
}

// RemoveSession deactivates relaying for a token.
func (rs *RelayServer) RemoveSession(token string) {
	rs.mu.Lock()
	delete(rs.sessions, token)
	rs.mu.Unlock()
}

// Run starts the relay UDP listener. Blocks until ctx is cancelled.
func (rs *RelayServer) Run(ctx context.Context) error {
	// If a connection was already injected (e.g. by tests), use it directly.
	if rs.conn != nil {
		return rs.serve(ctx, rs.conn)
	}

	addr, err := net.ResolveUDPAddr("udp", rs.addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	rs.conn = conn

	return rs.serve(ctx, conn)
}

// serve runs the main packet-forwarding loop on the given connection.
// It blocks until ctx is cancelled.
func (rs *RelayServer) serve(ctx context.Context, conn *net.UDPConn) error {
	defer conn.Close()

	rs.log.Info("relay server listening", "addr", rs.addr)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// Start idle session reaper.
	go rs.reapIdleSessions(ctx)

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

		// Packet format: magic (4) + tokenID (16) + payload
		if n < headerLen || string(buf[:4]) != string(relayMagic) {
			continue
		}

		var pktTokenID [tokenIDLen]byte
		copy(pktTokenID[:], buf[4:4+tokenIDLen])
		payload := buf[headerLen:n]

		// Fast path: read-lock to find session and forward.
		rs.mu.RLock()
		sess := rs.findSessionByTokenID(pktTokenID)
		if sess == nil {
			rs.mu.RUnlock()
			continue
		}

		// Determine if we need to assign a peer (write path) or just forward (read path).
		needsWrite := sess.peerA == nil ||
			(sess.peerB == nil && raddr.String() != sess.peerA.String())

		if !needsWrite {
			// Pure forwarding — hot path under RLock only.
			var target *net.UDPAddr
			switch {
			case raddr.String() == sess.peerA.String():
				target = sess.peerB
			case raddr.String() == sess.peerB.String():
				target = sess.peerA
			}
			sess.lastActive = time.Now()
			rs.mu.RUnlock()

			if target != nil {
				conn.WriteToUDP(payload, target)
			}
			continue
		}
		rs.mu.RUnlock()

		// Slow path: need to assign peer — acquire write lock.
		rs.mu.Lock()
		// Re-lookup under write lock in case session was removed.
		sess = rs.findSessionByTokenID(pktTokenID)
		if sess == nil {
			rs.mu.Unlock()
			continue
		}

		var target *net.UDPAddr
		switch {
		case sess.peerA == nil:
			sess.peerA = raddr
			sess.lastActive = time.Now()
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
		sess.lastActive = time.Now()
		rs.mu.Unlock()

		if target != nil {
			conn.WriteToUDP(payload, target)
		}
	}
}

// findSessionByTokenID locates a session by its 16-byte token ID.
// Caller must hold at least rs.mu.RLock().
func (rs *RelayServer) findSessionByTokenID(id [tokenIDLen]byte) *relaySession {
	for _, sess := range rs.sessions {
		if sess.tokenID == id {
			return sess
		}
	}
	return nil
}

// reapIdleSessions periodically removes sessions that have been idle for longer
// than idleTimeout. Runs until ctx is cancelled.
func (rs *RelayServer) reapIdleSessions(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.mu.Lock()
			now := time.Now()
			for token, sess := range rs.sessions {
				if now.Sub(sess.lastActive) > idleTimeout {
					rs.log.Info("reaping idle relay session",
						"session_id", sess.sessionID[:8],
						"idle", now.Sub(sess.lastActive).Round(time.Second))
					delete(rs.sessions, token)
				}
			}
			rs.mu.Unlock()
		}
	}
}

// tokenID computes a 16-byte identifier from a relay token using SHA-256.
func tokenID(token string) [tokenIDLen]byte {
	h := sha256.Sum256([]byte(token))
	var id [tokenIDLen]byte
	copy(id[:], h[:tokenIDLen])
	return id
}

// TokenIDBytes returns the 16-byte token identifier for use in packet headers.
func TokenIDBytes(token string) []byte {
	id := tokenID(token)
	return id[:]
}
