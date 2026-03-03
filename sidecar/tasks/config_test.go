package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/internal/patch"
)

// setupConfigFile creates a config.toml in the expected path within homeDir.
func setupConfigFile(t *testing.T, homeDir, content string) string {
	t.Helper()
	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
	return configPath
}

func TestConfigPatcherSetsPeers(t *testing.T) {
	homeDir := t.TempDir()
	configPath := setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
laddr = "tcp://0.0.0.0:26656"
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{
		Peers: []string{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"},
	})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, configPath)
	p2p := doc["p2p"].(map[string]any)

	peers := p2p["persistent-peers"].(string)
	if peers != "abc@1.2.3.4:26656,def@5.6.7.8:26656" {
		t.Fatalf("expected comma-joined peers, got %q", peers)
	}

	// Verify laddr is preserved.
	laddr := p2p["laddr"].(string)
	if laddr != "tcp://0.0.0.0:26656" {
		t.Fatalf("expected laddr preserved, got %q", laddr)
	}
}

func TestConfigPatcherSetsStateSync(t *testing.T) {
	homeDir := t.TempDir()
	configPath := setupConfigFile(t, homeDir, `
[statesync]
enable = false
trust-height = 0
trust-hash = ""
rpc-servers = ""
`)

	// Write a state sync config file as configure-state-sync would.
	ssCfg := StateSyncConfig{
		TrustHeight: 100000,
		TrustHash:   "ABCDEF1234567890",
		RpcServers:  "1.2.3.4:26657,5.6.7.8:26657",
	}
	data, err := json.Marshal(ssCfg)
	if err != nil {
		t.Fatalf("marshaling state sync config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".sei-sidecar-statesync.json"), data, 0o644); err != nil {
		t.Fatalf("writing state sync file: %v", err)
	}

	patcher := NewConfigPatcher(homeDir)
	err = patcher.PatchConfig(context.Background(), PatchSet{})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, configPath)
	ss := doc["statesync"].(map[string]any)

	if ss["enable"] != true {
		t.Fatal("expected statesync.enable = true")
	}
	if ss["trust-height"] != int64(100000) {
		t.Fatalf("expected trust_height 100000, got %v", ss["trust-height"])
	}
	if ss["trust-hash"] != "ABCDEF1234567890" {
		t.Fatalf("expected trust_hash, got %v", ss["trust-hash"])
	}
	if ss["rpc-servers"] != "1.2.3.4:26657,5.6.7.8:26657" {
		t.Fatalf("expected rpc_servers from file, got %v", ss["rpc-servers"])
	}
}

func TestConfigPatcherSetsNodeMode(t *testing.T) {
	homeDir := t.TempDir()
	configPath := setupConfigFile(t, homeDir, `
[base]
mode = "full"
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{
		NodeMode: "seed",
	})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, configPath)
	base := doc["base"].(map[string]any)
	if base["mode"] != "seed" {
		t.Fatalf("expected mode 'seed', got %v", base["mode"])
	}
}

func TestConfigPatcherPreservesUnrelatedFields(t *testing.T) {
	homeDir := t.TempDir()
	configPath := setupConfigFile(t, homeDir, `
[consensus]
timeout-commit = "5s"

[p2p]
persistent-peers = ""
max-num-inbound-peers = 40

[mempool]
size = 5000
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{
		Peers: []string{"abc@1.2.3.4:26656"},
	})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, configPath)

	consensus := doc["consensus"].(map[string]any)
	if consensus["timeout-commit"] != "5s" {
		t.Fatal("consensus.timeout-commit not preserved")
	}

	p2p := doc["p2p"].(map[string]any)
	if p2p["max-num-inbound-peers"] != int64(40) {
		t.Fatal("p2p.max-num-inbound-peers not preserved")
	}

	mempool := doc["mempool"].(map[string]any)
	if mempool["size"] != int64(5000) {
		t.Fatal("mempool.size not preserved")
	}
}

func TestConfigPatcherAtomicWrite(t *testing.T) {
	homeDir := t.TempDir()
	configPath := setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = "old"
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{
		Peers: []string{"new@1.2.3.4:26656"},
	})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	// Verify no temp file left behind.
	tmpPath := configPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should not exist after successful write")
	}

	// Verify the file is valid TOML.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if _, err := patch.UnmarshalTOML(data); err != nil {
		t.Fatalf("config is not valid TOML after patch: %v", err)
	}
}

func TestConfigPatcherReadsPeersFile(t *testing.T) {
	homeDir := t.TempDir()
	setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
`)

	// Write a peers file as discover-peers would.
	peers := []string{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"}
	if err := writePeersFile(homeDir, peers); err != nil {
		t.Fatalf("writing peers file: %v", err)
	}

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	configPath := filepath.Join(homeDir, "config", "config.toml")
	doc := readTOML(t, configPath)
	p2p := doc["p2p"].(map[string]any)
	got := p2p["persistent-peers"].(string)
	expected := strings.Join(peers, ",")
	if got != expected {
		t.Fatalf("expected peers from file %q, got %q", expected, got)
	}
}

func TestConfigPatcherCreatesSection(t *testing.T) {
	homeDir := t.TempDir()
	configPath := setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{
		NodeMode: "seed",
	})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, configPath)
	base := doc["base"].(map[string]any)
	if base["mode"] != "seed" {
		t.Fatalf("expected mode 'seed', got %v", base["mode"])
	}
}

// setupAppFile creates an app.toml in the expected path within homeDir.
func setupAppFile(t *testing.T, homeDir, content string) string {
	t.Helper()
	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	appPath := filepath.Join(configDir, "app.toml")
	if err := os.WriteFile(appPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writing app.toml: %v", err)
	}
	return appPath
}

func TestConfigPatcherSnapshotGeneration(t *testing.T) {
	homeDir := t.TempDir()
	setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
`)
	appPath := setupAppFile(t, homeDir, `
pruning = "default"
snapshot-interval = 0
snapshot-keep-recent = 2

[api]
enable = true
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{
		SnapshotGeneration: &SnapshotGenerationPatch{
			Interval:   2000,
			KeepRecent: 5,
		},
	})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, appPath)
	if doc["pruning"] != "nothing" {
		t.Errorf("pruning = %v, want %q", doc["pruning"], "nothing")
	}
	if doc["snapshot-interval"] != int64(2000) {
		t.Errorf("snapshot-interval = %v, want 2000", doc["snapshot-interval"])
	}
	if doc["snapshot-keep-recent"] != int64(5) {
		t.Errorf("snapshot-keep-recent = %v, want 5", doc["snapshot-keep-recent"])
	}

	// Verify unrelated app.toml fields are preserved.
	api := doc["api"].(map[string]any)
	if api["enable"] != true {
		t.Error("api.enable not preserved")
	}
}

func TestConfigPatcherSnapshotGenerationNil_NoAppTOMLChange(t *testing.T) {
	homeDir := t.TempDir()
	setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
`)
	appPath := setupAppFile(t, homeDir, `
pruning = "default"
snapshot-interval = 0
`)

	patcher := NewConfigPatcher(homeDir)
	err := patcher.PatchConfig(context.Background(), PatchSet{})
	if err != nil {
		t.Fatalf("PatchConfig failed: %v", err)
	}

	doc := readTOML(t, appPath)
	if doc["pruning"] != "default" {
		t.Errorf("pruning = %v, want %q (should be unchanged)", doc["pruning"], "default")
	}
	if doc["snapshot-interval"] != int64(0) {
		t.Errorf("snapshot-interval = %v, want 0 (should be unchanged)", doc["snapshot-interval"])
	}
}

func TestParsePatchSet_SnapshotGeneration(t *testing.T) {
	params := map[string]any{
		"snapshotGeneration": map[string]any{
			"interval":   int64(2000),
			"keepRecent": int64(10),
		},
	}
	ps, err := parsePatchSet(params)
	if err != nil {
		t.Fatalf("parsePatchSet() error = %v", err)
	}
	if ps.SnapshotGeneration == nil {
		t.Fatal("SnapshotGeneration is nil")
	}
	if ps.SnapshotGeneration.Interval != 2000 {
		t.Errorf("Interval = %d, want 2000", ps.SnapshotGeneration.Interval)
	}
	if ps.SnapshotGeneration.KeepRecent != 10 {
		t.Errorf("KeepRecent = %d, want 10", ps.SnapshotGeneration.KeepRecent)
	}
}

func TestParsePatchSet_NoSnapshotGeneration(t *testing.T) {
	ps, err := parsePatchSet(map[string]any{})
	if err != nil {
		t.Fatalf("parsePatchSet() error = %v", err)
	}
	if ps.SnapshotGeneration != nil {
		t.Error("SnapshotGeneration should be nil when not provided")
	}
}

func readTOML(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, err := patch.ReadTOML(path)
	if err != nil {
		t.Fatalf("reading TOML %s: %v", path, err)
	}
	return doc
}
