package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityGenerator_CallsSeidInit(t *testing.T) {
	homeDir := t.TempDir()
	var captured []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		captured = append(captured, name)
		captured = append(captured, args...)
		return nil, nil
	}

	handler := NewIdentityGenerator(homeDir, runner).Handler()
	err := handler(context.Background(), map[string]any{
		"chainId": "test-chain-1",
		"moniker": "val-0",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"seid", "init", "val-0", "--chain-id", "test-chain-1", "--home", homeDir}
	if len(captured) != len(expected) {
		t.Fatalf("command args = %v, want %v", captured, expected)
	}
	for i := range expected {
		if captured[i] != expected[i] {
			t.Errorf("arg[%d] = %q, want %q", i, captured[i], expected[i])
		}
	}
}

func TestIdentityGenerator_Idempotent(t *testing.T) {
	homeDir := t.TempDir()
	callCount := 0
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		callCount++
		return nil, nil
	}

	handler := NewIdentityGenerator(homeDir, runner).Handler()
	params := map[string]any{"chainId": "test-chain-1", "moniker": "val-0"}

	if err := handler(context.Background(), params); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}

	if err := handler(context.Background(), params); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected still 1 call after idempotent skip, got %d", callCount)
	}
}

func TestIdentityGenerator_MissingChainID(t *testing.T) {
	handler := NewIdentityGenerator(t.TempDir(), nil).Handler()
	err := handler(context.Background(), map[string]any{"moniker": "val-0"})
	if err == nil {
		t.Fatal("expected error for missing chainId")
	}
}

func TestIdentityGenerator_MissingMoniker(t *testing.T) {
	handler := NewIdentityGenerator(t.TempDir(), nil).Handler()
	err := handler(context.Background(), map[string]any{"chainId": "test-chain-1"})
	if err == nil {
		t.Fatal("expected error for missing moniker")
	}
}

func TestIdentityGenerator_SeidFailure(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("seid not found")
	}
	handler := NewIdentityGenerator(t.TempDir(), runner).Handler()
	err := handler(context.Background(), map[string]any{"chainId": "c", "moniker": "m"})
	if err == nil {
		t.Fatal("expected error when seid fails")
	}

	if !markerExists(t.TempDir(), identityMarkerFile) {
		// Good — marker should not exist after failure
	}
}

func TestIdentityGenerator_NoMarkerOnFailure(t *testing.T) {
	homeDir := t.TempDir()
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("fail")
	}
	handler := NewIdentityGenerator(homeDir, runner).Handler()
	_ = handler(context.Background(), map[string]any{"chainId": "c", "moniker": "m"})

	if _, err := os.Stat(filepath.Join(homeDir, identityMarkerFile)); err == nil {
		t.Fatal("marker file should not exist after seid failure")
	}
}
