package tasks

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUpdatePeersHandlerPatchesAndSignals(t *testing.T) {
	homeDir := t.TempDir()
	setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
`)

	var signalCalled bool
	var signalSent syscall.Signal
	origSignalFn := SignalSeidFn
	SignalSeidFn = func(sig syscall.Signal) error {
		signalCalled = true
		signalSent = sig
		return nil
	}
	defer func() { SignalSeidFn = origSignalFn }()

	handler := UpdatePeersHandler(homeDir)
	err := handler(context.Background(), map[string]any{
		"peers": []any{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"},
	})
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if !signalCalled {
		t.Fatal("expected SIGHUP to be sent")
	}
	if signalSent != syscall.SIGHUP {
		t.Fatalf("expected SIGHUP, got signal %d", signalSent)
	}

	// Verify config was patched.
	configPath := filepath.Join(homeDir, "config", "config.toml")
	doc := readTOML(t, configPath)
	p2p := doc["p2p"].(map[string]any)
	peers := p2p["persistent-peers"].(string)
	if peers != "abc@1.2.3.4:26656,def@5.6.7.8:26656" {
		t.Fatalf("expected patched peers, got %q", peers)
	}
}

func TestUpdatePeersHandlerMissingPeers(t *testing.T) {
	handler := UpdatePeersHandler(t.TempDir())
	err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error when peers param is missing")
	}
}

func TestUpdatePeersHandlerStringPeers(t *testing.T) {
	homeDir := t.TempDir()
	setupConfigFile(t, homeDir, `
[p2p]
persistent-peers = ""
`)

	origSignalFn := SignalSeidFn
	SignalSeidFn = func(_ syscall.Signal) error { return nil }
	defer func() { SignalSeidFn = origSignalFn }()

	handler := UpdatePeersHandler(homeDir)
	err := handler(context.Background(), map[string]any{
		"peers": "abc@1.2.3.4:26656,def@5.6.7.8:26656",
	})
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	configPath := filepath.Join(homeDir, "config", "config.toml")
	doc := readTOML(t, configPath)
	p2p := doc["p2p"].(map[string]any)
	peers := p2p["persistent-peers"].(string)
	if peers != "abc@1.2.3.4:26656,def@5.6.7.8:26656" {
		t.Fatalf("expected patched peers, got %q", peers)
	}
}

func TestSignalSeidProcessDetection(t *testing.T) {
	// isSeidProcess should match "seid" binary name.
	if !isSeidProcess("seid\x00start\x00") {
		t.Fatal("expected seid process to be detected")
	}
	if !isSeidProcess("/usr/bin/seid\x00start\x00") {
		t.Fatal("expected seid with full path to be detected")
	}
	if isSeidProcess("sei-sidecar\x00") {
		t.Fatal("sei-sidecar should not match seid")
	}
	if isSeidProcess("") {
		t.Fatal("empty cmdline should not match")
	}
}

func TestMarkReadyHandler(t *testing.T) {
	handler := MarkReadyHandler()
	err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("mark-ready handler should succeed, got: %v", err)
	}
}

func TestMarkerFileOperations(t *testing.T) {
	homeDir := t.TempDir()

	if markerExists(homeDir, "test-marker") {
		t.Fatal("marker should not exist initially")
	}

	if err := writeMarker(homeDir, "test-marker"); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	if !markerExists(homeDir, "test-marker") {
		t.Fatal("marker should exist after write")
	}

	// Verify the file actually exists on disk.
	if _, err := os.Stat(filepath.Join(homeDir, "test-marker")); err != nil {
		t.Fatalf("marker file not on disk: %v", err)
	}
}
