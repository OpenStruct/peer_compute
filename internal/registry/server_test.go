package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func newTestServer() *Server {
	return NewServer(nil)
}

// --- allocateTunnelIPs ---

func TestAllocateTunnelIPs_Sequential(t *testing.T) {
	s := newTestServer()
	pIP1, rIP1, err := s.allocateTunnelIPs("sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP1 != "10.99.1.1" || rIP1 != "10.99.1.2" {
		t.Errorf("first allocation = (%q, %q), want (10.99.1.1, 10.99.1.2)", pIP1, rIP1)
	}
	pIP2, rIP2, err := s.allocateTunnelIPs("sess-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP2 != "10.99.2.1" || rIP2 != "10.99.2.2" {
		t.Errorf("second allocation = (%q, %q), want (10.99.2.1, 10.99.2.2)", pIP2, rIP2)
	}
}

func TestAllocateTunnelIPs_WrapsMinorAt254(t *testing.T) {
	s := newTestServer()
	// Set hint to last minor in first major subnet
	s.nextTunnelHint = tunnelIndex{Major: 99, Minor: 254}
	pIP, rIP, err := s.allocateTunnelIPs("sess-wrap")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP != "10.99.254.1" || rIP != "10.99.254.2" {
		t.Errorf("allocation at 254 = (%q, %q), want (10.99.254.1, 10.99.254.2)", pIP, rIP)
	}
	// After minor 254, should advance to next major subnet (100.1)
	pIP2, rIP2, err := s.allocateTunnelIPs("sess-wrap2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP2 != "10.100.1.1" || rIP2 != "10.100.1.2" {
		t.Errorf("wrapped allocation = (%q, %q), want (10.100.1.1, 10.100.1.2)", pIP2, rIP2)
	}
}

func TestAllocateTunnelIPs_WrapsMajorAt126(t *testing.T) {
	s := newTestServer()
	// Set hint to last slot in the entire address space
	s.nextTunnelHint = tunnelIndex{Major: 126, Minor: 254}
	pIP, rIP, err := s.allocateTunnelIPs("sess-end")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP != "10.126.254.1" || rIP != "10.126.254.2" {
		t.Errorf("allocation at end = (%q, %q), want (10.126.254.1, 10.126.254.2)", pIP, rIP)
	}
	// After last slot, should wrap to beginning (99.1)
	pIP2, rIP2, err := s.allocateTunnelIPs("sess-restart")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP2 != "10.99.1.1" || rIP2 != "10.99.1.2" {
		t.Errorf("wrapped allocation = (%q, %q), want (10.99.1.1, 10.99.1.2)", pIP2, rIP2)
	}
}

func TestAllocateTunnelIPs_SkipsUsedSlots(t *testing.T) {
	s := newTestServer()
	// Allocate first slot
	_, _, err := s.allocateTunnelIPs("sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Allocate second slot
	_, _, err = s.allocateTunnelIPs("sess-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reclaim first slot and reset hint to start
	s.reclaimTunnelIndex("sess-1")
	s.tunnelMu.Lock()
	s.nextTunnelHint = tunnelIndex{Major: 99, Minor: 1}
	s.tunnelMu.Unlock()

	// Should reuse slot 99.1 since it was reclaimed
	pIP, rIP, err := s.allocateTunnelIPs("sess-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pIP != "10.99.1.1" || rIP != "10.99.1.2" {
		t.Errorf("reused allocation = (%q, %q), want (10.99.1.1, 10.99.1.2)", pIP, rIP)
	}
}

func TestAllocateTunnelIPs_Exhausted(t *testing.T) {
	s := newTestServer()
	// Fill all slots
	for i := 0; i < maxTunnelSlots; i++ {
		_, _, err := s.allocateTunnelIPs(fmt.Sprintf("sess-%d", i))
		if err != nil {
			t.Fatalf("unexpected error at slot %d: %v", i, err)
		}
	}
	// Next allocation should fail
	_, _, err := s.allocateTunnelIPs("sess-overflow")
	if err != ErrTunnelExhausted {
		t.Errorf("expected ErrTunnelExhausted, got %v", err)
	}
}

func TestAllocateTunnelIPs_ExhaustedThenReclaim(t *testing.T) {
	s := newTestServer()
	// Fill all slots
	for i := 0; i < maxTunnelSlots; i++ {
		_, _, err := s.allocateTunnelIPs(fmt.Sprintf("sess-%d", i))
		if err != nil {
			t.Fatalf("unexpected error at slot %d: %v", i, err)
		}
	}
	// Reclaim one slot
	s.reclaimTunnelIndex("sess-0")

	// Should now succeed
	_, _, err := s.allocateTunnelIPs("sess-new")
	if err != nil {
		t.Fatalf("expected allocation to succeed after reclaim, got %v", err)
	}
}

// --- reclaimTunnelIndex ---

func TestReclaimTunnelIndex(t *testing.T) {
	s := newTestServer()
	_, _, err := s.allocateTunnelIPs("sess-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.usedTunnelIdxs["sess-1"]; !ok {
		t.Fatal("session not in usedTunnelIdxs after allocation")
	}
	s.reclaimTunnelIndex("sess-1")
	if _, ok := s.usedTunnelIdxs["sess-1"]; ok {
		t.Error("session still in usedTunnelIdxs after reclaim")
	}
	// Also verify the reverse map is cleaned up
	if len(s.usedTunnelSet) != 0 {
		t.Error("usedTunnelSet not empty after reclaiming only session")
	}
}

func TestReclaimTunnelIndex_Idempotent(t *testing.T) {
	s := newTestServer()
	// Reclaiming a non-existent session should not panic
	s.reclaimTunnelIndex("nonexistent")
	if len(s.usedTunnelIdxs) != 0 || len(s.usedTunnelSet) != 0 {
		t.Error("maps should be empty after reclaiming non-existent session")
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

	created, err := s.CreateSession(ctx, &computev1.CreateSessionRequest{
		ProviderId:  reg.Provider.Id,
		RenterId:    "renter-1",
		Image:       "ubuntu:22.04",
		WgPublicKey: "renter-pk",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err = s.TerminateSession(ctx, &computev1.TerminateSessionRequest{
		SessionId: created.Session.Id,
	})
	if err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}

	resp, err = s.Heartbeat(ctx, &computev1.HeartbeatRequest{
		ProviderId:     reg.Provider.Id,
		ActiveSessions: []string{created.Session.Id},
	})
	if err != nil {
		t.Fatalf("Heartbeat with active sessions: %v", err)
	}
	if len(resp.TerminateSessions) != 1 || resp.TerminateSessions[0] != created.Session.Id {
		t.Fatalf("terminate sessions = %v, want [%s]", resp.TerminateSessions, created.Session.Id)
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

// --- Re-registration tests ---

// withProviderID returns a context with x-provider-id set in incoming gRPC metadata.
func withProviderID(ctx context.Context, id string) context.Context {
	md := metadata.Pairs("x-provider-id", id)
	return metadata.NewIncomingContext(ctx, md)
}

func TestReRegisterProvider_ExistingID(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// First registration: create a new provider.
	resp1, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "original",
		Address: "1.1.1.1:5000",
		Capacity: &computev1.Resources{
			CpuCores: 4,
			MemoryMb: 8192,
		},
	})
	if err != nil {
		t.Fatalf("first RegisterProvider: %v", err)
	}
	originalID := resp1.Provider.Id

	// Set provider offline to simulate a restart.
	prov, _ := s.plugins.Store.GetProvider(ctx, originalID)
	prov.Status = "offline"
	s.plugins.Store.PutProvider(ctx, prov)

	// Re-register with the same ID via metadata.
	reregCtx := withProviderID(ctx, originalID)
	resp2, err := s.RegisterProvider(reregCtx, &computev1.RegisterProviderRequest{
		Name:    "updated-name",
		Address: "2.2.2.2:6000",
		Capacity: &computev1.Resources{
			CpuCores: 8,
			MemoryMb: 16384,
		},
	})
	if err != nil {
		t.Fatalf("re-RegisterProvider: %v", err)
	}

	// Should reuse the same provider ID.
	if resp2.Provider.Id != originalID {
		t.Errorf("re-registration returned new ID %q, want %q", resp2.Provider.Id, originalID)
	}

	// Verify the provider record was updated.
	if resp2.Provider.Name != "updated-name" {
		t.Errorf("Name = %q, want %q", resp2.Provider.Name, "updated-name")
	}
	if resp2.Provider.Address != "2.2.2.2:6000" {
		t.Errorf("Address = %q, want %q", resp2.Provider.Address, "2.2.2.2:6000")
	}
	if resp2.Provider.Status != "online" {
		t.Errorf("Status = %q, want %q", resp2.Provider.Status, "online")
	}
	if resp2.Provider.Capacity.CpuCores != 8 {
		t.Errorf("CpuCores = %d, want 8", resp2.Provider.Capacity.CpuCores)
	}
	if resp2.Provider.Capacity.MemoryMb != 16384 {
		t.Errorf("MemoryMb = %d, want 16384", resp2.Provider.Capacity.MemoryMb)
	}
}

func TestReRegisterProvider_UnknownIDFallsBack(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Re-register with a non-existent ID should create a new provider.
	reregCtx := withProviderID(ctx, "nonexistent-id")
	resp, err := s.RegisterProvider(reregCtx, &computev1.RegisterProviderRequest{
		Name:    "fallback",
		Address: "3.3.3.3:7000",
		Capacity: &computev1.Resources{
			CpuCores: 2,
			MemoryMb: 4096,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider with unknown ID: %v", err)
	}

	// Should get a new ID, not the nonexistent one.
	if resp.Provider.Id == "nonexistent-id" {
		t.Error("should not reuse a nonexistent ID")
	}
	if resp.Provider.Id == "" {
		t.Error("Provider.Id should not be empty")
	}
	if resp.Provider.Name != "fallback" {
		t.Errorf("Name = %q, want %q", resp.Provider.Name, "fallback")
	}
}

func TestReRegisterProvider_NoMetadata(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Without metadata, should create new (current behavior).
	resp, err := s.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    "no-metadata",
		Address: "4.4.4.4:8000",
		Capacity: &computev1.Resources{
			CpuCores: 1,
			MemoryMb: 2048,
		},
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if resp.Provider.Id == "" {
		t.Error("expected a new provider ID")
	}
}

func TestProviderIDFromMetadata(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{
			name: "with metadata",
			ctx:  withProviderID(context.Background(), "test-id"),
			want: "test-id",
		},
		{
			name: "without metadata",
			ctx:  context.Background(),
			want: "",
		},
		{
			name: "empty metadata",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.MD{}),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := providerIDFromMetadata(tt.ctx)
			if got != tt.want {
				t.Errorf("providerIDFromMetadata() = %q, want %q", got, tt.want)
			}
		})
	}
}
