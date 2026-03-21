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

	if !IsRoot() {
		return "", fmt.Errorf("wg-quick up requires root privileges")
	}

	cmd, err := wgQuickCommand(ctx, "up", confPath)
	if err != nil {
		return "", err
	}
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

	if !IsRoot() {
		return fmt.Errorf("wg-quick down requires root privileges")
	}

	cmd, err := wgQuickCommand(ctx, "down", confPath)
	if err != nil {
		return err
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick down: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func wgQuickCommand(ctx context.Context, action, confPath string) (*exec.Cmd, error) {
	wgQuickPath, err := exec.LookPath("wg-quick")
	if err != nil {
		return nil, fmt.Errorf("wg-quick not found: %w", err)
	}

	// On macOS, wg-quick can require Bash 4+, while /bin/bash is 3.x.
	// Prefer Homebrew bash when present and invoke the script through it.
	if runtime.GOOS == "darwin" {
		for _, bashPath := range []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash"} {
			if _, err := os.Stat(bashPath); err == nil {
				return exec.CommandContext(ctx, bashPath, wgQuickPath, action, confPath), nil
			}
		}
	}

	return exec.CommandContext(ctx, wgQuickPath, action, confPath), nil
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
