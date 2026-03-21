package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
)

// debianSSHBootstrap installs OpenSSH if missing and runs sshd in the foreground.
// Vanilla ubuntu:/debian: images default to a shell that exits immediately when run
// non-interactively; sessions require a long-running process and something listening on 22/tcp.
// If PEER_COMPUTE_SSH_PUBLIC_KEY is set, it is written to /root/.ssh/authorized_keys
// for per-session key-based authentication. Password auth is kept as fallback for the
// web terminal proxy.
const debianSSHBootstrap = `set -e
export DEBIAN_FRONTEND=noninteractive
if ! command -v sshd >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq openssh-server
fi
mkdir -p /var/run/sshd
echo "root:$PEER_COMPUTE_SESSION_ROOT_PASSWORD" | chpasswd
sed -i 's/^#*PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config
mkdir -p /root/.ssh && chmod 700 /root/.ssh
if [ -n "$PEER_COMPUTE_SSH_PUBLIC_KEY" ]; then
  echo "$PEER_COMPUTE_SSH_PUBLIC_KEY" > /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
fi
exec /usr/sbin/sshd -D -e`

// StartContainerOpts holds all parameters for starting a session container.
type StartContainerOpts struct {
	SessionID    string
	Resources    *computev1.Resources
	ImageName    string
	SSHPublicKey string
	GPUCount     int
	ExtraEnv     map[string]string // e.g., {"PEER_COMPUTE_JUPYTER_TOKEN": "abc123"}
}

// ContainerRunner manages Docker containers for compute sessions.
type ContainerRunner struct {
	cli *client.Client
	log *slog.Logger
}

func NewContainerRunner(log *slog.Logger) (*ContainerRunner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &ContainerRunner{cli: cli, log: log}, nil
}

func (cr *ContainerRunner) Close() error {
	return cr.cli.Close()
}

// Ping verifies that the Docker daemon is reachable. A non-nil error typically
// means Docker Desktop is not running or the socket is inaccessible.
func (cr *ContainerRunner) Ping(ctx context.Context) error {
	_, err := cr.cli.Ping(ctx)
	return err
}

// createSessionNetwork creates an isolated Docker bridge network for a session.
// Inter-container communication is disabled to prevent cross-session traffic.
func (cr *ContainerRunner) createSessionNetwork(ctx context.Context, sessionID string) (string, error) {
	networkName := "pcp-" + sessionID[:12]
	resp, err := cr.cli.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc": "false", // No inter-container communication
		},
		Labels: map[string]string{
			"pcp.managed":    "true",
			"pcp.session_id": sessionID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("network create: %w", err)
	}
	cr.log.Debug("created session network", "network", networkName, "id", resp.ID[:12])
	return resp.ID, nil
}

// removeSessionNetwork removes the per-session Docker network.
// Errors are logged but not returned since this is best-effort cleanup.
func (cr *ContainerRunner) removeSessionNetwork(ctx context.Context, sessionID string) {
	networkName := "pcp-" + sessionID[:12]
	if err := cr.cli.NetworkRemove(ctx, networkName); err != nil {
		cr.log.Warn("network remove warning", "network", networkName, "error", err)
	} else {
		cr.log.Debug("removed session network", "network", networkName)
	}
}

// StartContainer pulls the image and runs a container with resource limits,
// security hardening, and per-session network isolation.
// Returns the container ID, the network ID, and the host-mapped SSH port.
func (cr *ContainerRunner) StartContainer(ctx context.Context, opts StartContainerOpts) (containerID string, networkID string, sshPort string, err error) {
	// 1. Create per-session isolated network
	networkID, err = cr.createSessionNetwork(ctx, opts.SessionID)
	if err != nil {
		return "", "", "", err
	}

	// 2. Pull image with a 5-minute timeout
	cr.log.Debug("pulling image", "image", opts.ImageName)
	pullCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	reader, err := cr.cli.ImagePull(pullCtx, opts.ImageName, image.PullOptions{})
	if err != nil {
		cr.removeSessionNetwork(ctx, opts.SessionID)
		return "", "", "", fmt.Errorf("image pull: %w", err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	// 3. Resource limits
	var nanoCPUs int64
	var memBytes int64
	if opts.Resources != nil {
		nanoCPUs = int64(opts.Resources.CpuCores) * 1e9
		memBytes = int64(opts.Resources.MemoryMb) * 1024 * 1024
	}

	// 4. Build environment variables
	env := buildEnv(opts)

	// 5. Container configuration with labels for identification
	containerCfg := &container.Config{
		Image: opts.ImageName,
		ExposedPorts: nat.PortSet{
			"22/tcp": struct{}{},
		},
		Labels: map[string]string{
			"pcp.managed":          "true",
			"pcp.session_id":       opts.SessionID,
			"peer-compute.session": opts.SessionID,
		},
	}
	if cmd := sessionContainerCmd(opts.ImageName); cmd != nil {
		containerCfg.Cmd = cmd
		containerCfg.Env = env
		cr.log.Debug("using session container command override", "image", opts.ImageName)
	}

	// 6. Host configuration with security hardening
	pidsLimit := int64(512)
	hostCfg := &container.HostConfig{
		Resources: container.Resources{
			NanoCPUs:  nanoCPUs,
			Memory:    memBytes,
			PidsLimit: &pidsLimit, // Prevent fork bombs
		},
		SecurityOpt: []string{
			"no-new-privileges", // Prevent privilege escalation
		},
		CapDrop: []string{"ALL"}, // Drop all capabilities
		CapAdd: []string{
			"CHOWN",            // Needed for apt/package managers
			"SETUID",           // Needed for sshd
			"SETGID",           // Needed for sshd
			"DAC_OVERRIDE",     // Needed for sshd
			"NET_BIND_SERVICE", // Needed for listening on port 22
			"SYS_CHROOT",       // Needed for sshd
		},
		ReadonlyRootfs: false, // Needs to be writable for package installs
		Tmpfs: map[string]string{
			"/tmp": "size=256m", // tmpfs for temp files
			"/run": "size=64m",  // tmpfs for runtime files
		},
		PortBindings: nat.PortMap{
			"22/tcp": []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: ""}, // auto-assign
			},
		},
	}

	// 7. GPU support
	if opts.GPUCount > 0 {
		hostCfg.Resources.DeviceRequests = []container.DeviceRequest{
			{
				Count:        opts.GPUCount,
				Capabilities: [][]string{{"gpu"}},
			},
		}
	}

	// 8. Networking configuration to attach container to session network
	networkingCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			"pcp-" + opts.SessionID[:12]: {
				NetworkID: networkID,
			},
		},
	}

	// 9. Remove any existing container with the same name to prevent collisions
	containerName := "pc-" + opts.SessionID[:12]
	cr.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	// Ignore errors — container may not exist

	// 10. Create and start the container
	resp, err := cr.cli.ContainerCreate(ctx, containerCfg, hostCfg, networkingCfg, nil, containerName)
	if err != nil {
		cr.removeSessionNetwork(ctx, opts.SessionID)
		return "", "", "", fmt.Errorf("container create: %w", err)
	}

	if err := cr.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		cr.removeSessionNetwork(ctx, opts.SessionID)
		return "", "", "", fmt.Errorf("container start: %w", err)
	}

	// 11. Get the assigned host port. Docker can take a brief moment to publish
	// the mapping after container start.
	for i := 0; i < 20; i++ {
		inspect, err := cr.cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			return resp.ID, networkID, "", fmt.Errorf("container inspect: %w", err)
		}
		if inspect.State != nil && !inspect.State.Running {
			return resp.ID, networkID, "", containerStartExitErr(inspect.State.Status, inspect.State.ExitCode)
		}
		bindings := inspect.NetworkSettings.Ports["22/tcp"]
		if len(bindings) > 0 && bindings[0].HostPort != "" {
			sshPort = bindings[0].HostPort
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cr.log.Info("started container", "container_id", resp.ID[:12], "ssh_port", sshPort, "network", "pcp-"+opts.SessionID[:12])
	return resp.ID, networkID, sshPort, nil
}

// buildEnv constructs the environment variable slice for a session container.
func buildEnv(opts StartContainerOpts) []string {
	pw := os.Getenv("PEER_COMPUTE_SESSION_ROOT_PASSWORD")
	if pw == "" {
		pw = "peercompute"
	}
	env := []string{
		"PEER_COMPUTE_SESSION_ROOT_PASSWORD=" + pw,
	}
	if opts.SSHPublicKey != "" {
		env = append(env, "PEER_COMPUTE_SSH_PUBLIC_KEY="+opts.SSHPublicKey)
	}
	for k, v := range opts.ExtraEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// sessionContainerCmd returns a Debian/Ubuntu bootstrap command so plain ubuntu:* images
// stay up and run sshd. Set PEER_COMPUTE_SKIP_SSH_BOOTSTRAP=1 to use the image CMD only.
func sessionContainerCmd(imageName string) []string {
	if strings.TrimSpace(os.Getenv("PEER_COMPUTE_SKIP_SSH_BOOTSTRAP")) != "" {
		return nil
	}
	s := strings.ToLower(imageName)
	if !strings.Contains(s, "ubuntu") && !strings.Contains(s, "debian") {
		return nil
	}
	return []string{"/bin/bash", "-c", debianSSHBootstrap}
}

func containerStartExitErr(status string, exitCode int) error {
	return fmt.Errorf("container exited during startup (status=%s, exit_code=%d): "+
		"the image must keep running and listen on TCP 22 for SSH. "+
		"Official ubuntu/debian images often exit immediately (default shell, no sshd). "+
		"For ubuntu/debian we bootstrap OpenSSH unless PEER_COMPUTE_SKIP_SSH_BOOTSTRAP=1; "+
		"otherwise use an image that runs sshd, or set a blocking CMD", status, exitCode)
}

// StopContainer stops and removes a container, then cleans up its per-session network.
func (cr *ContainerRunner) StopContainer(ctx context.Context, containerID, sessionID string) error {
	if containerID == "" {
		return nil
	}

	short := containerID
	if len(short) > 12 {
		short = short[:12]
	}
	cr.log.Info("stopping container", "container_id", short)
	if err := cr.cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		cr.log.Warn("container stop warning", "container_id", short, "error", err)
	}
	if err := cr.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		cr.log.Warn("container remove warning", "container_id", short, "error", err)
	}

	// Clean up the per-session network after the container is removed
	if sessionID != "" {
		cr.removeSessionNetwork(ctx, sessionID)
	}

	return nil
}
