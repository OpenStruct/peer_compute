package agent

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"golang.org/x/crypto/curve25519"
)

// WGKeyPair holds a WireGuard Curve25519 key pair.
type WGKeyPair struct {
	PrivateKey string // base64-encoded
	PublicKey  string // base64-encoded
}

// GenerateKeyPair creates a new WireGuard Curve25519 key pair.
func GenerateKeyPair() (*WGKeyPair, error) {
	var privKey [32]byte
	if _, err := rand.Read(privKey[:]); err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}

	// Clamp per WireGuard / Curve25519 spec
	privKey[0] &= 248
	privKey[31] &= 127
	privKey[31] |= 64

	pubKey, err := curve25519.X25519(privKey[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	return &WGKeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(privKey[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pubKey),
	}, nil
}

const wgConfTmpl = `[Interface]
PrivateKey = {{.PrivateKey}}
Address = {{.TunnelIP}}/24
ListenPort = {{.ListenPort}}

[Peer]
PublicKey = {{.PeerPublicKey}}
AllowedIPs = {{.PeerIP}}/32
{{- if .PeerEndpoint}}
Endpoint = {{.PeerEndpoint}}
{{- end}}
PersistentKeepalive = 25
`

// WGConfig holds the parameters for generating a wg-quick config file.
type WGConfig struct {
	PrivateKey    string
	TunnelIP      string // e.g. 10.99.0.1
	ListenPort    int
	PeerPublicKey string
	PeerIP        string // e.g. 10.99.0.2
	PeerEndpoint  string // e.g. 1.2.3.4:51820, or 127.0.0.1:proxyPort for relay
}

// WriteConfig writes a wg-quick compatible config file for a session.
func WriteConfig(sessionID string, cfg WGConfig) (string, error) {
	dir := filepath.Join(os.TempDir(), "peer-compute", "wg")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}

	path := filepath.Join(dir, fmt.Sprintf("pc-%s.conf", sessionID[:12]))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()

	tmpl := template.Must(template.New("wg").Parse(wgConfTmpl))
	if err := tmpl.Execute(f, cfg); err != nil {
		return "", err
	}

	return path, nil
}

// RemoveConfig cleans up the WireGuard config file for a session.
func RemoveConfig(sessionID string) error {
	path := filepath.Join(os.TempDir(), "peer-compute", "wg", fmt.Sprintf("pc-%s.conf", sessionID[:12]))
	return os.Remove(path)
}
