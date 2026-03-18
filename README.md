# Peer Compute

Open-source peer-to-peer compute framework. Enables providers to share compute resources (CPU, memory, GPU) with renters over encrypted WireGuard tunnels.

## Features

- Provider registration and discovery via gRPC registry
- Automatic NAT traversal (STUN + UDP hole-punching + relay fallback)
- Encrypted tunnels using WireGuard
- Docker container orchestration for compute sessions
- Pluggable authentication, reputation, and storage backends
- CLI for both providers (agent) and renters (peerctl)

## Architecture

Peer Compute is built around three binaries that communicate over gRPC and UDP:

```
                    ┌─────────────────────────────┐
                    │          Registry            │
                    │  gRPC :50051                 │
                    │  STUN :3478  Relay :3479     │
                    └──────┬──────────────┬────────┘
                           │              │
              gRPC + STUN  │              │  gRPC + STUN
                           │              │
                    ┌──────▼──────┐ ┌─────▼───────┐
                    │    Agent    │ │   peerctl    │
                    │ (Provider)  │ │  (Renter)    │
                    └──────┬──────┘ └─────┬────────┘
                           │              │
                           └──── WireGuard ───┘
                            Encrypted Tunnel
```

- **Registry** -- Central coordination server handling provider discovery, STUN signaling, and relay fallback.
- **Agent** -- Runs on provider machines. Advertises available resources to the registry and manages Docker containers for compute sessions.
- **peerctl** -- CLI tool for renters to browse providers, create sessions, and connect to compute resources.

## Quick Start

### Prerequisites

- Go 1.25+
- Docker (for providers)
- WireGuard (optional, for tunnel connections)

### Build

```bash
make build
```

This produces three binaries in `bin/`:

- `bin/registry`
- `bin/agent`
- `bin/peerctl`

### Start the Registry

```bash
./bin/registry
```

Defaults: gRPC on `:50051`, STUN on `:3478`, Relay on `:3479`.

### Start a Provider

```bash
./bin/agent
```

The agent connects to the registry and advertises the machine's available compute resources.

### Rent Compute

List available providers:

```bash
./bin/peerctl providers
```

Create a session and connect:

```bash
./bin/peerctl session create --provider <id> --image ubuntu:22.04 --connect
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `PORT` | `50051` | gRPC server listen port |
| `STUN_PORT` | `3478` | STUN UDP listen port |
| `RELAY_PORT` | `3479` | Relay UDP listen port |
| `DATABASE_URL` | (none) | PostgreSQL connection string (optional, for persistent storage) |
| `REGISTRY_ADDR` | `localhost:50051` | Registry address for agent and peerctl to connect to |
| `PROVIDER_NAME` | hostname | Human-readable name for the provider |
| `PROVIDER_ADDR` | auto-detected | Public address the provider advertises to the registry |

## gRPC API

The registry exposes a single gRPC service with RPCs grouped by function:

### Provider Management

- `RegisterProvider` -- Register a new provider with resource information
- `UnregisterProvider` -- Remove a provider from the registry
- `Heartbeat` -- Periodic liveness signal from a provider
- `ListProviders` -- Query available providers with optional filters
- `GetProvider` -- Retrieve details for a specific provider

### Session Management

- `CreateSession` -- Request a compute session on a specific provider
- `EndSession` -- Terminate an active session
- `GetSession` -- Retrieve session status and metadata
- `ListSessions` -- List sessions for a provider or renter

### NAT Traversal

- `GetSTUNInfo` -- Retrieve STUN server information for NAT discovery
- `ExchangeCandidates` -- Exchange ICE-style connectivity candidates between peers
- `AllocateRelay` -- Request a relay allocation when direct connectivity fails

## Plugin System

Peer Compute is designed for extensibility through three core interfaces:

- **Authenticator** -- Controls access to registry RPCs. Implement custom authentication schemes (JWT, API keys, mTLS).
- **Reputation** -- Scores providers and renters based on session outcomes. Plug in custom scoring algorithms.
- **Store** -- Persists providers, sessions, and metadata. Swap the default in-memory store for any backend.

Inject custom implementations using `plugin.Bundle` and `serve.RegistryConfig`:

```go
package main

import (
    "github.com/OpenStruct/peer_compute/plugin"
    "github.com/OpenStruct/peer_compute/serve"
)

func main() {
    bundle := plugin.Bundle{
        Authenticator: &MyCustomAuth{},
        Reputation:    &MyCustomReputation{},
        Store:         &MyCustomStore{},
    }

    cfg := serve.RegistryConfig{
        GRPCPort:  50051,
        STUNPort:  3478,
        RelayPort: 3479,
        Plugins:   bundle,
    }

    serve.StartRegistry(cfg)
}
```

Any field left `nil` in the bundle falls back to the default in-memory implementation.

## Docker

```bash
docker compose up -d   # postgres + redis + registry
```

## Development

```bash
make proto       # regenerate protobuf
make build       # compile binaries
make test        # run tests
make lint        # run linter
```

## License

Apache 2.0. See [LICENSE](LICENSE) for details.
