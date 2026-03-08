package tasks

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockHTTPDoer struct {
	responses map[string]*http.Response
}

func (m *mockHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	resp, ok := m.responses[req.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected request: %s", req.URL.String())
	}
	return resp, nil
}

func generateBlockHash() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%X", b)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func setupPeersInConfig(t *testing.T, homeDir string, peers []string) {
	t.Helper()
	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	content := fmt.Sprintf("[p2p]\npersistent-peers = %q\n", strings.Join(peers, ","))
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}

func TestStateSyncConfigurer_Success(t *testing.T) {
	homeDir := t.TempDir()
	setupPeersInConfig(t, homeDir, []string{"nodeId1@1.2.3.4:26656", "nodeId2@5.6.7.8:26656"})

	hash := generateBlockHash()
	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{
			"http://1.2.3.4:26657/status": jsonResponse(`{
				"sync_info": {"latest_block_height": "10000"}
			}`),
			"http://1.2.3.4:26657/block?height=8000": jsonResponse(fmt.Sprintf(`{
				"block_id": {"hash": %q}
			}`, hash)),
		},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	if err := configurer.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	if !markerExists(homeDir, stateSyncMarkerFile) {
		t.Fatal("marker file should exist after successful configure")
	}

	configDoc := readTOML(t, filepath.Join(homeDir, "config", "config.toml"))
	ss := configDoc["statesync"].(map[string]any)
	if ss["enable"] != true {
		t.Error("expected statesync.enable = true in config.toml")
	}
	if ss["trust-height"] != int64(8000) {
		t.Errorf("expected statesync.trust-height = 8000, got %v", ss["trust-height"])
	}
	if ss["trust-hash"] != hash {
		t.Errorf("expected trust-hash = %s, got %v", hash, ss["trust-hash"])
	}
	if ss["rpc-servers"] != "1.2.3.4:26657,5.6.7.8:26657" {
		t.Errorf("expected rpc-servers, got %v", ss["rpc-servers"])
	}
}

func TestStateSyncConfigurer_MarkerSkips(t *testing.T) {
	homeDir := t.TempDir()

	if err := writeMarker(homeDir, stateSyncMarkerFile); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	configurer := NewStateSyncConfigurer(homeDir, &mockHTTPDoer{})
	if err := configurer.Configure(context.Background()); err != nil {
		t.Fatalf("expected nil error when marker exists, got: %v", err)
	}
}

func TestStateSyncConfigurer_NoPeers(t *testing.T) {
	homeDir := t.TempDir()
	setupPeersInConfig(t, homeDir, nil)

	configurer := NewStateSyncConfigurer(homeDir, &mockHTTPDoer{})
	err := configurer.Configure(context.Background())
	if err == nil {
		t.Fatal("expected error when no peers in config")
	}
}

func TestStateSyncConfigurer_LowHeight(t *testing.T) {
	homeDir := t.TempDir()
	setupPeersInConfig(t, homeDir, []string{"nodeId1@10.0.0.1:26656"})

	hash := generateBlockHash()
	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{
			"http://10.0.0.1:26657/status": jsonResponse(`{
				"sync_info": {"latest_block_height": "500"}
			}`),
			"http://10.0.0.1:26657/block?height=1": jsonResponse(fmt.Sprintf(`{
				"block_id": {"hash": %q}
			}`, hash)),
		},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	if err := configurer.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	configDoc := readTOML(t, filepath.Join(homeDir, "config", "config.toml"))
	ss := configDoc["statesync"].(map[string]any)
	if ss["trust-height"] != int64(1) {
		t.Errorf("expected trustHeight clamped to 1, got %v", ss["trust-height"])
	}
	if ss["trust-hash"] != hash {
		t.Errorf("expected trustHash %s, got %v", hash, ss["trust-hash"])
	}
	// Single peer should be duplicated to satisfy Tendermint requirement.
	if ss["rpc-servers"] != "10.0.0.1:26657,10.0.0.1:26657" {
		t.Errorf("expected duplicated rpc-servers, got %v", ss["rpc-servers"])
	}
}

func TestStateSyncConfigurer_InvalidBlockHash(t *testing.T) {
	tests := []struct {
		name    string
		hash    string
		wantErr string
	}{
		{"empty hash", "", "empty block hash"},
		{"short hash", "ABCDEF", "unexpected block hash length"},
		{"long hash", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA00", "unexpected block hash length"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			setupPeersInConfig(t, homeDir, []string{"nodeId1@1.2.3.4:26656"})

			mock := &mockHTTPDoer{
				responses: map[string]*http.Response{
					"http://1.2.3.4:26657/status": jsonResponse(`{
						"sync_info": {"latest_block_height": "10000"}
					}`),
					"http://1.2.3.4:26657/block?height=8000": jsonResponse(fmt.Sprintf(`{
						"block_id": {"hash": %q}
					}`, tt.hash)),
				},
			}

			configurer := NewStateSyncConfigurer(homeDir, mock)
			err := configurer.Configure(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestExtractRPCHosts(t *testing.T) {
	tests := []struct {
		name     string
		peers    []string
		maxHosts int
		want     []string
	}{
		{
			name:     "standard peers",
			peers:    []string{"nodeId1@1.2.3.4:26656", "nodeId2@5.6.7.8:26656"},
			maxHosts: 2,
			want:     []string{"1.2.3.4", "5.6.7.8"},
		},
		{
			name:     "max hosts limits output",
			peers:    []string{"a@1.1.1.1:26656", "b@2.2.2.2:26656", "c@3.3.3.3:26656"},
			maxHosts: 2,
			want:     []string{"1.1.1.1", "2.2.2.2"},
		},
		{
			name:     "invalid format skipped",
			peers:    []string{"no-at-sign", "valid@10.0.0.1:26656"},
			maxHosts: 2,
			want:     []string{"10.0.0.1"},
		},
		{
			name:     "empty peers",
			peers:    []string{},
			maxHosts: 2,
			want:     nil,
		},
		{
			name:     "host without port",
			peers:    []string{"nodeId@myhost"},
			maxHosts: 1,
			want:     []string{"myhost"},
		},
		{
			name:     "IPv6-style host with port",
			peers:    []string{"nodeId@[::1]:26656"},
			maxHosts: 1,
			want:     []string{"[::1]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRPCHosts(tt.peers, tt.maxHosts)
			if len(got) != len(tt.want) {
				t.Fatalf("expected %d hosts, got %d: %v", len(tt.want), len(got), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("host[%d]: expected %q, got %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}

func TestStateSyncConfigurer_Handler(t *testing.T) {
	homeDir := t.TempDir()
	setupPeersInConfig(t, homeDir, []string{"nodeId1@1.2.3.4:26656"})

	hash := generateBlockHash()
	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{
			"http://1.2.3.4:26657/status": jsonResponse(`{
				"sync_info": {"latest_block_height": "5000"}
			}`),
			"http://1.2.3.4:26657/block?height=3000": jsonResponse(fmt.Sprintf(`{
				"block_id": {"hash": %q}
			}`, hash)),
		},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	handler := configurer.Handler()

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("Handler failed: %v", err)
	}

	configDoc := readTOML(t, filepath.Join(homeDir, "config", "config.toml"))
	ss := configDoc["statesync"].(map[string]any)
	if ss["trust-height"] != int64(3000) {
		t.Errorf("expected trustHeight 3000, got %v", ss["trust-height"])
	}
}

func TestReadPeersFromConfig(t *testing.T) {
	homeDir := t.TempDir()
	setupPeersInConfig(t, homeDir, []string{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"})

	peers, err := readPeersFromConfig(homeDir)
	if err != nil {
		t.Fatalf("readPeersFromConfig failed: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	if peers[0] != "abc@1.2.3.4:26656" {
		t.Errorf("peer[0] = %q, want abc@1.2.3.4:26656", peers[0])
	}
}

func TestReadPeersFromConfig_Empty(t *testing.T) {
	homeDir := t.TempDir()
	setupPeersInConfig(t, homeDir, nil)

	peers, err := readPeersFromConfig(homeDir)
	if err != nil {
		t.Fatalf("readPeersFromConfig failed: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(peers))
	}
}

func TestReadPeersFromConfig_NoConfigFile(t *testing.T) {
	homeDir := t.TempDir()
	peers, err := readPeersFromConfig(homeDir)
	if err != nil {
		t.Fatalf("readPeersFromConfig failed: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers for missing config, got %d", len(peers))
	}
}
