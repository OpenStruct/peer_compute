package relay

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// RawBasePort is the first UDP port in the raw-WireGuard relay range.
	// Each session gets a deterministic port in [RawBasePort, RawBasePort+RawPortRange).
	// These ports must be reachable by renters (open in firewall / exposed in docker-compose).
	RawBasePort  = 44000
	RawPortRange = 1000
)

// RawPortForToken returns the deterministic raw UDP port for the given relay
// token. The same formula is used by the relay server (to bind) and by the
// pro API (to build the WireGuard Endpoint line), so they always agree without
// any extra RPC.
func RawPortForToken(token string) int {
	h := sha256.Sum256([]byte(token))
	offset := int(binary.BigEndian.Uint32(h[:4]) % uint32(RawPortRange))
	return RawBasePort + offset
}

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
	mu         sync.RWMutex
	sessions   map[string]*relaySession // full token -> session
	conn       *net.UDPConn
	addr       string
	publicAddr string // public address advertised to agents; overrides addr when set
	log        *slog.Logger
}

type relaySession struct {
	sessionID  string
	token      string
	tokenID    [tokenIDLen]byte // first 16 bytes of SHA-256(token)
	peerA      *net.UDPAddr     // provider relay client (on main conn)
	peerB      *net.UDPAddr     // legacy: second relay client (pcp-renter)
	rawConn    *net.UDPConn     // per-session raw UDP listener for plain WireGuard renters
	renterAddr *net.UDPAddr     // renter's address on rawConn (set on first raw packet)
	created    time.Time
	lastActive time.Time
}

func NewRelayServer(listenAddr string, log *slog.Logger) *RelayServer {
	return &RelayServer{
		sessions: make(map[string]*relaySession),
		addr:     listenAddr,
		log:      log,
	}
}

// Addr returns the address advertised to agents. When SetPublicAddr has been
// called (e.g. to expose the Docker-mapped port), that value is returned;
// otherwise the bind address is returned.
func (rs *RelayServer) Addr() string {
	if rs.publicAddr != "" {
		return rs.publicAddr
	}
	return rs.addr
}

// SetPublicAddr sets the public address advertised to agents. Call this when
// the relay server is behind a NAT or Docker port mapping so that agents
// receive the externally-reachable address instead of the internal bind address.
// Example: "compute.example.com:42024"
func (rs *RelayServer) SetPublicAddr(addr string) {
	rs.publicAddr = addr
}

// RegisterSession activates relaying for a session with the given token.
// It also binds a per-session raw UDP port so plain WireGuard clients (renters
// using the downloaded .conf file) can connect without the pcp-renter binary.
// The raw port is deterministic — use RawPortForToken(token) to obtain it.
func (rs *RelayServer) RegisterSession(token, sessionID string) {
	now := time.Now()
	sess := &relaySession{
		sessionID:  sessionID,
		token:      token,
		tokenID:    tokenID(token),
		created:    now,
		lastActive: now,
	}

	// Bind the raw WireGuard port for this session.
	rawPort := RawPortForToken(token)
	rawConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: rawPort})
	if err != nil {
		rs.log.Warn("raw relay port unavailable, WireGuard .conf endpoint will be empty",
			"session_id", sessionID[:8], "port", rawPort, "error", err)
	} else {
		sess.rawConn = rawConn
		rs.log.Info("relay session registered",
			"session_id", sessionID[:8], "raw_port", rawPort)
		go rs.serveRaw(rawConn, sess)
	}

	rs.mu.Lock()
	rs.sessions[token] = sess
	rs.mu.Unlock()

	if err != nil {
		rs.log.Info("relay session registered (no raw port)", "session_id", sessionID[:8])
	}
}

// serveRaw handles raw WireGuard UDP packets from renters on the per-session
// port. No PCRL header is expected — the port itself identifies the session.
// Inbound packets are wrapped with the PCRL header and forwarded to the
// provider's relay client (peerA) on the main relay conn.
// Outbound packets from peerA are forwarded back to renterAddr (set below in
// the main serve loop).
func (rs *RelayServer) serveRaw(conn *net.UDPConn, sess *relaySession) {
	defer conn.Close()
	buf := make([]byte, 65536)
	hdr := make([]byte, headerLen)
	copy(hdr[:4], relayMagic)
	copy(hdr[4:], sess.tokenID[:])

	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // conn closed on session removal
		}
		sess.lastActive = time.Now()

		// Record the renter's address so the main loop can forward replies back.
		rs.mu.Lock()
		sess.renterAddr = raddr
		rs.mu.Unlock()

		// Forward to provider relay client with PCRL header.
		rs.mu.RLock()
		peerA := sess.peerA
		rs.mu.RUnlock()
		if peerA == nil {
			continue // provider hasn't connected yet; drop and wait
		}
		pkt := make([]byte, headerLen+n)
		copy(pkt, hdr)
		copy(pkt[headerLen:], buf[:n])
		rs.conn.WriteToUDP(pkt, peerA) //nolint:errcheck
	}
}

// RemoveSession deactivates relaying for a token.
func (rs *RelayServer) RemoveSession(token string) {
	rs.mu.Lock()
	sess := rs.sessions[token]
	delete(rs.sessions, token)
	rs.mu.Unlock()
	if sess != nil && sess.rawConn != nil {
		sess.rawConn.Close()
	}
}

// InvalidateSession immediately terminates a relay session, refusing to
// forward any further packets. This is called when a compute session is
// terminated to ensure the relay stops forwarding traffic before the
// 60-second idle reaper fires.
func (rs *RelayServer) InvalidateSession(token string) {
	rs.mu.Lock()
	sess, ok := rs.sessions[token]
	delete(rs.sessions, token)
	rs.mu.Unlock()

	if ok {
		if sess.rawConn != nil {
			sess.rawConn.Close()
		}
		rs.log.Info("relay session invalidated",
			"session_id", sess.sessionID[:min(8, len(sess.sessionID))])
	}
}

// SessionCount returns the current number of active relay sessions.
func (rs *RelayServer) SessionCount() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.sessions)
}

// ServeHealthz starts an HTTP health check endpoint on the given port.
// The /healthz endpoint returns the current session count as JSON.
func (rs *RelayServer) ServeHealthz(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","sessions":%d}`, rs.SessionCount())
	})
	return http.ListenAndServe(fmt.Sprintf(":%d", port), mux)
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
		// needsWrite is false once peerA is set AND at least one return path exists
		// (peerB for the legacy relay client, or renterAddr for the raw WireGuard path).
		needsWrite := sess.peerA == nil ||
			(sess.peerB == nil && sess.renterAddr == nil && raddr.String() != sess.peerA.String())

		if !needsWrite {
			// Pure forwarding — hot path under RLock only.
			var target *net.UDPAddr
			var rawFwd *net.UDPConn  // non-nil when forwarding via per-session raw port
			var rawDst *net.UDPAddr
			switch {
			case raddr.String() == sess.peerA.String():
				// Provider → renter: prefer raw path (plain WireGuard) if available.
				if sess.rawConn != nil && sess.renterAddr != nil {
					rawFwd = sess.rawConn
					rawDst = sess.renterAddr
				} else {
					target = sess.peerB
				}
			case sess.peerB != nil && raddr.String() == sess.peerB.String():
				target = sess.peerA
			}
			sess.lastActive = time.Now()
			rs.mu.RUnlock()

			if rawFwd != nil {
				rawFwd.WriteToUDP(payload, rawDst) //nolint:errcheck
			} else if target != nil {
				conn.WriteToUDP(payload, target) //nolint:errcheck
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
		var rawFwd *net.UDPConn // non-nil when forwarding via per-session raw port
		var rawDst *net.UDPAddr
		switch {
		case sess.peerA == nil:
			sess.peerA = raddr
			sess.lastActive = time.Now()
			rs.mu.Unlock()
			continue // no peer to forward to yet
		case sess.peerB == nil && sess.renterAddr == nil && raddr.String() != sess.peerA.String():
			sess.peerB = raddr
			target = sess.peerA
		case raddr.String() == sess.peerA.String():
			// Provider → renter: prefer raw path (plain WireGuard) if available.
			if sess.rawConn != nil && sess.renterAddr != nil {
				rawFwd = sess.rawConn
				rawDst = sess.renterAddr
			} else {
				target = sess.peerB
			}
		case sess.peerB != nil && raddr.String() == sess.peerB.String():
			target = sess.peerA
		default:
			rs.mu.Unlock()
			continue
		}
		sess.lastActive = time.Now()
		rs.mu.Unlock()

		if rawFwd != nil {
			rawFwd.WriteToUDP(payload, rawDst) //nolint:errcheck
		} else if target != nil {
			conn.WriteToUDP(payload, target) //nolint:errcheck
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
					if sess.rawConn != nil {
						sess.rawConn.Close()
					}
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
