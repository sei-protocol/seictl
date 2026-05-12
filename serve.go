package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/server"
	"github.com/sei-protocol/seictl/sidecar/tasks"
	"github.com/sei-protocol/seilog"
	"github.com/urfave/cli/v3"
)

var serveLog = seilog.NewLogger("seictl", "serve")

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
		genesisBucket := os.Getenv("SEI_GENESIS_BUCKET")
		genesisRegion := os.Getenv("SEI_GENESIS_REGION")
		snapshotBucket := os.Getenv("SEI_SNAPSHOT_BUCKET")
		snapshotRegion := os.Getenv("SEI_SNAPSHOT_REGION")

		podName := os.Getenv("HOSTNAME")
		if podName == "" {
			if h, err := os.Hostname(); err == nil {
				podName = h
			}
		}
		if podName == "" {
			podName = "unknown"
		}

		for _, kv := range []struct{ name, val string }{
			{"SEI_CHAIN_ID", chainID},
			{"SEI_GENESIS_BUCKET", genesisBucket},
			{"SEI_GENESIS_REGION", genesisRegion},
			{"SEI_SNAPSHOT_BUCKET", snapshotBucket},
			{"SEI_SNAPSHOT_REGION", snapshotRegion},
		} {
			if kv.val == "" {
				return fmt.Errorf("required environment variable %s is not set", kv.name)
			}
		}

		var snapshotUploadInterval time.Duration
		if raw := os.Getenv("SEI_SNAPSHOT_UPLOAD_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("invalid SEI_SNAPSHOT_UPLOAD_INTERVAL %q: %w", raw, err)
			}
			snapshotUploadInterval = parsed
		}

		execCfg, err := buildExecutionConfig(homeDir)
		if err != nil {
			return err
		}

		if err := tasks.EnsureDefaultConfig(homeDir); err != nil {
			return fmt.Errorf("home directory init failed: %w", err)
		}

		store, err := engine.NewSQLiteStore(filepath.Join(homeDir, "sidecar.db"))
		if err != nil {
			return fmt.Errorf("open result store: %w", err)
		}

		snapshotRestorer, err := tasks.NewSnapshotRestorer(homeDir, snapshotBucket, snapshotRegion, chainID, nil, nil)
		if err != nil {
			return fmt.Errorf("creating snapshot restorer: %w", err)
		}

		snapshotUploader, err := tasks.NewSnapshotUploader(homeDir, snapshotBucket, snapshotRegion, chainID, snapshotUploadInterval, nil)
		if err != nil {
			return fmt.Errorf("creating snapshot uploader: %w", err)
		}

		handlers := map[engine.TaskType]engine.TaskHandler{
			engine.TaskSnapshotRestore:          snapshotRestorer.Handler(),
			engine.TaskDiscoverPeers:            tasks.NewPeerDiscoverer(homeDir, nil, nil).Handler(),
			engine.TaskConfigPatch:              tasks.NewConfigPatcher(homeDir).Handler(),
			engine.TaskConfigApply:              tasks.NewConfigApplier(homeDir).Handler(),
			engine.TaskConfigValidate:           tasks.NewConfigValidator(homeDir).Handler(),
			engine.TaskConfigReload:             tasks.NewConfigReloader(homeDir).Handler(),
			engine.TaskMarkReady:                tasks.MarkReadyHandler(),
			engine.TaskConfigureGenesis:         tasks.NewGenesisFetcher(homeDir, chainID, genesisBucket, genesisRegion, nil).Handler(),
			engine.TaskConfigureStateSync:       tasks.NewStateSyncConfigurer(homeDir, nil).Handler(),
			engine.TaskSnapshotUpload:           snapshotUploader.Handler(),
			engine.TaskResultExport:             tasks.NewResultExporter(homeDir, chainID, podName, nil).Handler(),
			engine.TaskAwaitCondition:           tasks.NewConditionWaiter(nil).Handler(),
			engine.TaskGenerateIdentity:         tasks.NewIdentityGenerator(homeDir).Handler(),
			engine.TaskGenerateGentx:            tasks.NewGentxGenerator(homeDir).Handler(),
			engine.TaskUploadGenesisArtifacts:   tasks.NewGenesisArtifactUploader(homeDir, genesisBucket, genesisRegion, chainID, nil).Handler(),
			engine.TaskAssembleAndUploadGenesis: tasks.NewGenesisAssembler(homeDir, genesisBucket, genesisRegion, chainID, nil, nil).Handler(),
			engine.TaskSetGenesisPeers:          tasks.NewGenesisPeersSetter(homeDir, genesisBucket, genesisRegion, chainID, nil).Handler(),
		}

		eng := engine.NewEngine(ctx, handlers, store)
		eng.Config = execCfg

		srv := server.NewServer(":"+port, eng, homeDir)
		srvErr := srv.ListenAndServe(ctx)

		if closeErr := store.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "warn: result store close: %v\n", closeErr)
		}

		if srvErr != nil && !errors.Is(srvErr, context.Canceled) {
			return fmt.Errorf("server error: %w", srvErr)
		}
		return nil
	},
}

// buildExecutionConfig reads the keyring-related envs, opens the keyring
// (when configured), and wipes the passphrase from the process env so it
// no longer appears in /proc/<pid>/environ. The keyring is left nil when
// SEI_KEYRING_BACKEND is unset — governance signing is opt-in and the
// sidecar boots normally without it; sign-tx tasks will reject calls
// with a clear "keyring not configured" error.
func buildExecutionConfig(homeDir string) (engine.ExecutionConfig, error) {
	// Read and wipe the passphrase before any other logic so every return
	// path leaves /proc/<pid>/environ clean — including early returns for
	// unset/unsupported backend and missing-passphrase checks.
	passphrase := os.Getenv("SEI_KEYRING_PASSPHRASE")
	_ = os.Unsetenv("SEI_KEYRING_PASSPHRASE")

	backend := os.Getenv("SEI_KEYRING_BACKEND")
	if backend == "" {
		return engine.ExecutionConfig{}, nil
	}

	if !slices.Contains(server.AllowedBackends, backend) {
		return engine.ExecutionConfig{}, fmt.Errorf(
			"unsupported SEI_KEYRING_BACKEND %q (allowed: test|file|os)", backend)
	}

	dir := os.Getenv("SEI_KEYRING_DIR")
	if dir == "" {
		dir = filepath.Join(homeDir, "keyring-file")
	}

	if backend == server.BackendFile && passphrase == "" {
		return engine.ExecutionConfig{}, fmt.Errorf(
			"SEI_KEYRING_PASSPHRASE required when SEI_KEYRING_BACKEND=file")
	}

	kr, err := server.OpenKeyring(backend, dir, passphrase)
	if err != nil {
		// OpenKeyring already redacted the passphrase from err.Error();
		// don't %w-wrap because that re-exposes any typed-field contents
		// of the underlying SDK error chain.
		return engine.ExecutionConfig{}, err
	}

	if err := server.SmokeTestKeyring(kr); err != nil {
		return engine.ExecutionConfig{}, err
	}

	serveLog.Info("keyring opened", "backend", backend, "dir", dir)
	return engine.ExecutionConfig{Keyring: kr}, nil
}
