# Peer Compute

Rent compute from anyone's machine. An open-source, self-hostable peer-to-peer compute marketplace.

Providers share their CPU/GPU resources. Renters discover available machines and spin up sandboxed containers in seconds. Connections work across NATs and firewalls — no port forwarding required.

## How it works

```
Renter                      Registry                     Provider
  │                            │                            │
  │  1. Find a provider        │                            │
  │  peerctl providers         │                            │
  │<────────────────────────── │ ──────────────────────────>│
  │                            │         agent heartbeats   │
  │  2. Create a session       │                            │
  │  peerctl session create    │                            │
  │──────────────────────────> │ ──────────────────────────>│
  │                            │    agent picks up session, │
  │                            │    starts Docker container │
  │                            │                            │
  │  3. Connect                │                            │
  │  peerctl session connect   │                            │
  │──── STUN discover ───────>│<──── STUN discover ────────│
  │──── exchange candidates ──>│<── exchange candidates ────│
  │                            │                            │
  │  4. Tunnel established (WireGuard)                      │
  │<════════════════════════════════════════════════════════>│
  │         direct (hole-punch) or relayed                  │
```

## Quick start

**Prerequisites:** Go 1.23+, Docker, protoc

```bash
# Build everything
make build

# Terminal 1: Start the registry
./bin/registry

# Terminal 2: Start a provider
./bin/agent

# Terminal 3: Use compute
./bin/peerctl providers
./bin/peerctl session create --provider <id> --image ubuntu:22.04
./bin/peerctl session connect <session-id>
```

## Architecture

Three binaries, one repo:

| Binary | Role |
|--------|------|
| `registry` | Central coordination server. Brokers discovery, handles STUN/relay for NAT traversal. |
| `agent` | Provider daemon. Registers machine resources, runs sandboxed containers for renters. |
| `peerctl` | Renter CLI. Discover providers, create sessions, establish tunnels. |

### Networking

Connections between renter and provider work in three modes, tried in order:

1. **Direct** — Both on the same network or with open ports.
2. **Hole-punch** — UDP hole-punching via STUN discovery. Works for most NAT types.
3. **Relay** — Encrypted WireGuard traffic proxied through the registry. Fallback for symmetric NAT.

The registry runs three listeners:

| Port | Protocol | Purpose |
|------|----------|---------|
| 50051 | TCP | gRPC API |
| 3478 | UDP | STUN (endpoint discovery) |
| 3479 | UDP | Relay (NAT fallback) |

### Sandboxing

Provider containers run with:
- CPU limits (`--cpus`)
- Memory limits (`--memory`)
- Session labels for lifecycle tracking

## Project structure

```
cmd/
  registry/       Registry server entry point
  agent/          Provider agent entry point
  peerctl/        Renter CLI entry point
internal/
  registry/       gRPC server, session + provider store
  agent/          Docker runner, WireGuard tunnel, daemon loop
  cli/            Cobra commands (providers, session create/connect/get/terminate)
  nat/            STUN client/server, candidate gathering, UDP hole-punching
  relay/          UDP relay server + client for symmetric NAT fallback
proto/
  compute/v1/     Protobuf service definitions
```

## Configuration

All configuration via environment variables:

### Registry
| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `50051` | gRPC listen port |
| `STUN_PORT` | `3478` | STUN UDP port |
| `RELAY_PORT` | `3479` | Relay UDP port |

### Agent
| Variable | Default | Description |
|----------|---------|-------------|
| `REGISTRY_ADDR` | `localhost:50051` | Registry gRPC address |
| `PROVIDER_NAME` | hostname | Display name for this provider |
| `PROVIDER_ADDR` | `localhost:50052` | Advertised address |
| `STUN_ADDR` | `<registry>:3478` | STUN server address |
| `RELAY_ADDR` | `<registry>:3479` | Relay server address |

### CLI
```bash
peerctl --registry localhost:50051 providers
peerctl session create --provider <id> --image pytorch/pytorch --cpu 4 --memory 8192 --connect
peerctl session connect <session-id> --stun localhost:3478
peerctl session get <session-id>
peerctl session terminate <session-id>
```

## Development

```bash
# Generate protobuf code
make proto

# Build all binaries
make build

# Run tests
make test

# Start backing services (Postgres + Redis for future use)
make dev

# Lint
make lint
```

## Roadmap

- [ ] GPU support (NVIDIA Container Toolkit / MIG)
- [ ] PostgreSQL + Redis persistent store
- [ ] Reputation system for providers and renters
- [ ] Web UI (Next.js)
- [ ] `wg-quick` auto-activation
- [ ] Multi-session WireGuard (per-session tunnel IPs)
- [ ] Encrypted volume mounts for sensitive workloads
- [ ] NATS job queue for session assignment

## License

MIT
