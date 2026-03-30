package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/server"
	"github.com/sei-protocol/seictl/sidecar/tasks"
	"github.com/sei-protocol/seilog"
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
		defer func() { _ = seilog.Close() }()

		ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
		defer stop()

		homeDir := destinations.home
		if homeDir == "" {
			homeDir = "/sei"
		}
		port := cmd.String("port")
		chainID := os.Getenv("SEI_CHAIN_ID")

		if err := tasks.EnsureDefaultConfig(homeDir); err != nil {
			return fmt.Errorf("home directory init failed: %w", err)
		}

		store, err := engine.NewSQLiteStore(filepath.Join(homeDir, "sidecar.db"))
		if err != nil {
			return fmt.Errorf("open result store: %w", err)
		}

		handlers := map[engine.TaskType]engine.TaskHandler{
			engine.TaskSnapshotRestore:          tasks.NewSnapshotRestorer(homeDir, nil).Handler(),
			engine.TaskDiscoverPeers:            tasks.NewPeerDiscoverer(homeDir, nil, nil).Handler(),
			engine.TaskConfigPatch:              tasks.NewConfigPatcher(homeDir).Handler(),
			engine.TaskConfigApply:              tasks.NewConfigApplier(homeDir).Handler(),
			engine.TaskConfigValidate:           tasks.NewConfigValidator(homeDir).Handler(),
			engine.TaskConfigReload:             tasks.NewConfigReloader(homeDir).Handler(),
			engine.TaskMarkReady:                tasks.MarkReadyHandler(),
			engine.TaskConfigureGenesis:         tasks.NewGenesisFetcher(homeDir, chainID, nil).Handler(),
			engine.TaskConfigureStateSync:       tasks.NewStateSyncConfigurer(homeDir, nil).Handler(),
			engine.TaskSnapshotUpload:           tasks.NewSnapshotUploader(homeDir, nil).Handler(),
			engine.TaskResultExport:             tasks.NewResultExporter(homeDir, nil).Handler(),
			engine.TaskAwaitCondition:           tasks.NewConditionWaiter(nil).Handler(),
			engine.TaskGenerateIdentity:         tasks.NewIdentityGenerator(homeDir, nil).Handler(),
			engine.TaskGenerateGentx:            tasks.NewGentxGenerator(homeDir, nil).Handler(),
			engine.TaskUploadGenesisArtifacts:   tasks.NewGenesisArtifactUploader(homeDir, nil).Handler(),
			engine.TaskAssembleAndUploadGenesis: tasks.NewGenesisAssembler(homeDir, nil, nil, nil).Handler(),
			engine.TaskSetGenesisPeers:          tasks.NewGenesisPeersSetter(homeDir, nil).Handler(),
		}

		eng := engine.NewEngine(ctx, handlers, store)

		go runSchedulerTicker(ctx, eng)

		srv := server.NewServer(":"+port, eng, homeDir)
		srvErr := srv.ListenAndServe(ctx)

		// Give in-flight task goroutines a grace period to complete
		// their final store writes after context cancellation.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		eng.Shutdown(shutdownCtx)

		if closeErr := store.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "warn: result store close: %v\n", closeErr)
		}

		if srvErr != nil && !errors.Is(srvErr, context.Canceled) {
			return fmt.Errorf("server error: %w", srvErr)
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
