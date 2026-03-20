package agent

import (
	"context"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"google.golang.org/grpc"
)

// RegistryClient is the subset of registry RPCs the agent daemon uses.
// It is satisfied by both the generated gRPC client and the REST client.
type RegistryClient interface {
	RegisterProvider(ctx context.Context, req *computev1.RegisterProviderRequest) (*computev1.RegisterProviderResponse, error)
	Heartbeat(ctx context.Context, req *computev1.HeartbeatRequest) (*computev1.HeartbeatResponse, error)
	ListProviderSessions(ctx context.Context, req *computev1.ListProviderSessionsRequest) (*computev1.ListProviderSessionsResponse, error)
	UpdateSessionStatus(ctx context.Context, req *computev1.UpdateSessionStatusRequest) (*computev1.UpdateSessionStatusResponse, error)
	ExchangeCandidates(ctx context.Context, req *computev1.ExchangeCandidatesRequest) (*computev1.ExchangeCandidatesResponse, error)
	ReportConnectionResult(ctx context.Context, req *computev1.ReportConnectionResultRequest) (*computev1.ReportConnectionResultResponse, error)
}

// NewGRPCClient wraps the generated gRPC RegistryServiceClient as a RegistryClient.
func NewGRPCClient(conn *grpc.ClientConn) RegistryClient {
	return &grpcAdapter{client: computev1.NewRegistryServiceClient(conn)}
}

type grpcAdapter struct {
	client computev1.RegistryServiceClient
}

func (g *grpcAdapter) RegisterProvider(ctx context.Context, req *computev1.RegisterProviderRequest) (*computev1.RegisterProviderResponse, error) {
	return g.client.RegisterProvider(ctx, req)
}

func (g *grpcAdapter) Heartbeat(ctx context.Context, req *computev1.HeartbeatRequest) (*computev1.HeartbeatResponse, error) {
	return g.client.Heartbeat(ctx, req)
}

func (g *grpcAdapter) ListProviderSessions(ctx context.Context, req *computev1.ListProviderSessionsRequest) (*computev1.ListProviderSessionsResponse, error) {
	return g.client.ListProviderSessions(ctx, req)
}

func (g *grpcAdapter) UpdateSessionStatus(ctx context.Context, req *computev1.UpdateSessionStatusRequest) (*computev1.UpdateSessionStatusResponse, error) {
	return g.client.UpdateSessionStatus(ctx, req)
}

func (g *grpcAdapter) ExchangeCandidates(ctx context.Context, req *computev1.ExchangeCandidatesRequest) (*computev1.ExchangeCandidatesResponse, error) {
	return g.client.ExchangeCandidates(ctx, req)
}

func (g *grpcAdapter) ReportConnectionResult(ctx context.Context, req *computev1.ReportConnectionResultRequest) (*computev1.ReportConnectionResultResponse, error) {
	return g.client.ReportConnectionResult(ctx, req)
}
