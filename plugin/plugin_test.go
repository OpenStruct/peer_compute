package plugin

import (
	"context"
	"testing"
)

// --- OpenAuth tests ---

func TestOpenAuth_Authenticate_EmptyToken(t *testing.T) {
	a := OpenAuth{}
	userID, err := a.Authenticate(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userID != "anonymous" {
		t.Errorf("userID = %q, want %q", userID, "anonymous")
	}
}

func TestOpenAuth_Authenticate_WithToken(t *testing.T) {
	a := OpenAuth{}
	userID, err := a.Authenticate(context.Background(), "my-token-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userID != "my-token-123" {
		t.Errorf("userID = %q, want %q", userID, "my-token-123")
	}
}

func TestOpenAuth_Issue(t *testing.T) {
	a := OpenAuth{}
	tok, err := a.Issue(context.Background(), "user42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "tok_user42" {
		t.Errorf("token = %q, want %q", tok, "tok_user42")
	}
}

// --- OpenReputation tests ---

func TestOpenReputation_ProviderScore(t *testing.T) {
	r := OpenReputation{}
	score, err := r.ProviderScore(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score != 100 {
		t.Errorf("score = %d, want 100", score)
	}
}

func TestOpenReputation_RenterScore(t *testing.T) {
	r := OpenReputation{}
	score, err := r.RenterScore(context.Background(), "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score != 100 {
		t.Errorf("score = %d, want 100", score)
	}
}

func TestOpenReputation_RecordOutcome(t *testing.T) {
	r := OpenReputation{}
	err := r.RecordOutcome(context.Background(), "s1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenReputation_MeetsMinimum(t *testing.T) {
	r := OpenReputation{}
	ok, err := r.MeetsMinimum(context.Background(), "p1", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("MeetsMinimum returned false, want true")
	}
}

// --- MemoryStore Provider tests ---

func TestMemoryStore_PutGetProvider(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	rec := &ProviderRecord{
		ID:        "prov-1",
		Status:    "online",
		CPUCores:  4,
		MemoryMB:  8192,
		GPUCount:  1,
		AvailCPU:  4,
		AvailMemoryMB: 8192,
		AvailGPU:  1,
	}
	if err := s.PutProvider(ctx, rec); err != nil {
		t.Fatalf("PutProvider: %v", err)
	}
	got, err := s.GetProvider(ctx, "prov-1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.ID != "prov-1" || got.CPUCores != 4 || got.MemoryMB != 8192 || got.GPUCount != 1 {
		t.Errorf("got = %+v, want matching fields", got)
	}
}

func TestMemoryStore_GetProvider_NotFound(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.GetProvider(context.Background(), "nonexistent")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMemoryStore_GetProviderByPrefix_ExactMatch(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	rec := &ProviderRecord{ID: "prov-abc123", Status: "online"}
	_ = s.PutProvider(ctx, rec)

	got, err := s.GetProviderByPrefix(ctx, "prov-abc123")
	if err != nil {
		t.Fatalf("GetProviderByPrefix: %v", err)
	}
	if got.ID != "prov-abc123" {
		t.Errorf("ID = %q, want %q", got.ID, "prov-abc123")
	}
}

func TestMemoryStore_GetProviderByPrefix_PrefixMatch(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "prov-abc123", Status: "online"})

	got, err := s.GetProviderByPrefix(ctx, "prov-abc")
	if err != nil {
		t.Fatalf("GetProviderByPrefix: %v", err)
	}
	if got.ID != "prov-abc123" {
		t.Errorf("ID = %q, want %q", got.ID, "prov-abc123")
	}
}

func TestMemoryStore_GetProviderByPrefix_Ambiguous(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "prov-abc111", Status: "online"})
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "prov-abc222", Status: "online"})

	_, err := s.GetProviderByPrefix(ctx, "prov-abc")
	if err != ErrAmbiguousPrefix {
		t.Errorf("err = %v, want ErrAmbiguousPrefix", err)
	}
}

func TestMemoryStore_GetProviderByPrefix_NotFound(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.GetProviderByPrefix(context.Background(), "zzz")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMemoryStore_ListProviders_NoFilter(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "p1", Status: "online"})
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "p2", Status: "offline"})

	list, err := s.ListProviders(ctx, ProviderFilter{})
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestMemoryStore_ListProviders_WithFilter(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "p1", Status: "online", CPUCores: 8, MemoryMB: 16384, GPUCount: 2, AvailCPU: 8, AvailMemoryMB: 16384, AvailGPU: 2})
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "p2", Status: "offline", CPUCores: 2, MemoryMB: 4096, GPUCount: 0, AvailCPU: 2, AvailMemoryMB: 4096, AvailGPU: 0})
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "p3", Status: "online", CPUCores: 4, MemoryMB: 8192, GPUCount: 1, AvailCPU: 4, AvailMemoryMB: 8192, AvailGPU: 1})

	tests := []struct {
		name   string
		filter ProviderFilter
		want   int
	}{
		{"by status", ProviderFilter{Status: "online"}, 2},
		{"by min CPU", ProviderFilter{MinCPU: 4}, 2},
		{"by min memory", ProviderFilter{MinMemory: 10000}, 1},
		{"by min GPU", ProviderFilter{MinGPU: 1}, 2},
		{"combined", ProviderFilter{Status: "online", MinCPU: 8}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := s.ListProviders(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListProviders: %v", err)
			}
			if len(list) != tt.want {
				t.Errorf("len = %d, want %d", len(list), tt.want)
			}
		})
	}
}

func TestMemoryStore_DeleteProvider(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutProvider(ctx, &ProviderRecord{ID: "p1", Status: "online"})

	if err := s.DeleteProvider(ctx, "p1"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	_, err := s.GetProvider(ctx, "p1")
	if err != ErrNotFound {
		t.Errorf("after delete, err = %v, want ErrNotFound", err)
	}
}

// --- MemoryStore Session tests ---

func TestMemoryStore_PutGetSession(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	rec := &SessionRecord{
		ID:         "sess-1",
		ProviderID: "p1",
		RenterID:   "r1",
		Status:     "active",
	}
	if err := s.PutSession(ctx, rec); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	got, err := s.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != "sess-1" || got.ProviderID != "p1" || got.RenterID != "r1" {
		t.Errorf("got = %+v", got)
	}
}

func TestMemoryStore_GetSessionByPrefix_ExactMatch(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutSession(ctx, &SessionRecord{ID: "sess-abc123", Status: "active"})

	got, err := s.GetSessionByPrefix(ctx, "sess-abc123")
	if err != nil {
		t.Fatalf("GetSessionByPrefix: %v", err)
	}
	if got.ID != "sess-abc123" {
		t.Errorf("ID = %q, want %q", got.ID, "sess-abc123")
	}
}

func TestMemoryStore_GetSessionByPrefix_Ambiguous(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutSession(ctx, &SessionRecord{ID: "sess-abc111", Status: "active"})
	_ = s.PutSession(ctx, &SessionRecord{ID: "sess-abc222", Status: "active"})

	_, err := s.GetSessionByPrefix(ctx, "sess-abc")
	if err != ErrAmbiguousPrefix {
		t.Errorf("err = %v, want ErrAmbiguousPrefix", err)
	}
}

func TestMemoryStore_ListSessions_WithFilter(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutSession(ctx, &SessionRecord{ID: "s1", ProviderID: "p1", RenterID: "r1", Status: "active"})
	_ = s.PutSession(ctx, &SessionRecord{ID: "s2", ProviderID: "p2", RenterID: "r1", Status: "terminated"})
	_ = s.PutSession(ctx, &SessionRecord{ID: "s3", ProviderID: "p1", RenterID: "r2", Status: "terminated"})

	tests := []struct {
		name   string
		filter SessionFilter
		want   int
	}{
		{"no filter", SessionFilter{}, 3},
		{"by provider", SessionFilter{ProviderID: "p1"}, 2},
		{"by renter", SessionFilter{RenterID: "r1"}, 2},
		{"by status", SessionFilter{Status: "active"}, 1},
		{"combined", SessionFilter{ProviderID: "p1", Status: "active"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := s.ListSessions(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListSessions: %v", err)
			}
			if len(list) != tt.want {
				t.Errorf("len = %d, want %d", len(list), tt.want)
			}
		})
	}
}

func TestMemoryStore_DeleteSession(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.PutSession(ctx, &SessionRecord{ID: "s1", Status: "active"})

	if err := s.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	_, err := s.GetSession(ctx, "s1")
	if err != ErrNotFound {
		t.Errorf("after delete, err = %v, want ErrNotFound", err)
	}
}

// --- Defaults test ---

func TestDefaults(t *testing.T) {
	b := Defaults()
	if b.Auth == nil {
		t.Error("Auth is nil")
	}
	if b.Reputation == nil {
		t.Error("Reputation is nil")
	}
	if b.Store == nil {
		t.Error("Store is nil")
	}
	if _, ok := b.Auth.(OpenAuth); !ok {
		t.Errorf("Auth type = %T, want OpenAuth", b.Auth)
	}
	if _, ok := b.Reputation.(OpenReputation); !ok {
		t.Errorf("Reputation type = %T, want OpenReputation", b.Reputation)
	}
	if _, ok := b.Store.(*MemoryStore); !ok {
		t.Errorf("Store type = %T, want *MemoryStore", b.Store)
	}
}
