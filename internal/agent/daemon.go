package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc"

	computev1 "peer-compute/gen/compute/v1"
)

type Config struct {
	Name              string
	Address           string // host:port advertised to renters
	RegistryConn      *grpc.ClientConn
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	CPUCores          uint32
	MemoryMB          uint64
	DiskGB            uint64
	WGListenPort      int // base port for WireGuard tunnels
}

type Daemon struct {
	cfg      Config
	client   computev1.RegistryServiceClient
	runner   *ContainerRunner
	provider *computev1.Provider
	token    string

	mu       sync.Mutex
	sessions map[string]*runningSession // sessionID -> running state
}

type runningSession struct {
	containerID string
	wgKeyPair   *WGKeyPair
	wgConfPath  string
}

func NewDaemon(cfg Config) (*Daemon, error) {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.WGListenPort == 0 {
		cfg.WGListenPort = 51820
	}

	runner, err := NewContainerRunner()
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}

	return &Daemon{
		cfg:      cfg,
		client:   computev1.NewRegistryServiceClient(cfg.RegistryConn),
		runner:   runner,
		sessions: make(map[string]*runningSession),
	}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	if err := d.register(ctx); err != nil {
		return err
	}
	log.Printf("registered as provider %s (id=%s)", d.provider.Name, d.provider.Id)

	// Run heartbeat and session poller concurrently
	errCh := make(chan error, 2)
	go func() { errCh <- d.heartbeatLoop(ctx) }()
	go func() { errCh <- d.sessionLoop(ctx) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		d.cleanup(context.Background())
		return nil
	}
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
			return nil
		case <-ticker.C:
			d.mu.Lock()
			activeIDs := make([]string, 0, len(d.sessions))
			for id := range d.sessions {
				activeIDs = append(activeIDs, id)
			}
			d.mu.Unlock()

			resp, err := d.client.Heartbeat(ctx, &computev1.HeartbeatRequest{
				ProviderId:     d.provider.Id,
				Available:      d.provider.Available,
				ActiveSessions: activeIDs,
			})
			if err != nil {
				log.Printf("heartbeat failed: %v", err)
				continue
			}

			// Handle termination requests from registry
			for _, sid := range resp.TerminateSessions {
				go d.stopSession(context.Background(), sid)
			}

			log.Printf("heartbeat ok (%d active sessions)", len(activeIDs))
		}
	}
}

func (d *Daemon) sessionLoop(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			resp, err := d.client.ListProviderSessions(ctx, &computev1.ListProviderSessionsRequest{
				ProviderId:   d.provider.Id,
				StatusFilter: "pending",
			})
			if err != nil {
				log.Printf("poll sessions failed: %v", err)
				continue
			}

			for _, sess := range resp.Sessions {
				d.mu.Lock()
				_, already := d.sessions[sess.Id]
				d.mu.Unlock()
				if already {
					continue
				}
				go d.startSession(ctx, sess)
			}
		}
	}
}

func (d *Daemon) startSession(ctx context.Context, sess *computev1.Session) {
	log.Printf("starting session %s (image=%s)", sess.Id[:8], sess.Image)

	// 1. Start Docker container
	containerID, sshPort, err := d.runner.StartContainer(ctx, sess.Id, sess.Allocated, sess.Image)
	if err != nil {
		log.Printf("session %s: container failed: %v", sess.Id[:8], err)
		d.updateStatus(ctx, sess.Id, "terminated", "", "", nil)
		return
	}

	// 2. Generate WireGuard keypair and config
	kp, err := GenerateKeyPair()
	if err != nil {
		log.Printf("session %s: keygen failed: %v", sess.Id[:8], err)
		d.runner.StopContainer(ctx, containerID)
		d.updateStatus(ctx, sess.Id, "terminated", "", "", nil)
		return
	}

	wgPort := d.cfg.WGListenPort + d.activeSessionCount()
	confPath, err := WriteConfig(sess.Id, WGConfig{
		PrivateKey:    kp.PrivateKey,
		TunnelIP:      "10.99.0.1",
		ListenPort:    wgPort,
		PeerPublicKey: sess.WgPublicKey,
		PeerIP:        "10.99.0.2",
	})
	if err != nil {
		log.Printf("session %s: wg config failed: %v", sess.Id[:8], err)
	}

	// 3. Track running session
	d.mu.Lock()
	d.sessions[sess.Id] = &runningSession{
		containerID: containerID,
		wgKeyPair:   kp,
		wgConfPath:  confPath,
	}
	d.mu.Unlock()

	// 4. Report status to registry
	sshEndpoint := fmt.Sprintf("%s:%s", d.cfg.Address, sshPort)
	wgEndpoint := fmt.Sprintf("%s:%d", d.cfg.Address, wgPort)
	d.updateStatus(ctx, sess.Id, "running", containerID, sshEndpoint, &wgInfo{
		publicKey: kp.PublicKey,
		endpoint:  wgEndpoint,
	})

	log.Printf("session %s running (container=%s, ssh=%s, wg=%s)",
		sess.Id[:8], containerID[:12], sshEndpoint, wgEndpoint)
}

func (d *Daemon) stopSession(ctx context.Context, sessionID string) {
	d.mu.Lock()
	rs, ok := d.sessions[sessionID]
	if !ok {
		d.mu.Unlock()
		return
	}
	delete(d.sessions, sessionID)
	d.mu.Unlock()

	log.Printf("stopping session %s", sessionID[:8])

	if err := d.runner.StopContainer(ctx, rs.containerID); err != nil {
		log.Printf("session %s: stop container failed: %v", sessionID[:8], err)
	}

	RemoveConfig(sessionID)
	d.updateStatus(ctx, sessionID, "terminated", "", "", nil)
}

type wgInfo struct {
	publicKey string
	endpoint  string
}

func (d *Daemon) updateStatus(ctx context.Context, sessionID, status, containerID, sshEndpoint string, wg *wgInfo) {
	req := &computev1.UpdateSessionStatusRequest{
		SessionId:   sessionID,
		Status:      status,
		ContainerId: containerID,
		SshEndpoint: sshEndpoint,
	}
	if wg != nil {
		req.WgPublicKey = wg.publicKey
		req.WgEndpoint = wg.endpoint
	}

	if _, err := d.client.UpdateSessionStatus(ctx, req); err != nil {
		log.Printf("session %s: status update failed: %v", sessionID[:8], err)
	}
}

func (d *Daemon) activeSessionCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

func (d *Daemon) cleanup(ctx context.Context) {
	d.mu.Lock()
	ids := make([]string, 0, len(d.sessions))
	for id := range d.sessions {
		ids = append(ids, id)
	}
	d.mu.Unlock()

	for _, id := range ids {
		d.stopSession(ctx, id)
	}

	if d.runner != nil {
		d.runner.Close()
	}
}
