package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenesisFetcher_EmbeddedChain(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "pacific-1", nil)
	handler := fetcher.Handler()

	err := handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(homeDir, "config", "genesis.json"))
	if err != nil {
		t.Fatalf("reading genesis.json: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("genesis.json is empty")
	}

	var doc struct {
		ChainID string `json:"chain_id"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("genesis.json is not valid JSON: %v", err)
	}
	if doc.ChainID != "pacific-1" {
		t.Errorf("chain_id = %q, want %q", doc.ChainID, "pacific-1")
	}
}

func TestGenesisFetcher_EmbeddedChain_Idempotent(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "atlantic-2", nil)
	handler := fetcher.Handler()

	if err := handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("second call (should skip via marker): %v", err)
	}
}

func TestGenesisFetcher_UnknownChainNoS3(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "nonexistent-99", nil)
	handler := fetcher.Handler()

	err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown chain with no S3 params")
	}
}

func TestGenesisFetcher_NoChainIDNoS3(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "", nil)
	handler := fetcher.Handler()

	err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error when both chainID and S3 params are missing")
	}
}

func TestParseGenesisS3Config_WithURI(t *testing.T) {
	cfg, err := parseGenesisS3Config(map[string]any{
		"uri":    "s3://my-bucket/path/to/genesis.json",
		"region": "us-west-2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil S3 config")
	}
	if cfg.Bucket != "my-bucket" {
		t.Errorf("bucket = %q, want %q", cfg.Bucket, "my-bucket")
	}
	if cfg.Key != "path/to/genesis.json" {
		t.Errorf("key = %q, want %q", cfg.Key, "path/to/genesis.json")
	}
}

func TestParseGenesisS3Config_NoURI(t *testing.T) {
	cfg, err := parseGenesisS3Config(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil S3 config, got %+v", cfg)
	}
}

func TestParseGenesisS3Config_URIWithoutRegion(t *testing.T) {
	_, err := parseGenesisS3Config(map[string]any{
		"uri": "s3://bucket/key",
	})
	if err == nil {
		t.Fatal("expected error when uri is set without region")
	}
}
