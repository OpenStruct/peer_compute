package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/internal/relay"
	"github.com/OpenStruct/peer_compute/plugin"
)

const heartbeatTimeout = 30 * time.Second

// tunnelIndex represents a two-octet tunnel address: 10.{Major}.{Minor}.{role}.
type tunnelIndex struct {
	Major uint8 // 99–126
	Minor uint8 // 1–254
}

const (
	tunnelMajorMin = 99
	tunnelMajorMax = 126
	tunnelMinorMin = 1
	tunnelMinorMax = 254
	// maxTunnelSlots is (126-99+1) * (254-1+1) = 28 * 254 = 7112
	maxTunnelSlots = int(tunnelMajorMax-tunnelMajorMin+1) * int(tunnelMinorMax-tunnelMinorMin+1)
)

// ErrTunnelExhausted is returned when all tunnel IP slots are in use.
var ErrTunnelExhausted = errors.New("tunnel IP address space exhausted (all 7112 slots in use)")

// Server implements the RegistryService gRPC server.
type Server struct {
	computev1.UnimplementedRegistryServiceServer

	// NAT traversal state: sessionID -> peerID -> candidateSet (always in-memory)
	mu         sync.RWMutex
	candidates map[string]map[string]*candidateSet

	// Tunnel IP allocation for multi-session WireGuard.
	// Uses a two-octet address space: 10.{major}.{minor}.{role}
	// major ranges 99-126, minor ranges 1-254, giving 7112 concurrent sessions.
	tunnelMu       sync.Mutex
	nextTunnelHint tunnelIndex            // hint for where to start scanning
	usedTunnelIdxs map[string]tunnelIndex // sessionID -> allocated index
	usedTunnelSet  map[tunnelIndex]string // allocated index -> sessionID (reverse lookup)

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
		candidates:     make(map[string]map[string]*candidateSet),
		nextTunnelHint: tunnelIndex{Major: tunnelMajorMin, Minor: tunnelMinorMin},
		usedTunnelIdxs: make(map[string]tunnelIndex),
		usedTunnelSet:  make(map[tunnelIndex]string),
		relay:          relayServer,
		plugins:        plugin.Defaults(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// advanceTunnel moves a tunnelIndex to the next slot, wrapping around the
// two-octet address space (major: 99-126, minor: 1-254).
func advanceTunnel(idx tunnelIndex) tunnelIndex {
	idx.Minor++
	if idx.Minor > tunnelMinorMax {
		idx.Minor = tunnelMinorMin
		idx.Major++
		if idx.Major > tunnelMajorMax {
			idx.Major = tunnelMajorMin
		}
	}
	return idx
}

// allocateTunnelIPs assigns unique per-session tunnel IPs using a free-list
// scan over the two-octet address space. Returns an error when all 7112
// slots are in use.
func (s *Server) allocateTunnelIPs(sessionID string) (providerIP, renterIP string, err error) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()

	if len(s.usedTunnelSet) >= maxTunnelSlots {
		return "", "", ErrTunnelExhausted
	}

	// Scan from the hint position to find the next free slot.
	candidate := s.nextTunnelHint
	for {
		if _, taken := s.usedTunnelSet[candidate]; !taken {
			break
		}
		candidate = advanceTunnel(candidate)
		// We already checked len < max above, so this loop will terminate.
	}

	// Record the allocation in both maps.
	s.usedTunnelIdxs[sessionID] = candidate
	s.usedTunnelSet[candidate] = sessionID

	// Advance the hint past the slot we just allocated.
	s.nextTunnelHint = advanceTunnel(candidate)

	return fmt.Sprintf("10.%d.%d.1", candidate.Major, candidate.Minor),
		fmt.Sprintf("10.%d.%d.2", candidate.Major, candidate.Minor), nil
}

// reclaimTunnelIndex frees a tunnel index when a session terminates,
// making it available for reuse by future sessions.
func (s *Server) reclaimTunnelIndex(sessionID string) {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if idx, ok := s.usedTunnelIdxs[sessionID]; ok {
		delete(s.usedTunnelSet, idx)
		delete(s.usedTunnelIdxs, sessionID)
	}
}

// ── Conversions ────────────────────────────────────────────────────

func providerToProto(r *plugin.ProviderRecord) *computev1.Provider {
	return &computev1.Provider{
		Id:      r.ID,
		Name:    r.Name,
		Address: r.Address,
		Capacity: &computev1.Resources{
			CpuCores: r.CPUCores,
			MemoryMb: r.MemoryMB,
			DiskGb:   r.DiskGB,
			GpuCount: r.GPUCount,
			GpuModel: r.GPUModel,
		},
		Available: &computev1.Resources{
			CpuCores: r.AvailCPU,
			MemoryMb: r.AvailMemoryMB,
			GpuCount: r.AvailGPU,
		},
		Status:        r.Status,
		LastHeartbeat: timestamppb.New(time.Unix(r.LastHeartbeat, 0)),
		RegisteredAt:  timestamppb.New(time.Unix(r.RegisteredAt, 0)),
		PublicAddress: r.PublicAddress,
	}
}

func sessionToProto(r *plugin.SessionRecord) *computev1.Session {
	s := &computev1.Session{
		Id:         r.ID,
		ProviderId: r.ProviderID,
		RenterId:   r.RenterID,
		Allocated: &computev1.Resources{
			CpuCores: r.CPUCores,
			MemoryMb: r.MemoryMB,
		},
		Status:         r.Status,
		ContainerId:    r.ContainerID,
		SshEndpoint:    r.SSHEndpoint,
		Image:          r.Image,
		CreatedAt:      timestamppb.New(time.Unix(r.CreatedAt, 0)),
		WgPublicKey:    r.WGPublicKey,
		WgEndpoint:     r.WGEndpoint,
		ConnectionMode: r.ConnectionMode,
		RelayToken:     r.RelayToken,
		WgProviderIp:   r.WGProviderIP,
		WgRenterIp:     r.WGRenterIP,
	}
	if r.TerminatedAt > 0 {
		s.TerminatedAt = timestamppb.New(time.Unix(r.TerminatedAt, 0))
	}
	return s
}

func storeErr(err error) error {
	if errors.Is(err, plugin.ErrNotFound) {
		return status.Error(codes.NotFound, "not found")
	}
	if errors.Is(err, plugin.ErrAmbiguousPrefix) {
		return status.Error(codes.InvalidArgument, "ambiguous ID prefix")
	}
	return status.Errorf(codes.Internal, "store: %v", err)
}

// ── RPCs ───────────────────────────────────────────────────────────

// providerIDFromMetadata extracts the "x-provider-id" value from incoming gRPC
// metadata. Returns empty string if not present.
func providerIDFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("x-provider-id")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (s *Server) RegisterProvider(ctx context.Context, req *computev1.RegisterProviderRequest) (*computev1.RegisterProviderResponse, error) {
	now := time.Now().Unix()

	// Check for re-registration: if the caller supplies an existing provider ID
	// via gRPC metadata, update the existing record instead of creating a new one.
	if existingID := providerIDFromMetadata(ctx); existingID != "" {
		existing, err := s.plugins.Store.GetProvider(ctx, existingID)
		if err == nil {
			// Provider found — update its info and reset to online.
			existing.Name = req.Name
			existing.Address = req.Address
			existing.Status = "online"
			existing.CPUCores = req.Capacity.GetCpuCores()
			existing.MemoryMB = req.Capacity.GetMemoryMb()
			existing.DiskGB = req.Capacity.GetDiskGb()
			existing.GPUCount = req.Capacity.GetGpuCount()
			existing.GPUModel = req.Capacity.GetGpuModel()
			existing.AvailCPU = req.Capacity.GetCpuCores()
			existing.AvailMemoryMB = req.Capacity.GetMemoryMb()
			existing.AvailGPU = req.Capacity.GetGpuCount()
			existing.LastHeartbeat = now

			if err := s.plugins.Store.PutProvider(ctx, existing); err != nil {
				return nil, storeErr(err)
			}

			// Notify plugin hook (e.g. to refresh provider-user link).
			if hook, ok := s.plugins.Auth.(plugin.RegistrationHook); ok {
				if err := hook.OnProviderRegistered(ctx, existingID); err != nil {
					return nil, status.Errorf(codes.Internal, "registration hook: %v", err)
				}
			}

			return &computev1.RegisterProviderResponse{
				Provider: providerToProto(existing),
				Token:    "tok_" + existingID,
			}, nil
		}
		// If provider not found, fall through to create a new one.
		if !errors.Is(err, plugin.ErrNotFound) {
			return nil, storeErr(err)
		}
	}

	// New registration (current behavior).
	id := uuid.NewString()

	rec := &plugin.ProviderRecord{
		ID:            id,
		Name:          req.Name,
		Address:       req.Address,
		Status:        "online",
		CPUCores:      req.Capacity.GetCpuCores(),
		MemoryMB:      req.Capacity.GetMemoryMb(),
		DiskGB:        req.Capacity.GetDiskGb(),
		GPUCount:      req.Capacity.GetGpuCount(),
		GPUModel:      req.Capacity.GetGpuModel(),
		AvailCPU:      req.Capacity.GetCpuCores(),
		AvailMemoryMB: req.Capacity.GetMemoryMb(),
		AvailGPU:      req.Capacity.GetGpuCount(),
		RegisteredAt:  now,
		LastHeartbeat: now,
	}

	if err := s.plugins.Store.PutProvider(ctx, rec); err != nil {
		return nil, storeErr(err)
	}

	// Notify plugin if it implements RegistrationHook (e.g. to link provider to user).
	if hook, ok := s.plugins.Auth.(plugin.RegistrationHook); ok {
		if err := hook.OnProviderRegistered(ctx, id); err != nil {
			return nil, status.Errorf(codes.Internal, "registration hook: %v", err)
		}
	}

	return &computev1.RegisterProviderResponse{
		Provider: providerToProto(rec),
		Token:    "tok_" + id,
	}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *computev1.HeartbeatRequest) (*computev1.HeartbeatResponse, error) {
	rec, err := s.plugins.Store.GetProviderByPrefix(ctx, req.ProviderId)
	if err != nil {
		return nil, storeErr(err)
	}

	now := time.Now().Unix()

	if req.Available != nil {
		// Resource availability changed — do a full update.
		rec.LastHeartbeat = now
		rec.Status = "online"
		rec.AvailCPU = req.Available.CpuCores
		rec.AvailMemoryMB = req.Available.MemoryMb
		rec.AvailGPU = req.Available.GpuCount
		if err := s.plugins.Store.PutProvider(ctx, rec); err != nil {
			return nil, storeErr(err)
		}
	} else {
		// No resource changes — lightweight heartbeat UPDATE only.
		if err := s.plugins.Store.UpdateHeartbeat(ctx, rec.ID, now); err != nil {
			return nil, storeErr(err)
		}
	}

	resp := &computev1.HeartbeatResponse{Acknowledged: true}

	// Batch-check terminated sessions in a single query instead of N individual lookups.
	if len(req.ActiveSessions) > 0 {
		terminated, err := s.plugins.Store.GetTerminatedSessionIDs(ctx, req.ActiveSessions, rec.ID)
		if err == nil {
			resp.TerminateSessions = terminated
		}
	}

	return resp, nil
}

func (s *Server) ListProviders(ctx context.Context, req *computev1.ListProvidersRequest) (*computev1.ListProvidersResponse, error) {
	// First get all online providers
	records, err := s.plugins.Store.ListProviders(ctx, plugin.ProviderFilter{
		MinCPU:    req.MinCpu,
		MinMemory: req.MinMemoryMb,
		MinGPU:    req.MinGpu,
	})
	if err != nil {
		return nil, storeErr(err)
	}

	stale := time.Now().Add(-heartbeatTimeout).Unix()
	var out []*computev1.Provider
	for _, rec := range records {
		if rec.LastHeartbeat < stale {
			rec.Status = "offline"
			s.plugins.Store.PutProvider(ctx, rec)
			continue
		}
		if rec.Status != "online" {
			continue
		}
		out = append(out, providerToProto(rec))
	}

	return &computev1.ListProvidersResponse{Providers: out}, nil
}

func (s *Server) CreateSession(ctx context.Context, req *computev1.CreateSessionRequest) (*computev1.CreateSessionResponse, error) {
	pRec, err := s.plugins.Store.GetProviderByPrefix(ctx, req.ProviderId)
	if err != nil {
		return nil, storeErr(err)
	}
	if pRec.Status != "online" {
		return nil, status.Error(codes.Unavailable, "provider is not online")
	}

	// Plugin: check provider reputation
	if ok, err := s.plugins.Reputation.MeetsMinimum(ctx, pRec.ID, 50); err != nil || !ok {
		return nil, status.Error(codes.Unavailable, "provider does not meet minimum reputation")
	}

	relayToken := uuid.NewString()
	sessionID := uuid.NewString()

	// Allocate per-session tunnel IPs
	providerIP, renterIP, err := s.allocateTunnelIPs(sessionID)
	if err != nil {
		return nil, status.Errorf(codes.ResourceExhausted, "tunnel allocation: %v", err)
	}

	rec := &plugin.SessionRecord{
		ID:           sessionID,
		ProviderID:   pRec.ID,
		RenterID:     req.RenterId,
		Image:        req.Image,
		Status:       "pending",
		RelayToken:   relayToken,
		WGProviderIP: providerIP,
		WGRenterIP:   renterIP,
		CreatedAt:    time.Now().Unix(),
	}
	if req.Requested != nil {
		rec.CPUCores = req.Requested.CpuCores
		rec.MemoryMB = req.Requested.MemoryMb
	}

	if err := s.plugins.Store.PutSession(ctx, rec); err != nil {
		return nil, storeErr(err)
	}

	// Store renter's WG public key if provided
	if req.WgPublicKey != "" {
		s.mu.Lock()
		if s.candidates[sessionID] == nil {
			s.candidates[sessionID] = make(map[string]*candidateSet)
		}
		s.candidates[sessionID][req.RenterId] = &candidateSet{
			wgPublicKey: req.WgPublicKey,
		}
		s.mu.Unlock()
	}

	return &computev1.CreateSessionResponse{Session: sessionToProto(rec)}, nil
}

func (s *Server) GetSession(ctx context.Context, req *computev1.GetSessionRequest) (*computev1.GetSessionResponse, error) {
	rec, err := s.plugins.Store.GetSessionByPrefix(ctx, req.SessionId)
	if err != nil {
		return nil, storeErr(err)
	}

	return &computev1.GetSessionResponse{Session: sessionToProto(rec)}, nil
}

func (s *Server) TerminateSession(ctx context.Context, req *computev1.TerminateSessionRequest) (*computev1.TerminateSessionResponse, error) {
	rec, err := s.plugins.Store.GetSessionByPrefix(ctx, req.SessionId)
	if err != nil {
		return nil, storeErr(err)
	}

	rec.Status = "terminated"
	rec.TerminatedAt = time.Now().Unix()

	if err := s.plugins.Store.PutSession(ctx, rec); err != nil {
		return nil, storeErr(err)
	}

	s.plugins.Reputation.RecordOutcome(ctx, rec.ID, true)

	// Clean up relay, candidate, and tunnel index state
	if rec.RelayToken != "" && s.relay != nil {
		s.relay.RemoveSession(rec.RelayToken)
	}
	s.mu.Lock()
	delete(s.candidates, rec.ID)
	s.mu.Unlock()
	s.reclaimTunnelIndex(rec.ID)

	return &computev1.TerminateSessionResponse{Success: true}, nil
}

func (s *Server) ListProviderSessions(ctx context.Context, req *computev1.ListProviderSessionsRequest) (*computev1.ListProviderSessionsResponse, error) {
	records, err := s.plugins.Store.ListSessions(ctx, plugin.SessionFilter{
		ProviderID: req.ProviderId,
		Status:     req.StatusFilter,
	})
	if err != nil {
		return nil, storeErr(err)
	}

	out := make([]*computev1.Session, len(records))
	for i, rec := range records {
		out[i] = sessionToProto(rec)
	}

	return &computev1.ListProviderSessionsResponse{Sessions: out}, nil
}

func (s *Server) UpdateSessionStatus(ctx context.Context, req *computev1.UpdateSessionStatusRequest) (*computev1.UpdateSessionStatusResponse, error) {
	rec, err := s.plugins.Store.GetSessionByPrefix(ctx, req.SessionId)
	if err != nil {
		return nil, storeErr(err)
	}

	prevStatus := rec.Status
	rec.Status = req.Status

	if req.Status == "terminated" && prevStatus != "terminated" {
		s.plugins.Reputation.RecordOutcome(ctx, rec.ID, true)
		rec.TerminatedAt = time.Now().Unix()
		s.reclaimTunnelIndex(rec.ID)
	}
	if req.ContainerId != "" {
		rec.ContainerID = req.ContainerId
	}
	if req.SshEndpoint != "" {
		rec.SSHEndpoint = req.SshEndpoint
	}
	if req.WgPublicKey != "" {
		rec.WGPublicKey = req.WgPublicKey
	}
	if req.WgEndpoint != "" {
		rec.WGEndpoint = req.WgEndpoint
	}
	if req.Status == "running" {
		if req.WgEndpoint == "" {
			rec.ConnectionMode = "compat"
		} else if rec.ConnectionMode == "" {
			rec.ConnectionMode = "direct"
		}
	}

	if err := s.plugins.Store.PutSession(ctx, rec); err != nil {
		return nil, storeErr(err)
	}

	return &computev1.UpdateSessionStatusResponse{Success: true}, nil
}

// ── NAT Traversal RPCs ─────────────────────────────────────────────

func (s *Server) ExchangeCandidates(ctx context.Context, req *computev1.ExchangeCandidatesRequest) (*computev1.ExchangeCandidatesResponse, error) {
	rec, err := s.plugins.Store.GetSessionByPrefix(ctx, req.SessionId)
	if err != nil {
		return nil, storeErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Store this peer's candidates
	if s.candidates[rec.ID] == nil {
		s.candidates[rec.ID] = make(map[string]*candidateSet)
	}
	s.candidates[rec.ID][req.PeerId] = &candidateSet{
		candidates:  req.Candidates,
		wgPublicKey: req.WgPublicKey,
	}

	// Look for the other peer's candidates
	resp := &computev1.ExchangeCandidatesResponse{}
	if s.relay != nil {
		resp.RelayAddress = s.relay.Addr()
	}
	resp.RelayToken = rec.RelayToken

	for peerID, cs := range s.candidates[rec.ID] {
		if peerID != req.PeerId {
			resp.PeerCandidates = cs.candidates
			resp.PeerWgPublicKey = cs.wgPublicKey
			break
		}
	}

	return resp, nil
}

func (s *Server) ReportConnectionResult(ctx context.Context, req *computev1.ReportConnectionResultRequest) (*computev1.ReportConnectionResultResponse, error) {
	rec, err := s.plugins.Store.GetSessionByPrefix(ctx, req.SessionId)
	if err != nil {
		return nil, storeErr(err)
	}

	if req.UseRelay {
		rec.ConnectionMode = "relay"
		if s.relay != nil {
			s.relay.RegisterSession(rec.RelayToken, rec.ID)
		}
	} else {
		rec.ConnectionMode = "holepunch"
	}

	if err := s.plugins.Store.PutSession(ctx, rec); err != nil {
		return nil, storeErr(err)
	}

	return &computev1.ReportConnectionResultResponse{Acknowledged: true}, nil
}
