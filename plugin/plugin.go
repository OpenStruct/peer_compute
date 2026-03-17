// Package plugin defines the extension points for Peer Compute.
//
// The open-source version ships with no-op/basic implementations.
// Custom implementations can be provided via the plugin.Bundle.
package plugin

import "context"

// Authenticator validates and identifies users.
type Authenticator interface {
	// Authenticate validates a token and returns the user ID.
	Authenticate(ctx context.Context, token string) (userID string, err error)

	// Issue creates a new token for a user.
	Issue(ctx context.Context, userID string) (token string, err error)
}

// Reputation scores providers and renters.
type Reputation interface {
	// ProviderScore returns a trust score (0-100) for a provider.
	ProviderScore(ctx context.Context, providerID string) (int, error)

	// RenterScore returns a trust score (0-100) for a renter.
	RenterScore(ctx context.Context, renterID string) (int, error)

	// RecordOutcome logs whether a session completed successfully.
	RecordOutcome(ctx context.Context, sessionID string, success bool) error

	// MeetsMinimum checks if a provider meets the minimum score to accept sessions.
	MeetsMinimum(ctx context.Context, providerID string, minScore int) (bool, error)
}

// Store provides persistent storage for providers, sessions, and state.
type Store interface {
	ProviderStore
	SessionStore
	Close() error
}

// ProviderStore manages provider records.
type ProviderStore interface {
	PutProvider(ctx context.Context, p *ProviderRecord) error
	GetProvider(ctx context.Context, id string) (*ProviderRecord, error)
	GetProviderByPrefix(ctx context.Context, prefix string) (*ProviderRecord, error)
	ListProviders(ctx context.Context, filter ProviderFilter) ([]*ProviderRecord, error)
	DeleteProvider(ctx context.Context, id string) error
}

// SessionStore manages session records.
type SessionStore interface {
	PutSession(ctx context.Context, s *SessionRecord) error
	GetSession(ctx context.Context, id string) (*SessionRecord, error)
	GetSessionByPrefix(ctx context.Context, prefix string) (*SessionRecord, error)
	ListSessions(ctx context.Context, filter SessionFilter) ([]*SessionRecord, error)
	DeleteSession(ctx context.Context, id string) error
}

// ProviderRecord is the storage-layer representation of a provider.
type ProviderRecord struct {
	ID            string
	Name          string
	Address       string
	PublicAddress string
	Status        string
	CPUCores      uint32
	MemoryMB      uint64
	DiskGB        uint64
	GPUCount      uint32
	GPUModel      string
	AvailCPU      uint32
	AvailMemoryMB uint64
	AvailGPU      uint32
	RegisteredAt  int64 // unix timestamp
	LastHeartbeat int64
}

// ProviderFilter for listing providers.
type ProviderFilter struct {
	MinCPU    uint32
	MinMemory uint64
	MinGPU    uint32
	Status    string
}

// SessionRecord is the storage-layer representation of a session.
type SessionRecord struct {
	ID             string
	ProviderID     string
	RenterID       string
	Image          string
	Status         string
	ContainerID    string
	SSHEndpoint    string
	ConnectionMode string
	RelayToken     string
	WGPublicKey    string
	WGEndpoint     string
	WGProviderIP   string // per-session tunnel IP for provider (e.g. "10.99.1.1")
	WGRenterIP     string // per-session tunnel IP for renter (e.g. "10.99.1.2")
	CPUCores       uint32
	MemoryMB       uint64
	CreatedAt      int64
	TerminatedAt   int64
}

// SessionFilter for listing sessions.
type SessionFilter struct {
	ProviderID string
	RenterID   string
	Status     string
}
