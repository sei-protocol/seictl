package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/sei-protocol/platform/sei-sidecar/engine"
	"github.com/sei-protocol/platform/sei-sidecar/server"
	"github.com/sei-protocol/platform/sei-sidecar/tasks"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	homeDir := envOrDefault("SEI_HOME", "/sei")
	port := envOrDefault("SEI_SIDECAR_PORT", "7777")

	if err := initHomeDir(homeDir); err != nil {
		log.Fatalf("home directory init failed: %v", err)
	}

	// Wire the real signal implementation into the engine package.
	engine.SignalSeidFn = func(sig syscall.Signal) {
		if err := tasks.SignalSeid(sig); err != nil {
			log.Printf("failed to signal seid: %v", err)
		}
	}

	handlers := map[engine.TaskType]engine.TaskHandler{
		engine.TaskSnapshotRestore: tasks.NewSnapshotRestorer(homeDir, nil).Handler(),
		engine.TaskDiscoverPeers:   tasks.NewPeerDiscoverer(homeDir, nil, nil).Handler(),
		engine.TaskConfigPatch:     tasks.NewConfigPatcher(homeDir).Handler(),
		engine.TaskMarkReady:       tasks.MarkReadyHandler(),
		engine.TaskConfigureGenesis:   tasks.NewGenesisFetcher(homeDir, nil).Handler(),
		engine.TaskConfigureStateSync: tasks.NewStateSyncConfigurer(homeDir, nil).Handler(),
		engine.TaskUpdatePeers:        tasks.UpdatePeersHandler(homeDir),
		engine.TaskSnapshotUpload:     tasks.NewSnapshotUploader(homeDir, nil).Handler(),
	}

	eng := engine.NewEngine(homeDir, handlers, seidRPCBlockHeight)

	go runDrainTicker(ctx, eng)
	go runUpgradeTicker(ctx, eng)
	go runSchedulerTicker(ctx, eng)

	srv := server.NewServer(":"+port, eng)
	if err := srv.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("server error: %v", err)
	}
}

func runDrainTicker(ctx context.Context, eng *engine.Engine) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			eng.DrainUpdates()
			return
		case <-ticker.C:
			eng.DrainUpdates()
		}
	}
}

func runUpgradeTicker(ctx context.Context, eng *engine.Engine) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			eng.CheckUpgrades()
		}
	}
}

func runSchedulerTicker(ctx context.Context, eng *engine.Engine) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			eng.EvalSchedules()
		}
	}
}

// initHomeDir creates the seid home directory structure and writes a minimal
// default config.toml. This is the Go equivalent of `seid init` — it ensures
// the config patcher has a valid TOML file to work with on first boot.
func initHomeDir(homeDir string) error {
	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	dataDir := filepath.Join(homeDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	if _, err := os.Stat(configPath); err == nil {
		return nil // config already exists, skip
	}

	if err := os.WriteFile(configPath, []byte(defaultConfigTOML), 0o644); err != nil {
		return fmt.Errorf("writing default config.toml: %w", err)
	}

	return nil
}

// seidRPCBlockHeight queries seid's Tendermint RPC for the current block height.
func seidRPCBlockHeight() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:26657/status", nil)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET /status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading response: %w", err)
	}

	var status rpcStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return 0, fmt.Errorf("parsing /status response: %w", err)
	}

	height, err := strconv.ParseInt(status.Result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing block height %q: %w", status.Result.SyncInfo.LatestBlockHeight, err)
	}

	return height, nil
}

type rpcStatusResponse struct {
	Result struct {
		SyncInfo struct {
			LatestBlockHeight string `json:"latest_block_height"`
		} `json:"sync_info"`
	} `json:"result"`
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultConfigTOML is a minimal config.toml that the config patcher can work with.
// Fields are populated by the config-patch task during bootstrap.
const defaultConfigTOML = `[base]
mode = "full"

[p2p]
persistent_peers = ""
laddr = "tcp://0.0.0.0:26656"

[statesync]
enable = false
trust_height = 0
trust_hash = ""
rpc_servers = ""

[consensus]
timeout_commit = "5s"

[mempool]
size = 5000
`
