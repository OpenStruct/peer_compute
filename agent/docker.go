package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"

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

	// Get the assigned host port
	inspect, err := cr.cli.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return resp.ID, "", fmt.Errorf("container inspect: %w", err)
	}

	bindings := inspect.NetworkSettings.Ports["22/tcp"]
	if len(bindings) > 0 {
		sshPort = bindings[0].HostPort
	}

	cr.log.Info("started container", "container_id", resp.ID[:12], "ssh_port", sshPort)
	return resp.ID, sshPort, nil
}

// StopContainer stops and removes a container.
func (cr *ContainerRunner) StopContainer(ctx context.Context, containerID string) error {
	cr.log.Info("stopping container", "container_id", containerID[:12])
	if err := cr.cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		cr.log.Warn("container stop warning", "container_id", containerID[:12], "error", err)
	}
	return cr.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}
