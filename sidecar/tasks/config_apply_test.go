package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	seiconfig "github.com/sei-protocol/sei-config"
)

func writeDefaultConfig(t *testing.T, homeDir string, mode seiconfig.NodeMode) {
	t.Helper()
	cfg := seiconfig.DefaultForMode(mode)
	if err := seiconfig.WriteConfigToDir(cfg, homeDir); err != nil {
		t.Fatalf("writing default config: %v", err)
	}
}

func TestConfigApplier_FullGeneration(t *testing.T) {
	homeDir := t.TempDir()
	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"mode":        "validator",
		"incremental": false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := seiconfig.ReadConfigFromDir(homeDir)
	if err != nil {
		t.Fatalf("reading back config: %v", err)
	}
	if cfg.Mode != seiconfig.ModeValidator {
		t.Errorf("mode: got %q, want %q", cfg.Mode, seiconfig.ModeValidator)
	}
	if cfg.EVM.HTTPEnabled {
		t.Error("validator should have EVM HTTP disabled")
	}
}

func TestConfigApplier_FullWithOverrides(t *testing.T) {
	homeDir := t.TempDir()
	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"mode":        "full",
		"incremental": false,
		"overrides": map[string]any{
			"evm.http_port": "9545",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := seiconfig.ReadConfigFromDir(homeDir)
	if err != nil {
		t.Fatalf("reading back config: %v", err)
	}
	if cfg.EVM.HTTPPort != 9545 {
		t.Errorf("evm.http_port: got %d, want 9545", cfg.EVM.HTTPPort)
	}
}

func TestConfigApplier_FullMissingMode(t *testing.T) {
	homeDir := t.TempDir()
	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"incremental": false,
	})
	if err == nil {
		t.Fatal("expected error for missing mode")
	}
	if !strings.Contains(err.Error(), "mode is required") {
		t.Errorf("error should mention mode: %v", err)
	}
}

func TestConfigApplier_FullInvalidMode(t *testing.T) {
	homeDir := t.TempDir()
	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"mode":        "bogus",
		"incremental": false,
	})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("error should mention invalid mode: %v", err)
	}
}

func TestConfigApplier_Incremental(t *testing.T) {
	homeDir := t.TempDir()
	writeDefaultConfig(t, homeDir, seiconfig.ModeFull)

	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"incremental": true,
		"overrides": map[string]any{
			"evm.http_port": "9999",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := seiconfig.ReadConfigFromDir(homeDir)
	if err != nil {
		t.Fatalf("reading back config: %v", err)
	}
	if cfg.EVM.HTTPPort != 9999 {
		t.Errorf("evm.http_port: got %d, want 9999", cfg.EVM.HTTPPort)
	}
	if cfg.Mode != seiconfig.ModeFull {
		t.Errorf("mode should be preserved: got %q", cfg.Mode)
	}
}

func TestConfigApplier_IncrementalPreservesExisting(t *testing.T) {
	homeDir := t.TempDir()
	writeDefaultConfig(t, homeDir, seiconfig.ModeFull)

	origCfg, _ := seiconfig.ReadConfigFromDir(homeDir)
	origMoniker := origCfg.Chain.Moniker

	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"incremental": true,
		"overrides": map[string]any{
			"evm.http_port": "7777",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, _ := seiconfig.ReadConfigFromDir(homeDir)
	if cfg.Chain.Moniker != origMoniker {
		t.Errorf("moniker changed: got %q, want %q", cfg.Chain.Moniker, origMoniker)
	}
}

func TestConfigApplier_WritesFiles(t *testing.T) {
	homeDir := t.TempDir()
	applier := NewConfigApplier(homeDir)
	handler := applier.Handler()

	err := handler(context.Background(), map[string]any{
		"mode":        "full",
		"incremental": false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, f := range []string{"config.toml", "app.toml"} {
		path := filepath.Join(homeDir, "config", f)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s not created: %v", f, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", f)
		}
	}
}

func TestConfigApplier_AllModes(t *testing.T) {
	modes := []string{"validator", "full", "seed", "archive"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			homeDir := t.TempDir()
			applier := NewConfigApplier(homeDir)
			handler := applier.Handler()

			err := handler(context.Background(), map[string]any{
				"mode":        mode,
				"incremental": false,
			})
			if err != nil {
				t.Fatalf("mode %s failed: %v", mode, err)
			}

			cfg, err := seiconfig.ReadConfigFromDir(homeDir)
			if err != nil {
				t.Fatalf("reading back %s config: %v", mode, err)
			}
			if string(cfg.Mode) != mode {
				t.Errorf("mode: got %q, want %q", cfg.Mode, mode)
			}
		})
	}
}
