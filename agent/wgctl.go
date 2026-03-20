package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// WGUp activates a WireGuard tunnel using wg-quick.
// Returns the interface name on success.
func WGUp(ctx context.Context, confPath string, log *slog.Logger) (string, error) {
	ifaceName := ifaceFromPath(confPath)
	log.Info("activating wireguard tunnel", "interface", ifaceName, "config", confPath)

	cmd := exec.CommandContext(ctx, "wg-quick", "up", confPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wg-quick up: %w: %s", err, strings.TrimSpace(string(output)))
	}
	log.Info("wireguard tunnel active", "interface", ifaceName)
	return ifaceName, nil
}

// WGDown deactivates a WireGuard tunnel.
func WGDown(ctx context.Context, confPath string, log *slog.Logger) error {
	ifaceName := ifaceFromPath(confPath)
	log.Info("deactivating wireguard tunnel", "interface", ifaceName)

	cmd := exec.CommandContext(ctx, "wg-quick", "down", confPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick down: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// HasWGQuick checks whether wg-quick is available on PATH.
func HasWGQuick() bool {
	_, err := exec.LookPath("wg-quick")
	return err == nil
}

// IsRoot checks whether the current process has root/admin privileges.
func IsRoot() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return os.Getuid() == 0
}

// ifaceFromPath extracts the interface name from a config path.
// /tmp/peer-compute/wg/pc-abc123def4.conf -> pc-abc123def4
func ifaceFromPath(confPath string) string {
	base := filepath.Base(confPath)
	return strings.TrimSuffix(base, ".conf")
}
