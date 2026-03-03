package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sei-protocol/seictl/sei-sidecar/engine"
	"github.com/sei-protocol/seictl/sei-sidecar/server"
	"github.com/sei-protocol/seictl/sei-sidecar/tasks"
	"github.com/urfave/cli/v3"
)

var serveCmd = cli.Command{
	Name:  "serve",
	Usage: "Start the sidecar task executor and HTTP API",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "port",
			Sources: cli.EnvVars("SEI_SIDECAR_PORT"),
			Value:   "7777",
			Usage:   "Port for the sidecar HTTP API",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		homeDir := destinations.home
		if homeDir == "" {
			homeDir = "/sei"
		}
		port := cmd.String("port")

		if err := initSeiHome(homeDir); err != nil {
			return fmt.Errorf("home directory init failed: %w", err)
		}

		handlers := map[engine.TaskType]engine.TaskHandler{
			engine.TaskSnapshotRestore:    tasks.NewSnapshotRestorer(homeDir, nil).Handler(),
			engine.TaskDiscoverPeers:      tasks.NewPeerDiscoverer(homeDir, nil, nil).Handler(),
			engine.TaskConfigPatch:        tasks.NewConfigPatcher(homeDir).Handler(),
			engine.TaskMarkReady:          tasks.MarkReadyHandler(),
			engine.TaskConfigureGenesis:   tasks.NewGenesisFetcher(homeDir, nil).Handler(),
			engine.TaskConfigureStateSync: tasks.NewStateSyncConfigurer(homeDir, nil).Handler(),
			engine.TaskUpdatePeers:        tasks.UpdatePeersHandler(homeDir),
			engine.TaskSnapshotUpload:     tasks.NewSnapshotUploader(homeDir, nil).Handler(),
		}

		eng := engine.NewEngine(handlers)

		go runSchedulerTicker(ctx, eng)

		srv := server.NewServer(":"+port, eng)
		if err := srv.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	},
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

// initSeiHome creates the seid home directory structure and writes a minimal
// default config.toml.
func initSeiHome(homeDir string) error {
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

// defaultConfigTOML is a minimal config.toml that the config patcher can work with.
const defaultConfigTOML = `[base]
mode = "full"

[p2p]
persistent-peers = ""
laddr = "tcp://0.0.0.0:26656"

[statesync]
enable = false
trust-height = 0
trust-hash = ""
rpc-servers = ""

[consensus]
timeout-commit = "5s"

[mempool]
size = 5000
`
