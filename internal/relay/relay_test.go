package relay

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTokenHashBytes_Deterministic(t *testing.T) {
	h1 := TokenHashBytes("my-token")
	h2 := TokenHashBytes("my-token")
	if !bytes.Equal(h1, h2) {
		t.Errorf("hashes differ for same token: %x vs %x", h1, h2)
	}
}

func TestTokenHashBytes_DifferentTokens(t *testing.T) {
	h1 := TokenHashBytes("token-a")
	h2 := TokenHashBytes("token-b")
	if bytes.Equal(h1, h2) {
		t.Errorf("hashes should differ for different tokens: both %x", h1)
	}
}

func TestTokenHashBytes_Length(t *testing.T) {
	h := TokenHashBytes("any-token")
	if len(h) != 4 {
		t.Errorf("hash length = %d, want 4", len(h))
	}
}

func TestRelayServer_RegisterAndRemoveSession(t *testing.T) {
	rs := NewRelayServer(":0", discardLogger())
	// sessionID must be at least 8 chars long since RegisterSession does sessionID[:8]
	rs.RegisterSession("my-relay-token", "session-01234567")
	rs.RemoveSession("my-relay-token")
}

func TestRelayServer_Addr(t *testing.T) {
	addr := "127.0.0.1:9999"
	rs := NewRelayServer(addr, discardLogger())
	if got := rs.Addr(); got != addr {
		t.Errorf("Addr() = %q, want %q", got, addr)
	}
}
