package agent

import "context"

// TokenCredentials implements grpc.PerRPCCredentials to inject an API key
// into the "authorization" metadata header on every gRPC call.
type TokenCredentials struct {
	Token string
}

// GetRequestMetadata returns the authorization header.
func (t *TokenCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": t.Token}, nil
}

// RequireTransportSecurity returns false since TLS is not yet enforced.
func (t *TokenCredentials) RequireTransportSecurity() bool {
	return false
}
