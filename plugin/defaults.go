package plugin

import "context"

// ── Default implementations (open-source / self-hosted) ────────────

// OpenAuth accepts all tokens and returns a fixed user ID.
type OpenAuth struct{}

func (OpenAuth) Authenticate(_ context.Context, token string) (string, error) {
	if token == "" {
		return "anonymous", nil
	}
	return token, nil
}

func (OpenAuth) Issue(_ context.Context, userID string) (string, error) {
	return "tok_" + userID, nil
}

// OpenReputation trusts all providers and renters equally.
type OpenReputation struct{}

func (OpenReputation) ProviderScore(_ context.Context, _ string) (int, error) {
	return 100, nil
}

func (OpenReputation) RenterScore(_ context.Context, _ string) (int, error) {
	return 100, nil
}

func (OpenReputation) RecordOutcome(_ context.Context, _ string, _ bool) error {
	return nil
}

func (OpenReputation) MeetsMinimum(_ context.Context, _ string, _ int) (bool, error) {
	return true, nil
}

// Defaults returns a set of default plugin implementations for the
// open-source / self-hosted version.
func Defaults() *Bundle {
	return &Bundle{
		Auth:       OpenAuth{},
		Reputation: OpenReputation{},
	}
}

// Bundle holds all plugin implementations together.
type Bundle struct {
	Auth       Authenticator
	Reputation Reputation
}
