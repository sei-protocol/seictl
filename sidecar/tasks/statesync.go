package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sei-protocol/seictl/internal/patch"
	"github.com/sei-protocol/seictl/sidecar/engine"
)

const (
	stateSyncMarkerFile = ".sei-sidecar-statesync-done"
	trustHeightOffset   = 2000
	rpcTimeout          = 10 * time.Second
)

// StateSyncConfig holds the trust point and RPC servers for Tendermint state sync.
type StateSyncConfig struct {
	TrustHeight int64
	TrustHash   string
	RpcServers  string
}

// HTTPDoer abstracts HTTP requests for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// StateSyncConfigurer discovers a trust point from peers and writes the config file.
type StateSyncConfigurer struct {
	homeDir    string
	httpClient HTTPDoer
}

// NewStateSyncConfigurer creates a configurer targeting the given home directory.
func NewStateSyncConfigurer(homeDir string, client HTTPDoer) *StateSyncConfigurer {
	if client == nil {
		client = &http.Client{Timeout: rpcTimeout}
	}
	return &StateSyncConfigurer{homeDir: homeDir, httpClient: client}
}

// Handler returns an engine.TaskHandler.
func (s *StateSyncConfigurer) Handler() engine.TaskHandler {
	return func(ctx context.Context, _ map[string]any) error {
		return s.Configure(ctx)
	}
}

// Configure reads persistent-peers from config.toml, queries a peer for a
// trust point, and writes the state sync settings back to config.toml.
func (s *StateSyncConfigurer) Configure(ctx context.Context) error {
	if markerExists(s.homeDir, stateSyncMarkerFile) {
		return nil
	}

	peers, err := readPeersFromConfig(s.homeDir)
	if err != nil {
		return fmt.Errorf("configure-state-sync: %w", err)
	}
	if len(peers) == 0 {
		return fmt.Errorf("configure-state-sync: no peers in config.toml")
	}

	rpcHosts := extractRPCHosts(peers, 2)
	if len(rpcHosts) == 0 {
		return fmt.Errorf("configure-state-sync: could not extract RPC hosts from peers")
	}

	latestHeight, err := s.queryLatestHeight(ctx, rpcHosts[0])
	if err != nil {
		return fmt.Errorf("configure-state-sync: querying latest height: %w", err)
	}

	trustHeight := latestHeight - trustHeightOffset
	if trustHeight < 1 {
		trustHeight = 1
	}

	trustHash, err := s.queryBlockHash(ctx, rpcHosts[0], trustHeight)
	if err != nil {
		return fmt.Errorf("configure-state-sync: querying block hash at height %d: %w", trustHeight, err)
	}

	// Tendermint requires at least two comma-separated RPC servers for state sync.
	// When only one peer is available, duplicate it to satisfy the requirement.
	for len(rpcHosts) < 2 {
		rpcHosts = append(rpcHosts, rpcHosts[0])
	}
	rpcServers := make([]string, len(rpcHosts))
	for i, h := range rpcHosts {
		rpcServers[i] = h + ":26657"
	}

	cfg := StateSyncConfig{
		TrustHeight: trustHeight,
		TrustHash:   trustHash,
		RpcServers:  strings.Join(rpcServers, ","),
	}

	if err := writeStateSyncToConfig(s.homeDir, cfg); err != nil {
		return fmt.Errorf("configure-state-sync: writing config.toml: %w", err)
	}

	return writeMarker(s.homeDir, stateSyncMarkerFile)
}

// extractRPCHosts extracts up to maxHosts host addresses from peer strings
// in "nodeId@host:port" format.
func extractRPCHosts(peers []string, maxHosts int) []string {
	var hosts []string
	for _, p := range peers {
		if len(hosts) >= maxHosts {
			break
		}
		parts := strings.SplitN(p, "@", 2)
		if len(parts) != 2 {
			continue
		}
		hostPort := parts[1]
		host := hostPort
		if idx := strings.LastIndex(hostPort, ":"); idx >= 0 {
			host = hostPort[:idx]
		}
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func (s *StateSyncConfigurer) queryLatestHeight(ctx context.Context, host string) (int64, error) {
	url := fmt.Sprintf("http://%s:26657/status", host)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return 0, err
	}

	var status tendermintStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return 0, fmt.Errorf("parsing status response: %w", err)
	}

	var height int64
	if _, err := fmt.Sscanf(status.SyncInfo.LatestBlockHeight, "%d", &height); err != nil {
		return 0, fmt.Errorf("parsing height %q: %w", status.SyncInfo.LatestBlockHeight, err)
	}
	return height, nil
}

func (s *StateSyncConfigurer) queryBlockHash(ctx context.Context, host string, height int64) (string, error) {
	url := fmt.Sprintf("http://%s:26657/block?height=%d", host, height)
	body, err := s.doGet(ctx, url)
	if err != nil {
		return "", err
	}

	var block tendermintBlockResponse
	if err := json.Unmarshal(body, &block); err != nil {
		return "", fmt.Errorf("parsing block response: %w", err)
	}
	if block.BlockID.Hash == "" {
		return "", fmt.Errorf("empty block hash at height %d", height)
	}
	return block.BlockID.Hash, nil
}

func (s *StateSyncConfigurer) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", url, err)
	}

	return body, nil
}

func writeStateSyncToConfig(homeDir string, cfg StateSyncConfig) error {
	configPath := filepath.Join(homeDir, "config", "config.toml")
	ssPatch := map[string]any{
		"statesync": map[string]any{
			"enable":       true,
			"trust-height": cfg.TrustHeight,
			"trust-hash":   cfg.TrustHash,
			"rpc-servers":  cfg.RpcServers,
		},
	}
	return mergeAndWrite(configPath, ssPatch)
}

// readPeersFromConfig reads the persistent-peers value from config.toml and
// splits it into individual peer strings.
func readPeersFromConfig(homeDir string) ([]string, error) {
	configPath := filepath.Join(homeDir, "config", "config.toml")
	doc, err := patch.ReadTOML(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config.toml: %w", err)
	}

	p2p, ok := doc["p2p"].(map[string]any)
	if !ok {
		return nil, nil
	}

	raw, _ := p2p["persistent-peers"].(string)
	if raw == "" {
		return nil, nil
	}

	var peers []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			peers = append(peers, p)
		}
	}
	return peers, nil
}
