package registry

import (
	"context"
	"testing"
	"time"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer() *Server {
	return NewServer(nil)
}

// --- allocateTunnelIPs ---

func TestAllocateTunnelIPs_Sequential(t *testing.T) {
	s := newTestServer()
	pIP1, rIP1 := s.allocateTunnelIPs("sess-1")
	if pIP1 != "10.99.1.1" || rIP1 != "10.99.1.2" {
		t.Errorf("first allocation = (%q, %q), want (10.99.1.1, 10.99.1.2)", pIP1, rIP1)
	}
	pIP2, rIP2 := s.allocateTunnelIPs("sess-2")
	if pIP2 != "10.99.2.1" || rIP2 != "10.99.2.2" {
		t.Errorf("second allocation = (%q, %q), want (10.99.2.1, 10.99.2.2)", pIP2, rIP2)
	}
}

func TestAllocateTunnelIPs_WrapsAt254(t *testing.T) {
	s := newTestServer()
	s.nextTunnelIdx = 254
	pIP, rIP := s.allocateTunnelIPs("sess-wrap")
	if pIP != "10.99.254.1" || rIP != "10.99.254.2" {
		t.Errorf("allocation at 254 = (%q, %q), want (10.99.254.1, 10.99.254.2)", pIP, rIP)
	}
	// After 254, nextTunnelIdx should wrap to 1
	pIP2, rIP2 := s.allocateTunnelIPs("sess-wrap2")
	if pIP2 != "10.99.1.1" || rIP2 != "10.99.1.2" {
		t.Errorf("wrapped allocation = (%q, %q), want (10.99.1.1, 10.99.1.2)", pIP2, rIP2)
	}
}

// --- reclaimTunnelIndex ---

func TestReclaimTunnelIndex(t *testing.T) {
	s := newTestServer()
	s.allocateTunnelIPs("sess-1")
	if _, ok := s.usedTunnelIdxs["sess-1"]; !ok {
		t.Fatal("session not in usedTunnelIdxs after allocation")
	}
	s.reclaimTunnelIndex("sess-1")
	if _, ok := s.usedTunnelIdxs["sess-1"]; ok {
		t.Error("session still in usedTunnelIdxs after reclaim")
	}
}

// --- providerToProto ---

func TestProviderToProto(t *testing.T) {
	rec := &plugin.ProviderRecord{
		ID:            "prov-1",
		Name:          "test-provider",
		Address:       "192.168.1.1:5000",
		PublicAddress: "1.2.3.4:5000",
		CPUCores:      8,
		MemoryMB:      16384,
		DiskGB:        500,
		GPUCount:      2,
		GPUModel:      "A100",
		AvailCPU:      6,
		AvailMemoryMB: 12000,
		AvailGPU:      1,
		Status:        "online",
		RegisteredAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		LastHeartbeat: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC).Unix(),
	}
	p := providerToProto(rec)
	if p.Id != "prov-1" {
		t.Errorf("Id = %q, want %q", p.Id, "prov-1")
	}
	if p.Name != "test-provider" {
		t.Errorf("Name = %q, want %q", p.Name, "test-provider")
	}
	if p.Address != "192.168.1.1:5000" {
		t.Errorf("Address = %q, want %q", p.Address, "192.168.1.1:5000")
	}
	if p.PublicAddress != "1.2.3.4:5000" {
		t.Errorf("PublicAddress = %q, want %q", p.PublicAddress, "1.2.3.4:5000")
	}
	if p.Capacity == nil {
		t.Fatal("Capacity is nil")
	}
	if p.Capacity.CpuCores != 8 {
		t.Errorf("Capacity.CpuCores = %d, want 8", p.Capacity.CpuCores)
	}
	if p.Capacity.MemoryMb != 16384 {
		t.Errorf("Capacity.MemoryMb = %d, want 16384", p.Capacity.MemoryMb)
	}
	if p.Capacity.DiskGb != 500 {
		t.Errorf("Capacity.DiskGb = %d, want 500", p.Capacity.DiskGb)
	}
	if p.Capacity.GpuCount != 2 {
		t.Errorf("Capacity.GpuCount = %d, want 2", p.Capacity.GpuCount)
	}
	if p.Capacity.GpuModel != "A100" {
		t.Errorf("Capacity.GpuModel = %q, want %q", p.Capacity.GpuModel, "A100")
	}
	if p.Available == nil {
		t.Fatal("Available is nil")
	}
	if p.Available.CpuCores != 6 {
		t.Errorf("Available.CpuCores = %d, want 6", p.Available.CpuCores)
	}
	if p.Status != "online" {
		t.Errorf("Status = %q, want %q", p.Status, "online")
	}
}

// --- sessionToProto ---

func TestSessionToProto(t *testing.T) {
	now := time.Now().Unix()
	rec := &plugin.SessionRecord{
		ID:             "sess-1",
		ProviderID:     "prov-1",
		RenterID:       "rent-1",
		Status:         "active",
		WGProviderIP:   "10.99.1.1",
		WGRenterIP:     "10.99.1.2",
		RelayToken:     "relay-tok",
		ConnectionMode: "holepunch",
		CreatedAt:      now,
		TerminatedAt:   0,
	}
	s := sessionToProto(rec)
	if s.Id != "sess-1" {
		t.Errorf("Id = %q, want %q", s.Id, "sess-1")
	}
	if s.ProviderId != "prov-1" {
		t.Errorf("ProviderId = %q, want %q", s.ProviderId, "prov-1")
	}
	if s.RenterId != "rent-1" {
		t.Errorf("RenterId = %q, want %q", s.RenterId, "rent-1")
	}
	if s.Status != "active" {
		t.Errorf("Status = %q, want %q", s.Status, "active")
	}
	if s.WgProviderIp != "10.99.1.1" {
		t.Errorf("WgProviderIp = %q, want %q", s.WgProviderIp, "10.99.1.1")
	}
	if s.WgRenterIp != "10.99.1.2" {
		t.Errorf("WgRenterIp = %q, want %q", s.WgRenterIp, "10.99.1.2")
	}
	if s.TerminatedAt != nil {
		t.Error("TerminatedAt should be nil when record value is 0")
	}

	// Test with TerminatedAt set
	rec.TerminatedAt = now + 3600
	s2 := sessionToProto(rec)
	if s2.TerminatedAt == nil {
		t.Error("TerminatedAt should be non-nil when record value is nonzero")
	}
}

// --- storeErr ---

func TestStoreErr_NotFound(t *testing.T) {
	err := storeErr(plugin.ErrNotFound)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", st.Code())
	}
}

func TestStoreErr_AmbiguousPrefix(t *testing.T) {
	err := storeErr(plugin.ErrAmbiguousPrefix)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

func TestStoreErr_Other(t *testing.T) {
	err := storeErr(context.DeadlineExceeded)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want Internal", st.Code())
	}
}

// --- RPC integration tests ---

func TestRegisterProvider(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	resp, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "test",
		Address: "192.168.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 4,
			MemoryMb: 8192,
			GpuCount: 1,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if resp.Provider == nil {
		t.Fatal("Provider is nil")
	}
	if resp.Provider.Id == "" {
		t.Error("Provider.Id is empty")
	}
	if resp.Token == "" {
		t.Error("Token is empty")
	}
}

func TestHeartbeat(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	reg, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "hb-test",
		Address: "1.2.3.4:5000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	resp, err := s.Heartbeat(ctx, &computev1.HeartbeatRequest{
		ProviderId: reg.Provider.Id,
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !resp.Acknowledged {
		t.Error("Heartbeat not acknowledged")
	}
}

func TestListProviders(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	_, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "p1",
		Address: "1.1.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider 1: %v", err)
	}
	_, err = s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "p2",
		Address: "2.2.2.2:5000",
		Capacity: &computev1.Resources{
			CpuCores: 4,
			MemoryMb: 8192,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider 2: %v", err)
	}
	resp, err := s.ListProviders(ctx, &computev1.ListProvidersRequest{})
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(resp.Providers) != 2 {
		t.Errorf("len = %d, want 2", len(resp.Providers))
	}
}

func TestCreateSession(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	reg, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "cs-test",
		Address: "1.1.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	resp, err := s.CreateSession(ctx, &computev1.CreateSessionRequest{
		ProviderId:  reg.Provider.Id,
		WgPublicKey: "renter-pk",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.Session == nil {
		t.Fatal("Session is nil")
	}
	if resp.Session.WgProviderIp == "" || resp.Session.WgRenterIp == "" {
		t.Error("tunnel IPs not assigned")
	}
	if resp.Session.Status != "pending" {
		t.Errorf("status = %q, want pending", resp.Session.Status)
	}
}

func TestGetSession(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	reg, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "gs-test",
		Address: "1.1.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	created, err := s.CreateSession(ctx, &computev1.CreateSessionRequest{
		ProviderId:  reg.Provider.Id,
		WgPublicKey: "renter-pk",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	resp, err := s.GetSession(ctx, &computev1.GetSessionRequest{
		SessionId: created.Session.Id,
	})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if resp.Session.Id != created.Session.Id {
		t.Errorf("ID = %q, want %q", resp.Session.Id, created.Session.Id)
	}
}

func TestTerminateSession(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	reg, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "ts-test",
		Address: "1.1.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	created, err := s.CreateSession(ctx, &computev1.CreateSessionRequest{
		ProviderId:  reg.Provider.Id,
		WgPublicKey: "renter-pk",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	resp, err := s.TerminateSession(ctx, &computev1.TerminateSessionRequest{
		SessionId: created.Session.Id,
	})
	if err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}
	if !resp.Success {
		t.Error("TerminateSession did not return success")
	}

	// Verify the session is now terminated
	getResp, err := s.GetSession(ctx, &computev1.GetSessionRequest{
		SessionId: created.Session.Id,
	})
	if err != nil {
		t.Fatalf("GetSession after terminate: %v", err)
	}
	if getResp.Session.Status != "terminated" {
		t.Errorf("status = %q, want terminated", getResp.Session.Status)
	}
}

func TestCreateSession_ProviderOffline(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	reg, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "offline-test",
		Address: "1.1.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	// Set provider offline
	prov, err := s.plugins.Store.GetProvider(ctx, reg.Provider.Id)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	prov.Status = "offline"
	_ = s.plugins.Store.PutProvider(ctx, prov)

	_, err = s.CreateSession(ctx, &computev1.CreateSessionRequest{
		ProviderId:  reg.Provider.Id,
		WgPublicKey: "renter-pk",
	})
	if err == nil {
		t.Fatal("expected error for offline provider")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", st.Code())
	}
}
