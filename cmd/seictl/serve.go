package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/server"
	"github.com/sei-protocol/seictl/sidecar/tasks"
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

		if err := tasks.EnsureDefaultConfig(homeDir); err != nil {
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

		eng := engine.NewEngine(ctx, handlers)

		go runSchedulerTicker(ctx, eng)

		srv := server.NewServer(":"+port, eng)
		if err := srv.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("server error: %w", err)
		}
		eng.Close()
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
