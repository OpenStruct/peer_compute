package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/OpenStruct/peer_compute/agent"
)

func main() {
	registry := flag.String("registry", envOr("PCP_REGISTRY", "localhost:50051"), "Registry gRPC address")
	token := flag.String("token", envOr("PCP_TOKEN", ""), "API key for authentication (optional)")
	name := flag.String("name", envOr("PROVIDER_NAME", hostname()), "Provider display name")
	addr := flag.String("address", envOr("PROVIDER_ADDR", "localhost:50052"), "Advertised address for renters")
	cpuFlag := flag.Int("cpu", envInt("PROVIDER_CPU", 0), "CPU cores to offer (0 = auto-detect)")
	memFlag := flag.Int("memory", envInt("PROVIDER_MEMORY_MB", 0), "Memory in MB (0 = auto-detect)")
	diskFlag := flag.Int("disk", envInt("PROVIDER_DISK_GB", 0), "Disk in GB (0 = auto-detect)")
	gpuCountFlag := flag.Int("gpu-count", envInt("PROVIDER_GPU_COUNT", 0), "GPU count (0 = auto-detect)")
	gpuModelFlag := flag.String("gpu-model", envOr("PROVIDER_GPU_MODEL", ""), "GPU model override")
	flag.Parse()

	sys := agent.DetectResources()
	cpu := coalesce32(uint32(*cpuFlag), sys.CPUCores)
	mem := coalesce64(uint64(*memFlag), sys.MemoryMB)
	disk := coalesce64(uint64(*diskFlag), sys.DiskGB)
	gpuCount := uint32(*gpuCountFlag)
	gpuModel := *gpuModelFlag
	if *gpuCountFlag == 0 && sys.GPUCount > 0 {
		gpuCount = sys.GPUCount
		if gpuModel == "" {
			gpuModel = sys.GPUModel
		}
	}

	log.Printf("resources: cpu=%d memory=%dMB disk=%dGB gpu=%d(%s)",
		cpu, mem, disk, gpuCount, gpuModel)

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	if *token != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(&agent.TokenCredentials{Token: *token}))
	}

	conn, err := grpc.NewClient(*registry, dialOpts...)
	if err != nil {
		log.Fatalf("failed to connect to registry: %v", err)
	}
	defer conn.Close()

	registryHost := *registry
	if h, _, err := net.SplitHostPort(*registry); err == nil {
		registryHost = h
	}

	cfg := agent.Config{
		Name:              *name,
		Address:           *addr,
		Token:             *token,
		RegistryConn:      conn,
		HeartbeatInterval: 10 * time.Second,
		CPUCores:          cpu,
		MemoryMB:          mem,
		DiskGB:            disk,
		GPUCount:          gpuCount,
		GPUModel:          gpuModel,
		STUNAddr:          envOr("STUN_ADDR", registryHost+":3478"),
		RelayAddr:         envOr("RELAY_ADDR", registryHost+":3479"),
	}

	daemon, err := agent.NewDaemon(cfg)
	if err != nil {
		log.Fatalf("failed to create daemon: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Run(ctx); err != nil {
		log.Fatalf("daemon error: %v", err)
	}
}

func coalesce32(v, fallback uint32) uint32 {
	if v > 0 {
		return v
	}
	return fallback
}

func coalesce64(v, fallback uint64) uint64 {
	if v > 0 {
		return v
	}
	return fallback
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("invalid integer for %s: %q", key, v)
	}
	return n
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
