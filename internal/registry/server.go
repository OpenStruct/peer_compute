package registry

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/internal/relay"
	"github.com/OpenStruct/peer_compute/plugin"
)

const heartbeatTimeout = 30 * time.Second

// Server implements the RegistryService gRPC server.
// MVP: in-memory store. Swap to PostgreSQL + Redis for production.
type Server struct {
	computev1.UnimplementedRegistryServiceServer

	mu        sync.RWMutex
	providers map[string]*computev1.Provider
	sessions  map[string]*computev1.Session

	// NAT traversal state: sessionID -> peerID -> candidateSet
	candidates map[string]map[string]*candidateSet

	relay   *relay.RelayServer
	plugins *plugin.Bundle
}

type candidateSet struct {
	candidates  []*computev1.EndpointCandidate
	wgPublicKey string
}

// Option configures the server.
type Option func(*Server)

// WithPlugins sets custom plugin implementations (marketplace).
func WithPlugins(b *plugin.Bundle) Option {
	return func(s *Server) { s.plugins = b }
}

func NewServer(relayServer *relay.RelayServer, opts ...Option) *Server {
	s := &Server{
		providers:  make(map[string]*computev1.Provider),
		sessions:   make(map[string]*computev1.Session),
		candidates: make(map[string]map[string]*candidateSet),
		relay:      relayServer,
		plugins:    plugin.Defaults(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// resolveProvider finds a provider by full ID or unique prefix.
func (s *Server) resolveProvider(prefix string) (*computev1.Provider, error) {
	if p, ok := s.providers[prefix]; ok {
		return p, nil
	}
	var match *computev1.Provider
	for id, p := range s.providers {
		if strings.HasPrefix(id, prefix) {
			if match != nil {
				return nil, status.Error(codes.InvalidArgument, "ambiguous provider ID prefix")
			}
			match = p
		}
	}
	if match == nil {
		return nil, status.Error(codes.NotFound, "provider not found")
	}
	return match, nil
}

// resolveSession finds a session by full ID or unique prefix.
func (s *Server) resolveSession(prefix string) (*computev1.Session, error) {
	if sess, ok := s.sessions[prefix]; ok {
		return sess, nil
	}
	var match *computev1.Session
	for id, sess := range s.sessions {
		if strings.HasPrefix(id, prefix) {
			if match != nil {
				return nil, status.Error(codes.InvalidArgument, "ambiguous session ID prefix")
			}
			match = sess
		}
	}
	if match == nil {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	return match, nil
}

func (s *Server) RegisterProvider(_ context.Context, req *computev1.RegisterProviderRequest) (*computev1.RegisterProviderResponse, error) {
	id := uuid.NewString()
	now := timestamppb.Now()

	provider := &computev1.Provider{
		Id:            id,
		Name:          req.Name,
		Address:       req.Address,
		Capacity:      req.Capacity,
		Available:     req.Capacity,
		Status:        "online",
		LastHeartbeat: now,
		RegisteredAt:  now,
	}

	s.mu.Lock()
	s.providers[id] = provider
	s.mu.Unlock()

	return &computev1.RegisterProviderResponse{
		Provider: provider,
		Token:    "tok_" + id,
	}, nil
}

func (s *Server) Heartbeat(_ context.Context, req *computev1.HeartbeatRequest) (*computev1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.resolveProvider(req.ProviderId)
	if err != nil {
		return nil, err
	}

	p.LastHeartbeat = timestamppb.Now()
	p.Available = req.Available
	p.Status = "online"

	return &computev1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *Server) ListProviders(_ context.Context, req *computev1.ListProvidersRequest) (*computev1.ListProvidersResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stale := time.Now().Add(-heartbeatTimeout)
	var out []*computev1.Provider

	for _, p := range s.providers {
		if p.LastHeartbeat.AsTime().Before(stale) {
			p.Status = "offline"
		}
		if p.Status != "online" {
			continue
		}
		if req.MinCpu > 0 && p.Available.CpuCores < req.MinCpu {
			continue
		}
		if req.MinMemoryMb > 0 && p.Available.MemoryMb < req.MinMemoryMb {
			continue
		}
		if req.MinGpu > 0 && p.Available.GpuCount < req.MinGpu {
			continue
		}
		out = append(out, p)
	}

	return &computev1.ListProvidersResponse{Providers: out}, nil
}

func (s *Server) CreateSession(ctx context.Context, req *computev1.CreateSessionRequest) (*computev1.CreateSessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.resolveProvider(req.ProviderId)
	if err != nil {
		return nil, err
	}
	if p.Status != "online" {
		return nil, status.Error(codes.Unavailable, "provider is not online")
	}

	// Plugin: check provider reputation
	if ok, err := s.plugins.Reputation.MeetsMinimum(ctx, p.Id, 50); err != nil || !ok {
		return nil, status.Error(codes.Unavailable, "provider does not meet minimum reputation")
	}

	relayToken := uuid.NewString()

	session := &computev1.Session{
		Id:         uuid.NewString(),
		ProviderId: p.Id,
		RenterId:   req.RenterId,
		Allocated:  req.Requested,
		Image:      req.Image,
		Status:     "pending",
		CreatedAt:  timestamppb.Now(),
		RelayToken: relayToken,
	}

	s.sessions[session.Id] = session

	// Store renter's WG public key if provided
	if req.WgPublicKey != "" {
		if s.candidates[session.Id] == nil {
			s.candidates[session.Id] = make(map[string]*candidateSet)
		}
		s.candidates[session.Id][req.RenterId] = &candidateSet{
			wgPublicKey: req.WgPublicKey,
		}
	}

	return &computev1.CreateSessionResponse{Session: session}, nil
}

func (s *Server) GetSession(_ context.Context, req *computev1.GetSessionRequest) (*computev1.GetSessionResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, err := s.resolveSession(req.SessionId)
	if err != nil {
		return nil, err
	}

	return &computev1.GetSessionResponse{Session: session}, nil
}

func (s *Server) TerminateSession(ctx context.Context, req *computev1.TerminateSessionRequest) (*computev1.TerminateSessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.resolveSession(req.SessionId)
	if err != nil {
		return nil, err
	}

	session.Status = "terminated"
	session.TerminatedAt = timestamppb.Now()

	s.plugins.Reputation.RecordOutcome(ctx, session.Id, true)

	// Clean up relay and candidate state
	if session.RelayToken != "" && s.relay != nil {
		s.relay.RemoveSession(session.RelayToken)
	}
	delete(s.candidates, session.Id)

	return &computev1.TerminateSessionResponse{Success: true}, nil
}

func (s *Server) ListProviderSessions(_ context.Context, req *computev1.ListProviderSessionsRequest) (*computev1.ListProviderSessionsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*computev1.Session
	for _, sess := range s.sessions {
		if sess.ProviderId != req.ProviderId {
			continue
		}
		if req.StatusFilter != "" && sess.Status != req.StatusFilter {
			continue
		}
		out = append(out, sess)
	}

	return &computev1.ListProviderSessionsResponse{Sessions: out}, nil
}

func (s *Server) UpdateSessionStatus(ctx context.Context, req *computev1.UpdateSessionStatusRequest) (*computev1.UpdateSessionStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.resolveSession(req.SessionId)
	if err != nil {
		return nil, err
	}

	prevStatus := session.Status
	session.Status = req.Status

	if req.Status == "terminated" && prevStatus != "terminated" {
		s.plugins.Reputation.RecordOutcome(ctx, session.Id, true)
	}
	if req.ContainerId != "" {
		session.ContainerId = req.ContainerId
	}
	if req.SshEndpoint != "" {
		session.SshEndpoint = req.SshEndpoint
	}
	if req.WgPublicKey != "" {
		session.WgPublicKey = req.WgPublicKey
	}
	if req.WgEndpoint != "" {
		session.WgEndpoint = req.WgEndpoint
	}
	if req.Status == "terminated" {
		session.TerminatedAt = timestamppb.Now()
	}

	return &computev1.UpdateSessionStatusResponse{Success: true}, nil
}

// ── NAT Traversal RPCs ─────────────────────────────────────────────

func (s *Server) ExchangeCandidates(_ context.Context, req *computev1.ExchangeCandidatesRequest) (*computev1.ExchangeCandidatesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.resolveSession(req.SessionId)
	if err != nil {
		return nil, err
	}

	// Store this peer's candidates
	if s.candidates[session.Id] == nil {
		s.candidates[session.Id] = make(map[string]*candidateSet)
	}
	s.candidates[session.Id][req.PeerId] = &candidateSet{
		candidates:  req.Candidates,
		wgPublicKey: req.WgPublicKey,
	}

	// Look for the other peer's candidates
	resp := &computev1.ExchangeCandidatesResponse{}
	if s.relay != nil {
		resp.RelayAddress = s.relay.Addr()
	}
	resp.RelayToken = session.RelayToken

	for peerID, cs := range s.candidates[session.Id] {
		if peerID != req.PeerId {
			resp.PeerCandidates = cs.candidates
			resp.PeerWgPublicKey = cs.wgPublicKey
			break
		}
	}

	return resp, nil
}

func (s *Server) ReportConnectionResult(_ context.Context, req *computev1.ReportConnectionResultRequest) (*computev1.ReportConnectionResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.resolveSession(req.SessionId)
	if err != nil {
		return nil, err
	}

	if req.UseRelay {
		session.ConnectionMode = "relay"
		if s.relay != nil {
			s.relay.RegisterSession(session.RelayToken, session.Id)
		}
	} else {
		session.ConnectionMode = "holepunch"
	}

	return &computev1.ReportConnectionResultResponse{Acknowledged: true}, nil
}
