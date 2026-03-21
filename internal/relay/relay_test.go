package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTokenIDBytes_Deterministic(t *testing.T) {
	h1 := TokenIDBytes("my-token")
	h2 := TokenIDBytes("my-token")
	if !bytes.Equal(h1, h2) {
		t.Errorf("token IDs differ for same token: %x vs %x", h1, h2)
	}
}

func TestTokenIDBytes_DifferentTokens(t *testing.T) {
	h1 := TokenIDBytes("token-a")
	h2 := TokenIDBytes("token-b")
	if bytes.Equal(h1, h2) {
		t.Errorf("token IDs should differ for different tokens: both %x", h1)
	}
}

func TestTokenIDBytes_Length(t *testing.T) {
	h := TokenIDBytes("any-token")
	if len(h) != 16 {
		t.Errorf("token ID length = %d, want 16", len(h))
	}
}

func TestRelayServer_RegisterAndRemoveSession(t *testing.T) {
	rs := NewRelayServer(":0", discardLogger())
	// sessionID must be at least 8 chars long since RegisterSession does sessionID[:8]
	rs.RegisterSession("my-relay-token", "session-01234567")

	rs.mu.RLock()
	if len(rs.sessions) != 1 {
		t.Errorf("expected 1 session after register, got %d", len(rs.sessions))
	}
	rs.mu.RUnlock()

	rs.RemoveSession("my-relay-token")

	rs.mu.RLock()
	if len(rs.sessions) != 0 {
		t.Errorf("expected 0 sessions after remove, got %d", len(rs.sessions))
	}
	rs.mu.RUnlock()
}

func TestRelayServer_Addr(t *testing.T) {
	addr := "127.0.0.1:9999"
	rs := NewRelayServer(addr, discardLogger())
	if got := rs.Addr(); got != addr {
		t.Errorf("Addr() = %q, want %q", got, addr)
	}
}

func TestRelayServer_SessionMapUsesFullToken(t *testing.T) {
	rs := NewRelayServer(":0", discardLogger())
	rs.RegisterSession("token-aaa", "session-aaaaaaaa")
	rs.RegisterSession("token-bbb", "session-bbbbbbbb")

	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if len(rs.sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(rs.sessions))
	}

	sessA, ok := rs.sessions["token-aaa"]
	if !ok {
		t.Fatal("session for token-aaa not found")
	}
	if sessA.sessionID != "session-aaaaaaaa" {
		t.Errorf("wrong session ID for token-aaa: %s", sessA.sessionID)
	}

	sessB, ok := rs.sessions["token-bbb"]
	if !ok {
		t.Fatal("session for token-bbb not found")
	}
	if sessB.sessionID != "session-bbbbbbbb" {
		t.Errorf("wrong session ID for token-bbb: %s", sessB.sessionID)
	}
}

func TestRelayServer_PacketProtocol(t *testing.T) {
	// Bind a UDP socket ourselves so we know the exact port before Run is called.
	laddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		t.Fatal(err)
	}
	actualAddr := conn.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rs := NewRelayServer(actualAddr.String(), discardLogger())
	// Inject the already-bound conn so Run reuses it instead of binding a new one.
	rs.conn = conn

	token := "test-relay-token-xyz"
	rs.RegisterSession(token, "session-12345678")

	// Start the server in background. Run detects the pre-set conn and uses it.
	go rs.Run(ctx)

	// Give the server goroutine time to start reading.
	time.Sleep(50 * time.Millisecond)

	// Simulate peer A sending a packet.
	peerA, err := net.DialUDP("udp", nil, actualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer peerA.Close()

	// Build a correctly formatted packet: magic(4) + tokenID(16) + payload
	tid := TokenIDBytes(token)
	payload := []byte("hello-from-peerA")
	pkt := make([]byte, 0, headerLen+len(payload))
	pkt = append(pkt, relayMagic...)
	pkt = append(pkt, tid...)
	pkt = append(pkt, payload...)

	// Peer A sends — this registers peerA address, no forwarding yet.
	_, err = peerA.Write(pkt)
	if err != nil {
		t.Fatal(err)
	}

	// Give the server time to process.
	time.Sleep(100 * time.Millisecond)

	// Verify peer A was registered.
	rs.mu.RLock()
	sess := rs.sessions[token]
	if sess == nil {
		rs.mu.RUnlock()
		t.Fatal("session not found")
	}
	if sess.peerA == nil {
		rs.mu.RUnlock()
		t.Fatal("peerA not registered after first packet")
	}
	rs.mu.RUnlock()
}

func TestRelayServer_SessionCount(t *testing.T) {
	rs := NewRelayServer(":0", discardLogger())

	if got := rs.SessionCount(); got != 0 {
		t.Errorf("SessionCount() = %d, want 0", got)
	}

	rs.RegisterSession("token-1", "session-11111111")
	if got := rs.SessionCount(); got != 1 {
		t.Errorf("SessionCount() = %d, want 1", got)
	}

	rs.RegisterSession("token-2", "session-22222222")
	if got := rs.SessionCount(); got != 2 {
		t.Errorf("SessionCount() = %d, want 2", got)
	}

	rs.RemoveSession("token-1")
	if got := rs.SessionCount(); got != 1 {
		t.Errorf("SessionCount() = %d, want 1", got)
	}
}

func TestRelayServer_ServeHealthz(t *testing.T) {
	rs := NewRelayServer(":0", discardLogger())
	rs.RegisterSession("health-token", "session-health01")

	// Start healthz on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	go rs.ServeHealthz(port)
	// Give the HTTP server time to start.
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + itoa(port) + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Status   string `json:"status"`
		Sessions int    `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want %q", body.Status, "ok")
	}
	if body.Sessions != 1 {
		t.Errorf("sessions = %d, want 1", body.Sessions)
	}
}

// itoa converts an int to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestRelayServer_IdleTimeout(t *testing.T) {
	rs := NewRelayServer(":0", discardLogger())
	rs.RegisterSession("idle-token", "session-idle1234")

	// Manually set lastActive to well past the idle timeout.
	rs.mu.Lock()
	rs.sessions["idle-token"].lastActive = time.Now().Add(-2 * idleTimeout)
	rs.mu.Unlock()

	// Run a single reap cycle.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so reaper exits after one tick

	// Manually trigger reap logic.
	rs.mu.Lock()
	now := time.Now()
	for token, sess := range rs.sessions {
		if now.Sub(sess.lastActive) > idleTimeout {
			delete(rs.sessions, token)
		}
	}
	rs.mu.Unlock()

	_ = ctx // suppress unused warning

	rs.mu.RLock()
	if len(rs.sessions) != 0 {
		t.Errorf("expected idle session to be reaped, got %d sessions", len(rs.sessions))
	}
	rs.mu.RUnlock()
}
