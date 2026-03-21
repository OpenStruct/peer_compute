package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
)

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

// StartContainer pulls the image and runs a container with resource limits.
// Returns the container ID and the host-mapped SSH port.
func (cr *ContainerRunner) StartContainer(ctx context.Context, sessionID string, res *computev1.Resources, imageName string) (containerID string, sshPort string, err error) {
	// Pull image
	cr.log.Debug("pulling image", "image", imageName)
	reader, err := cr.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return "", "", fmt.Errorf("image pull: %w", err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	// Resource limits
	var nanoCPUs int64
	var memBytes int64
	if res != nil {
		nanoCPUs = int64(res.CpuCores) * 1e9
		memBytes = int64(res.MemoryMb) * 1024 * 1024
	}

	// Expose port 22 for SSH
	containerCfg := &container.Config{
		Image: imageName,
		ExposedPorts: nat.PortSet{
			"22/tcp": struct{}{},
		},
		Labels: map[string]string{
			"peer-compute.session": sessionID,
		},
	}

	hostCfg := &container.HostConfig{
		Resources: container.Resources{
			NanoCPUs: nanoCPUs,
			Memory:   memBytes,
		},
		PortBindings: nat.PortMap{
			"22/tcp": []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: ""}, // auto-assign
			},
		},
	}

	resp, err := cr.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "pc-"+sessionID[:12])
	if err != nil {
		return "", "", fmt.Errorf("container create: %w", err)
	}

	if err := cr.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", "", fmt.Errorf("container start: %w", err)
	}

	// Get the assigned host port. Docker can take a brief moment to publish
	// the mapping after container start.
	for i := 0; i < 20; i++ {
		inspect, err := cr.cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			return resp.ID, "", fmt.Errorf("container inspect: %w", err)
		}
		if inspect.State != nil && !inspect.State.Running {
			return resp.ID, "", fmt.Errorf("container exited during startup (status=%s, exit_code=%d)", inspect.State.Status, inspect.State.ExitCode)
		}
		bindings := inspect.NetworkSettings.Ports["22/tcp"]
		if len(bindings) > 0 && bindings[0].HostPort != "" {
			sshPort = bindings[0].HostPort
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cr.log.Info("started container", "container_id", resp.ID[:12], "ssh_port", sshPort)
	return resp.ID, sshPort, nil
}

// StopContainer stops and removes a container.
func (cr *ContainerRunner) StopContainer(ctx context.Context, containerID string) error {
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
	return cr.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}
