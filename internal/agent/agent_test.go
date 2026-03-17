package agent

import (
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
