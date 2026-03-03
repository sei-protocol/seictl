package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sei-protocol/seictl/internal/patch"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/tasks/defaults"
)

// PatchSet describes the TOML patches to apply to config.toml and app.toml.
type PatchSet struct {
	Peers    []string
	NodeMode string

	// SnapshotGeneration, when non-nil, configures app.toml for archival
	// pruning and periodic Tendermint state-sync snapshot production.
	SnapshotGeneration *SnapshotGenerationPatch
}

// SnapshotGenerationPatch holds the app.toml values needed to produce
// Tendermint state-sync snapshots.
type SnapshotGenerationPatch struct {
	Interval   int64
	KeepRecent int32
}

// ConfigPatcher applies TOML patches to seid configuration files.
type ConfigPatcher struct {
	homeDir string
}

// NewConfigPatcher creates a patcher targeting the given home directory.
func NewConfigPatcher(homeDir string) *ConfigPatcher {
	return &ConfigPatcher{homeDir: homeDir}
}

// Handler returns an engine.TaskHandler that adapts map[string]any params
// to a typed PatchSet and delegates to PatchConfig.
func (p *ConfigPatcher) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		ps, err := parsePatchSet(params)
		if err != nil {
			return err
		}
		return p.PatchConfig(ctx, ps)
	}
}

// PatchConfig reads config.toml (and app.toml when snapshot generation is
// configured), applies the patch set, and writes atomically.
// If no peers are in the patch set, it reads from the peers file written by
// discover-peers (if it exists).
func (p *ConfigPatcher) PatchConfig(_ context.Context, ps PatchSet) error {
	if len(ps.Peers) == 0 {
		if filePeers, err := ReadPeersFile(p.homeDir); err == nil && len(filePeers) > 0 {
			ps.Peers = filePeers
		}
	}

	configPath := filepath.Join(p.homeDir, "config", "config.toml")

	doc, err := patch.ReadTOML(configPath)
	if err != nil {
		return fmt.Errorf("parsing config.toml: %w", err)
	}

	if len(ps.Peers) > 0 {
		patch.SetNestedValue(doc, "p2p", "persistent-peers", strings.Join(ps.Peers, ","))
	}
	if ps.NodeMode != "" {
		patch.SetNestedValue(doc, "base", "mode", ps.NodeMode)
	}

	if ssCfg, err := ReadStateSyncFile(p.homeDir); err == nil {
		patch.SetNestedValue(doc, "statesync", "enable", true)
		patch.SetNestedValue(doc, "statesync", "trust-height", ssCfg.TrustHeight)
		patch.SetNestedValue(doc, "statesync", "trust-hash", ssCfg.TrustHash)
		patch.SetNestedValue(doc, "statesync", "rpc-servers", ssCfg.RpcServers)
	}

	if err := patch.WriteTOML(configPath, doc); err != nil {
		return err
	}

	if ps.SnapshotGeneration != nil {
		if err := p.patchAppTOML(ps.SnapshotGeneration); err != nil {
			return fmt.Errorf("patching app.toml for snapshot generation: %w", err)
		}
	}

	return nil
}

// patchAppTOML configures app.toml for archival pruning and snapshot
// production. This sets:
//   - pruning = "nothing"           (retain all state)
//   - snapshot-interval = <interval>
//   - snapshot-keep-recent = <keepRecent>
func (p *ConfigPatcher) patchAppTOML(sg *SnapshotGenerationPatch) error {
	appPath := filepath.Join(p.homeDir, "config", "app.toml")

	doc, err := patch.ReadTOML(appPath)
	if err != nil {
		return fmt.Errorf("parsing app.toml: %w", err)
	}

	doc["pruning"] = "nothing"
	doc["snapshot-interval"] = sg.Interval
	doc["snapshot-keep-recent"] = int64(sg.KeepRecent)

	return patch.WriteTOML(appPath, doc)
}

func parsePatchSet(params map[string]any) (PatchSet, error) {
	var ps PatchSet

	if raw, ok := params["peers"]; ok {
		switch v := raw.(type) {
		case []string:
			ps.Peers = v
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return PatchSet{}, fmt.Errorf("config-patch: peers must be a list of strings")
				}
				ps.Peers = append(ps.Peers, s)
			}
		default:
			return PatchSet{}, fmt.Errorf("config-patch: peers must be a list of strings")
		}
	}

	if v, ok := params["nodeMode"].(string); ok {
		ps.NodeMode = v
	}

	if raw, ok := params["snapshotGeneration"].(map[string]any); ok {
		sg := &SnapshotGenerationPatch{}
		if v, ok := toInt64(raw["interval"]); ok {
			sg.Interval = v
		}
		if v, ok := toInt32(raw["keepRecent"]); ok {
			sg.KeepRecent = v
		}
		ps.SnapshotGeneration = sg
	}

	return ps, nil
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func toInt32(v any) (int32, bool) {
	switch n := v.(type) {
	case int32:
		return n, true
	case int:
		return int32(n), true
	case int64:
		return int32(n), true
	case float64:
		return int32(n), true
	default:
		return 0, false
	}
}


// EnsureDefaultConfig creates the seid home directory structure and writes a
// minimal default config.toml if one does not already exist. The default is
// embedded from defaults/config.toml.
func EnsureDefaultConfig(homeDir string) error {
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
		return nil
	}

	defaultConfig, err := defaults.FS.ReadFile("config.toml")
	if err != nil {
		return fmt.Errorf("reading embedded default config: %w", err)
	}

	if err := os.WriteFile(configPath, defaultConfig, 0o644); err != nil {
		return fmt.Errorf("writing default config.toml: %w", err)
	}

	return nil
}

