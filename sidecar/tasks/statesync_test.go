package tasks

import (
	"context"
	"encoding/json"
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

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestStateSyncConfigurer_Success(t *testing.T) {
	homeDir := t.TempDir()

	peers := []string{"nodeId1@1.2.3.4:26656", "nodeId2@5.6.7.8:26656"}
	if err := writePeersFile(homeDir, peers); err != nil {
		t.Fatalf("writing peers file: %v", err)
	}

	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{
			"http://1.2.3.4:26657/status": jsonResponse(`{
				"sync_info": {"latest_block_height": "10000"}
			}`),
			"http://1.2.3.4:26657/block?height=8000": jsonResponse(`{
				"block_id": {"hash": "ABCDEF123456"}
			}`),
		},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	if err := configurer.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	cfg, err := ReadStateSyncFile(homeDir)
	if err != nil {
		t.Fatalf("reading state sync file: %v", err)
	}

	if cfg.TrustHeight != 8000 {
		t.Errorf("expected trustHeight 8000, got %d", cfg.TrustHeight)
	}
	if cfg.TrustHash != "ABCDEF123456" {
		t.Errorf("expected trustHash ABCDEF123456, got %q", cfg.TrustHash)
	}
	if cfg.RpcServers != "1.2.3.4:26657,5.6.7.8:26657" {
		t.Errorf("expected rpcServers '1.2.3.4:26657,5.6.7.8:26657', got %q", cfg.RpcServers)
	}

	if !markerExists(homeDir, stateSyncMarkerFile) {
		t.Fatal("marker file should exist after successful configure")
	}
}

func TestStateSyncConfigurer_MarkerSkips(t *testing.T) {
	homeDir := t.TempDir()

	if err := writeMarker(homeDir, stateSyncMarkerFile); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	if err := configurer.Configure(context.Background()); err != nil {
		t.Fatalf("expected nil error when marker exists, got: %v", err)
	}

	// State sync file should NOT exist since we skipped.
	if _, err := os.Stat(filepath.Join(homeDir, stateSyncFile)); !os.IsNotExist(err) {
		t.Fatal("state sync file should not exist when marker already present")
	}
}

func TestStateSyncConfigurer_NoPeersFile(t *testing.T) {
	homeDir := t.TempDir()

	configurer := NewStateSyncConfigurer(homeDir, &mockHTTPDoer{})
	err := configurer.Configure(context.Background())
	if err == nil {
		t.Fatal("expected error when peers file is missing")
	}
	if !strings.Contains(err.Error(), "reading peers file") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestStateSyncConfigurer_LowHeight(t *testing.T) {
	homeDir := t.TempDir()

	peers := []string{"nodeId1@10.0.0.1:26656"}
	if err := writePeersFile(homeDir, peers); err != nil {
		t.Fatalf("writing peers file: %v", err)
	}

	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{
			"http://10.0.0.1:26657/status": jsonResponse(`{
				"sync_info": {"latest_block_height": "500"}
			}`),
			"http://10.0.0.1:26657/block?height=1": jsonResponse(`{
				"block_id": {"hash": "GENESIS_HASH"}
			}`),
		},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	if err := configurer.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	cfg, err := ReadStateSyncFile(homeDir)
	if err != nil {
		t.Fatalf("reading state sync file: %v", err)
	}

	if cfg.TrustHeight != 1 {
		t.Errorf("expected trustHeight clamped to 1, got %d", cfg.TrustHeight)
	}
	if cfg.TrustHash != "GENESIS_HASH" {
		t.Errorf("expected trustHash GENESIS_HASH, got %q", cfg.TrustHash)
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

	peers := []string{"nodeId1@1.2.3.4:26656"}
	if err := writePeersFile(homeDir, peers); err != nil {
		t.Fatalf("writing peers file: %v", err)
	}

	mock := &mockHTTPDoer{
		responses: map[string]*http.Response{
			"http://1.2.3.4:26657/status": jsonResponse(`{
				"sync_info": {"latest_block_height": "5000"}
			}`),
			"http://1.2.3.4:26657/block?height=3000": jsonResponse(`{
				"block_id": {"hash": "TRUST_HASH"}
			}`),
		},
	}

	configurer := NewStateSyncConfigurer(homeDir, mock)
	handler := configurer.Handler()

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("Handler failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(homeDir, stateSyncFile))
	if err != nil {
		t.Fatalf("reading state sync file: %v", err)
	}
	var cfg StateSyncConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing state sync file: %v", err)
	}
	if cfg.TrustHeight != 3000 {
		t.Errorf("expected trustHeight 3000, got %d", cfg.TrustHeight)
	}
}
