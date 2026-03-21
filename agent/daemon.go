package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	computev1 "github.com/OpenStruct/peer_compute/gen/compute/v1"
	"github.com/OpenStruct/peer_compute/internal/logging"
	"github.com/OpenStruct/peer_compute/internal/nat"
	"github.com/OpenStruct/peer_compute/internal/relay"
	"github.com/OpenStruct/peer_compute/internal/retry"
)

type Config struct {
	Name              string
	Address           string           // host:port advertised to renters
	Token             string           // API key for authenticated registration (optional)
	RegistryConn      *grpc.ClientConn // used when Client is nil (gRPC mode)
	Client            RegistryClient   // if set, used directly (REST mode)
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	CPUCores          uint32
	MemoryMB          uint64
	DiskGB            uint64
	GPUCount          uint32
	GPUModel          string
	WGListenPort      int    // base port for WireGuard tunnels
	STUNAddr          string // registry STUN endpoint (e.g. registry:3478)
	RelayAddr         string // registry relay endpoint (e.g. registry:3479)
}

type Daemon struct {
	cfg      Config
	client   RegistryClient
	runner   *ContainerRunner
	provider *computev1.Provider
	token    string
	log      *slog.Logger
	compat   bool
	modeNote string

	mu       sync.Mutex
	sessions map[string]*runningSession // sessionID -> running state
}

type runningSession struct {
	containerID string
	wgKeyPair   *WGKeyPair
	wgConfPath  string
	relayCancel context.CancelFunc // nil if direct/holepunched
	connMode    string             // "direct", "holepunch", "relay"
}

func NewDaemon(cfg Config) (*Daemon, error) {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.WGListenPort == 0 {
		cfg.WGListenPort = 51820
	}

	log := logging.New("agent")

	runner, err := NewContainerRunner(log.With("sub", "docker"))
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}

	client := cfg.Client
	if client == nil {
		client = NewGRPCClient(cfg.RegistryConn)
	}

	compat := false
	modeNote := ""
	if !HasWGQuick() {
		compat = true
		modeNote = "wg-quick not found"
	} else if !IsRoot() {
		compat = true
		modeNote = "insufficient privileges for wg-quick"
	}

	return &Daemon{
		cfg:      cfg,
		client:   client,
		runner:   runner,
		sessions: make(map[string]*runningSession),
		log:      log,
		compat:   compat,
		modeNote: modeNote,
	}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	if err := d.register(ctx); err != nil {
		return err
	}
	d.log.Info("registered as provider", "name", d.provider.Name, "id", d.provider.Id)
	if d.compat {
		d.log.Info("running in compatibility mode", "reason", d.modeNote)
	}

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
			GpuCount: d.cfg.GPUCount,
			GpuModel: d.cfg.GPUModel,
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

			var resp *computev1.HeartbeatResponse
			err := retry.Do(ctx, retry.Config{
				MaxAttempts: 3, BaseDelay: 1 * time.Second,
				MaxDelay: 5 * time.Second, Jitter: 0.3,
			}, func(ctx context.Context) error {
				var rpcErr error
				resp, rpcErr = d.client.Heartbeat(ctx, &computev1.HeartbeatRequest{
					ProviderId:     d.provider.Id,
					Available:      d.provider.Available,
					ActiveSessions: activeIDs,
				})
				return rpcErr
			})
			if err != nil {
				d.log.Warn("heartbeat failed after retries", "error", err)
				continue
			}

			for _, sid := range resp.TerminateSessions {
				go d.stopSession(context.Background(), sid)
			}

			d.log.Debug("heartbeat ok", "active_sessions", len(activeIDs))
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
			var resp *computev1.ListProviderSessionsResponse
			err := retry.Do(ctx, retry.Config{
				MaxAttempts: 3, BaseDelay: 500 * time.Millisecond,
				MaxDelay: 3 * time.Second, Jitter: 0.3,
			}, func(ctx context.Context) error {
				var rpcErr error
				resp, rpcErr = d.client.ListProviderSessions(ctx, &computev1.ListProviderSessionsRequest{
					ProviderId:   d.provider.Id,
					StatusFilter: "pending",
				})
				return rpcErr
			})
			if err != nil {
				d.log.Warn("session poll failed after retries", "error", err)
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
	sid := sess.Id[:8]
	d.log.Info("starting session", "session_id", sid, "image", sess.Image)

	// Reserve session slot immediately to avoid duplicate concurrent starts while
	// this startup path performs network and container operations.
	d.mu.Lock()
	if _, exists := d.sessions[sess.Id]; exists {
		d.mu.Unlock()
		d.log.Debug("session already tracked, skipping duplicate start", "session_id", sid)
		return
	}
	rs := &runningSession{}
	d.sessions[sess.Id] = rs
	d.mu.Unlock()

	cleanupFailedStart := func() {
		d.mu.Lock()
		delete(d.sessions, sess.Id)
		d.mu.Unlock()
		if rs.relayCancel != nil {
			rs.relayCancel()
		}
		if rs.containerID != "" {
			if err := d.runner.StopContainer(ctx, rs.containerID); err != nil {
				d.log.Warn("failed to stop container after startup failure", "session_id", sid, "error", err)
			}
		}
		if rs.wgConfPath != "" {
			_ = WGDown(ctx, rs.wgConfPath, d.log)
		}
		_ = RemoveConfig(sess.Id)
	}

	// 1. Start Docker container
	containerID, sshPort, err := d.runner.StartContainer(ctx, sess.Id, sess.Allocated, sess.Image)
	if err != nil {
		d.log.Error("container start failed", "session_id", sid, "error", err)
		cleanupFailedStart()
		d.updateStatus(ctx, sess.Id, "terminated", "", "", "")
		return
	}
	rs.containerID = containerID

	// 2. Generate WireGuard keypair
	kp, err := GenerateKeyPair()
	if err != nil {
		d.log.Error("keygen failed", "session_id", sid, "error", err)
		cleanupFailedStart()
		d.updateStatus(ctx, sess.Id, "terminated", "", "", "")
		return
	}
	rs.wgKeyPair = kp

	// Derive WG port from per-session tunnel index for deterministic, collision-free allocation
	wgPort := d.cfg.WGListenPort + parseTunnelIndex(sess.GetWgProviderIp())

	// 3-6. Connectivity mode negotiation.
	var exchResp *computev1.ExchangeCandidatesResponse
	var peerEndpoint string
	var connMode string

	if d.compat {
		connMode = "compat"
	} else {
		// 3. Gather endpoint candidates (local + STUN)
		candidates, err := nat.GatherCandidates(ctx, d.cfg.STUNAddr, wgPort, d.log)
		if err != nil {
			d.log.Warn("candidate gathering failed", "session_id", sid, "error", err)
		}

		// Convert to proto candidates
		protoCandidates := make([]*computev1.EndpointCandidate, len(candidates))
		for i, c := range candidates {
			protoCandidates[i] = &computev1.EndpointCandidate{
				Address:  c.Address,
				Type:     c.Type,
				Priority: c.Priority,
			}
		}

		// 4. Exchange candidates with the renter via registry
		exchResp, err = d.client.ExchangeCandidates(ctx, &computev1.ExchangeCandidatesRequest{
			SessionId:   sess.Id,
			PeerId:      d.provider.Id,
			Candidates:  protoCandidates,
			WgPublicKey: kp.PublicKey,
		})
		if err != nil {
			d.log.Warn("candidate exchange failed", "session_id", sid, "error", err)
			exchResp = &computev1.ExchangeCandidatesResponse{}
		}

		peerWGKey := exchResp.GetPeerWgPublicKey()

		// 5. Attempt hole-punching if we have peer candidates
		if len(exchResp.GetPeerCandidates()) > 0 {
			remoteCandidates := make([]nat.Candidate, len(exchResp.PeerCandidates))
			for i, c := range exchResp.PeerCandidates {
				remoteCandidates[i] = nat.Candidate{
					Address:  c.Address,
					Type:     c.Type,
					Priority: c.Priority,
				}
			}

			addr, err := nat.Probe(ctx, wgPort, remoteCandidates, 10*time.Second, d.log)
			if err == nil {
				peerEndpoint = addr
				connMode = "holepunch"
				d.log.Info("hole punch succeeded", "session_id", sid, "endpoint", addr)
			} else if errors.Is(err, nat.ErrHolePunchFailed) {
				d.log.Info("hole punch failed, falling back to relay", "session_id", sid)
			}
		}

		// 6. Relay fallback
		if connMode == "" && exchResp.GetRelayAddress() != "" {
			proxyPort := wgPort + 10000 // offset to avoid collision
			rc, err := relay.NewRelayClient(exchResp.RelayAddress, exchResp.RelayToken, proxyPort, wgPort, d.log)
			if err != nil {
				d.log.Error("relay client failed", "session_id", sid, "error", err)
			} else {
				relayCtx, cancel := context.WithCancel(ctx)
				rs.relayCancel = cancel
				go rc.Run(relayCtx)

				peerEndpoint = fmt.Sprintf("127.0.0.1:%d", proxyPort)
				connMode = "relay"

				// Tell registry to activate relay
				retry.Do(ctx, retry.DefaultConfig(), func(ctx context.Context) error {
					_, err := d.client.ReportConnectionResult(ctx, &computev1.ReportConnectionResultRequest{
						SessionId: sess.Id,
						PeerId:    d.provider.Id,
						UseRelay:  true,
					})
					return err
				})

				d.log.Info("relay active", "session_id", sid, "relay_addr", exchResp.RelayAddress)
			}
		}

		if connMode == "" {
			connMode = "direct"
			peerEndpoint = ""
		}

		// 7. Write and activate WireGuard config.
		providerIP := sess.GetWgProviderIp()
		renterIP := sess.GetWgRenterIp()
		if providerIP == "" {
			providerIP = "10.99.0.1" // fallback for backward compat
		}
		if renterIP == "" {
			renterIP = "10.99.0.2"
		}

		confPath, err := WriteConfig(sess.Id, WGConfig{
			PrivateKey:    kp.PrivateKey,
			TunnelIP:      providerIP,
			ListenPort:    wgPort,
			PeerPublicKey: peerWGKey,
			PeerIP:        renterIP,
			PeerEndpoint:  peerEndpoint,
		})
		if err != nil {
			d.log.Error("wireguard config failed", "session_id", sid, "error", err)
		}

		if confPath != "" {
			rs.wgConfPath = confPath
			if _, err := WGUp(ctx, confPath, d.log); err != nil {
				d.log.Warn("wireguard auto-activation failed", "session_id", sid, "error", err)
				// Fall back to compatibility mode if WG cannot be activated.
				connMode = "compat"
			}
		}
	}

	// 8. Mark mode on tracked session
	d.mu.Lock()
	if current, ok := d.sessions[sess.Id]; ok {
		current.connMode = connMode
	}
	d.mu.Unlock()

	// 9. Report status to registry
	sshEndpoint := fmt.Sprintf("%s:%s", d.cfg.Address, sshPort)
	wgEndpoint := ""
	if connMode != "compat" {
		wgEndpoint = fmt.Sprintf("%s:%d", d.cfg.Address, wgPort)
	}
	d.updateStatus(ctx, sess.Id, "running", containerID, sshEndpoint, wgEndpoint)

	// Report connection result if holepunch
	if connMode == "holepunch" {
		retry.Do(ctx, retry.DefaultConfig(), func(ctx context.Context) error {
			_, err := d.client.ReportConnectionResult(ctx, &computev1.ReportConnectionResultRequest{
				SessionId:        sess.Id,
				PeerId:           d.provider.Id,
				SelectedEndpoint: peerEndpoint,
			})
			return err
		})
	}

	d.log.Info("session running", "session_id", sid, "mode", connMode,
		"container", containerID[:12], "ssh", sshEndpoint, "wg", wgEndpoint)
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

	d.log.Info("stopping session", "session_id", sessionID[:8])

	if rs.relayCancel != nil {
		rs.relayCancel()
	}

	if err := d.runner.StopContainer(ctx, rs.containerID); err != nil {
		d.log.Warn("stop container failed", "session_id", sessionID[:8], "error", err)
	}

	// Deactivate WireGuard tunnel before removing config
	if rs.wgConfPath != "" {
		if err := WGDown(ctx, rs.wgConfPath, d.log); err != nil {
			d.log.Warn("wireguard deactivation failed", "session_id", sessionID[:8], "error", err)
		}
	}

	RemoveConfig(sessionID)
	d.updateStatus(ctx, sessionID, "terminated", "", "", "")
}

func (d *Daemon) updateStatus(ctx context.Context, sessionID, status, containerID, sshEndpoint, wgEndpoint string) {
	req := &computev1.UpdateSessionStatusRequest{
		SessionId:   sessionID,
		Status:      status,
		ContainerId: containerID,
		SshEndpoint: sshEndpoint,
		WgEndpoint:  wgEndpoint,
	}

	err := retry.Do(ctx, retry.Config{
		MaxAttempts: 3, BaseDelay: 500 * time.Millisecond,
		MaxDelay: 5 * time.Second, Jitter: 0.3,
	}, func(ctx context.Context) error {
		_, err := d.client.UpdateSessionStatus(ctx, req)
		return err
	})
	if err != nil {
		d.log.Warn("status update failed after retries", "session_id", sessionID[:8], "error", err)
	}
}

func (d *Daemon) activeSessionCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

// parseTunnelIndex extracts the third octet from a tunnel IP (e.g. "10.99.3.1" -> 3).
// Returns 0 if the IP is empty or malformed, which causes the WG port to equal the base port.
func parseTunnelIndex(ip string) int {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[2])
	return n
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
