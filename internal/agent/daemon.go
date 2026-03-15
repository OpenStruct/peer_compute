package agent

import (
	"context"
	"log"
	"time"

	"google.golang.org/grpc"

	computev1 "peer-compute/gen/compute/v1"
)

type Config struct {
	Name              string
	Address           string
	RegistryConn      *grpc.ClientConn
	HeartbeatInterval time.Duration
	CPUCores          uint32
	MemoryMB          uint64
	DiskGB            uint64
}

type Daemon struct {
	cfg      Config
	client   computev1.RegistryServiceClient
	provider *computev1.Provider
	token    string
}

func NewDaemon(cfg Config) (*Daemon, error) {
	return &Daemon{
		cfg:    cfg,
		client: computev1.NewRegistryServiceClient(cfg.RegistryConn),
	}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	if err := d.register(ctx); err != nil {
		return err
	}
	log.Printf("registered as provider %s (id=%s)", d.provider.Name, d.provider.Id)

	return d.heartbeatLoop(ctx)
}

func (d *Daemon) register(ctx context.Context) error {
	resp, err := d.client.RegisterProvider(ctx, &computev1.RegisterProviderRequest{
		Name:    d.cfg.Name,
		Address: d.cfg.Address,
		Capacity: &computev1.Resources{
			CpuCores: d.cfg.CPUCores,
			MemoryMb: d.cfg.MemoryMB,
			DiskGb:   d.cfg.DiskGB,
		},
	})
	if err != nil {
		return err
	}

	d.provider = resp.Provider
	d.token = resp.Token
	return nil
}

func (d *Daemon) heartbeatLoop(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("stopping heartbeat")
			return nil
		case <-ticker.C:
			_, err := d.client.Heartbeat(ctx, &computev1.HeartbeatRequest{
				ProviderId: d.provider.Id,
				Available:  d.provider.Available,
			})
			if err != nil {
				log.Printf("heartbeat failed: %v", err)
				continue
			}
			log.Println("heartbeat ok")
		}
	}
}
