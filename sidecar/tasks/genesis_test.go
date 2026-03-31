package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGenesisFetcher_EmbeddedChain(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "pacific-1", "test-bucket", "us-east-2", nil)
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
	fetcher := NewGenesisFetcher(homeDir, "atlantic-2", "test-bucket", "us-east-2", nil)
	handler := fetcher.Handler()

	if err := handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("second call (should skip via marker): %v", err)
	}
}

func TestGenesisFetcher_UnknownChainFallsBackToS3(t *testing.T) {
	homeDir := t.TempDir()
	called := false
	mockFactory := func(ctx context.Context, region string) (S3GetObjectAPI, error) {
		called = true
		if region != "us-east-2" {
			t.Errorf("region = %q, want %q", region, "us-east-2")
		}
		return nil, fmt.Errorf("mock: intentional S3 error")
	}
	fetcher := NewGenesisFetcher(homeDir, "custom-devnet-1", "my-genesis-bucket", "us-east-2", mockFactory)
	handler := fetcher.Handler()

	err := handler(context.Background(), map[string]any{})
	if !called {
		t.Fatal("expected S3 fallback for unknown chain")
	}
	if err == nil {
		t.Fatal("expected error from mock S3 factory")
	}
}

func TestGenesisFetcher_UnknownChainNoBucket(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "custom-devnet-1", "", "", nil)
	handler := fetcher.Handler()

	err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown chain with no bucket configured")
	}
}

func TestGenesisFetcher_NoChainID(t *testing.T) {
	homeDir := t.TempDir()
	fetcher := NewGenesisFetcher(homeDir, "", "bucket", "region", nil)
	handler := fetcher.Handler()

	err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error when chainID is empty")
	}
}
