package registry

import (
	"context"
	"sync"
	"time"

	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "peer-compute/gen/compute/v1"
)

const heartbeatTimeout = 30 * time.Second

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

// Server implements the RegistryService gRPC server.
// MVP: in-memory store. Swap to PostgreSQL + Redis for production.
type Server struct {
	computev1.UnimplementedRegistryServiceServer

	mu        sync.RWMutex
	providers map[string]*computev1.Provider
	sessions  map[string]*computev1.Session
}

func NewServer() *Server {
	return &Server{
		providers: make(map[string]*computev1.Provider),
		sessions:  make(map[string]*computev1.Session),
	}
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

func (s *Server) CreateSession(_ context.Context, req *computev1.CreateSessionRequest) (*computev1.CreateSessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.resolveProvider(req.ProviderId)
	if err != nil {
		return nil, err
	}
	if p.Status != "online" {
		return nil, status.Error(codes.Unavailable, "provider is not online")
	}

	session := &computev1.Session{
		Id:         uuid.NewString(),
		ProviderId: p.Id,
		RenterId:   req.RenterId,
		Allocated:  req.Requested,
		Image:      req.Image,
		Status:     "pending",
		CreatedAt:  timestamppb.Now(),
	}

	s.sessions[session.Id] = session

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

func (s *Server) TerminateSession(_ context.Context, req *computev1.TerminateSessionRequest) (*computev1.TerminateSessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.resolveSession(req.SessionId)
	if err != nil {
		return nil, err
	}

	session.Status = "terminated"
	session.TerminatedAt = timestamppb.Now()

	return &computev1.TerminateSessionResponse{Success: true}, nil
}
