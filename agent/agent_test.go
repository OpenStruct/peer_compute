package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIfaceFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"full path with .conf", "/tmp/peer-compute/wg/pc-abc123.conf", "pc-abc123"},
		{"simple filename", "simple.conf", "simple"},
		{"path without extension", "/path/to/noext", "noext"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ifaceFromPath(tt.path)
			if got != tt.want {
				t.Errorf("ifaceFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("IsRoot not supported on windows")
	}
	// In normal test environments we are not root
	got := IsRoot()
	if got {
		t.Log("running as root; test still passes but this is unusual in CI")
	}
}

// --- Provider state persistence tests ---

func TestSaveAndLoadProviderState(t *testing.T) {
	dir := t.TempDir()
	state := &providerState{
		ProviderID: "prov-abc123",
		Token:      "tok_prov-abc123",
	}

	if err := saveProviderState(dir, state); err != nil {
		t.Fatalf("saveProviderState: %v", err)
	}

	loaded, err := loadProviderState(dir)
	if err != nil {
		t.Fatalf("loadProviderState: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadProviderState returned nil")
	}
	if loaded.ProviderID != state.ProviderID {
		t.Errorf("ProviderID = %q, want %q", loaded.ProviderID, state.ProviderID)
	}
	if loaded.Token != state.Token {
		t.Errorf("Token = %q, want %q", loaded.Token, state.Token)
	}
}

func TestLoadProviderState_FileNotExists(t *testing.T) {
	dir := t.TempDir()
	loaded, err := loadProviderState(dir)
	if err != nil {
		t.Fatalf("loadProviderState: %v", err)
	}
	if loaded != nil {
		t.Fatalf("expected nil for nonexistent file, got %+v", loaded)
	}
}

func TestLoadProviderState_EmptyProviderID(t *testing.T) {
	dir := t.TempDir()
	data, _ := json.Marshal(&providerState{ProviderID: "", Token: "tok"})
	os.WriteFile(filepath.Join(dir, "provider.json"), data, 0600)

	loaded, err := loadProviderState(dir)
	if err != nil {
		t.Fatalf("loadProviderState: %v", err)
	}
	if loaded != nil {
		t.Fatalf("expected nil for empty provider_id, got %+v", loaded)
	}
}

func TestLoadProviderState_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "provider.json"), []byte("not json"), 0600)

	_, err := loadProviderState(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSaveProviderState_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	state := &providerState{ProviderID: "prov-1", Token: "tok-1"}

	if err := saveProviderState(dir, state); err != nil {
		t.Fatalf("saveProviderState: %v", err)
	}

	// Verify file was created
	info, err := os.Stat(filepath.Join(dir, "provider.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestDefaultStateDir(t *testing.T) {
	dir := defaultStateDir()
	if dir == "" {
		t.Fatal("defaultStateDir returned empty string")
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("defaultStateDir = %q is not absolute", dir)
	}
}
