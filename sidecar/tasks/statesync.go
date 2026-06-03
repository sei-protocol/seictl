package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sei-protocol/seictl/internal/patch"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/rpc"
	"github.com/sei-protocol/seilog"
)

var ssLog = seilog.NewLogger("seictl", "task", "state-sync")

const (
	stateSyncMarkerFile = ".sei-sidecar-statesync-done"
	trustHeightOffset   = 2000
	rpcPort             = "26657"
	witnessProbeTimeout = 10 * time.Second
)

// StateSyncConfig holds the trust point and RPC servers for Tendermint state sync.
type StateSyncConfig struct {
	TrustHeight      int64
	TrustHash        string
	TrustPeriod      string
	RpcServers       string
	UseLocalSnapshot bool
	BackfillBlocks   int64
}

// StateSyncConfigurer discovers a trust point from peers and writes the config file.
type StateSyncConfigurer struct {
	homeDir    string
	httpClient rpc.HTTPDoer
}

// NewStateSyncConfigurer creates a configurer targeting the given home directory.
func NewStateSyncConfigurer(homeDir string, client rpc.HTTPDoer) *StateSyncConfigurer {
	if client == nil {
		client = &http.Client{}
	}
	return &StateSyncConfigurer{homeDir: homeDir, httpClient: client}
}

// Handler returns an engine.TaskHandler.
func (s *StateSyncConfigurer) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, params StateSyncRequest) error {
		return s.Configure(ctx, params)
	})
}

// StateSyncRequest groups the caller-provided parameters for state-sync configuration.
type StateSyncRequest struct {
	UseLocalSnapshot bool   `json:"useLocalSnapshot"`
	TrustPeriod      string `json:"trustPeriod"`
	BackfillBlocks   int64  `json:"backfillBlocks"`
	// RpcServers are explicit light-client witness endpoints ("host:port").
	// When set they are used verbatim and witnesses are NOT derived from
	// persistent-peers. The controller supplies reachable internal RPC
	// endpoints here so the witness plane stays internal even for nodes whose
	// persistent-peers are external P2P NLB hostnames (which serve only P2P).
	RpcServers []string `json:"rpcServers"`
}

// Configure determines the state-sync light-client witnesses, queries one for a
// trust point, and writes the state sync settings back to config.toml.
//
// Witnesses come from p.RpcServers when the caller provides them (the
// controller resolves these to reachable internal RPC endpoints); otherwise
// they are derived from persistent-peers. Either way only witnesses that
// actually answer /status are written — a witness that does not serve RPC
// (e.g. an external P2P NLB hostname) otherwise makes seid exit with
// "no witnesses connected" and crashloop. When UseLocalSnapshot is true the
// trust height is taken from the locally-restored snapshot rather than queried.
func (s *StateSyncConfigurer) Configure(ctx context.Context, p StateSyncRequest) error {
	if markerExists(s.homeDir, stateSyncMarkerFile) {
		ssLog.Debug("already completed, skipping")
		return nil
	}

	candidates, err := s.witnessCandidates(p)
	if err != nil {
		return fmt.Errorf("configure-state-sync: %w", err)
	}

	reachable := s.reachableWitnesses(ctx, candidates)
	if len(reachable) == 0 {
		return fmt.Errorf("configure-state-sync: no reachable RPC witness among %v", candidates)
	}

	var trustHeight int64
	if p.UseLocalSnapshot {
		h, err := discoverLocalSnapshotHeight(s.homeDir)
		if err != nil {
			return fmt.Errorf("configure-state-sync: discovering local snapshot height: %w", err)
		}
		trustHeight = h
		ssLog.Info("using local snapshot height as trust height", "height", trustHeight)
	} else {
		ssLog.Info("querying latest height", "endpoint", reachable[0])
		latestHeight, err := s.queryLatestHeight(ctx, reachable[0])
		if err != nil {
			return fmt.Errorf("configure-state-sync: querying latest height: %w", err)
		}
		trustHeight = latestHeight - trustHeightOffset
		if trustHeight < 1 {
			trustHeight = 1
		}
	}

	ssLog.Info("querying trust hash", "trust-height", trustHeight, "endpoint", reachable[0])
	trustHash, err := s.queryBlockHash(ctx, reachable[0], trustHeight)
	if err != nil {
		return fmt.Errorf("configure-state-sync: querying block hash at height %d: %w", trustHeight, err)
	}

	// CometBFT's light client requires at least two witnesses; pad by
	// duplicating the primary when only one reachable witness exists.
	for len(reachable) < 2 {
		reachable = append(reachable, reachable[0])
	}

	cfg := StateSyncConfig{
		TrustHeight:      trustHeight,
		TrustHash:        trustHash,
		TrustPeriod:      p.TrustPeriod,
		RpcServers:       strings.Join(reachable, ","),
		UseLocalSnapshot: p.UseLocalSnapshot,
		BackfillBlocks:   p.BackfillBlocks,
	}

	ssLog.Info("writing config", "trust-height", trustHeight, "trust-hash", trustHash,
		"trust-period", p.TrustPeriod, "rpc-servers", cfg.RpcServers,
		"use-local-snapshot", p.UseLocalSnapshot, "backfill-blocks", p.BackfillBlocks)
	if err := writeStateSyncToConfig(s.homeDir, cfg); err != nil {
		return fmt.Errorf("configure-state-sync: writing config.toml: %w", err)
	}

	return writeMarker(s.homeDir, stateSyncMarkerFile)
}

// witnessCandidates returns the candidate state-sync witness endpoints
// ("host:port"). Caller-provided RpcServers are used verbatim; otherwise the
// witnesses are derived from persistent-peers by attaching the RPC port to each
// peer's host. The peer-derived form is correct for peers that serve RPC on the
// same host as P2P (EC2 peers, internal cluster DNS) and is filtered by
// reachableWitnesses for peers that do not (external P2P NLB hostnames).
func (s *StateSyncConfigurer) witnessCandidates(p StateSyncRequest) ([]string, error) {
	if len(p.RpcServers) > 0 {
		ssLog.Info("using caller-provided rpc witnesses", "count", len(p.RpcServers))
		return p.RpcServers, nil
	}

	peers, err := readPeersFromConfig(s.homeDir)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, fmt.Errorf("no peers in config.toml")
	}
	ssLog.Debug("found peers", "count", len(peers))

	hosts := extractRPCHosts(peers, 2)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("could not extract RPC hosts from peers")
	}
	endpoints := make([]string, len(hosts))
	for i, h := range hosts {
		endpoints[i] = h + ":" + rpcPort
	}
	return endpoints, nil
}

// reachableWitnesses returns the candidate endpoints whose /status responds.
// Dropping unreachable witnesses turns an otherwise-crashlooping config (seid
// exits on "no witnesses connected") into a working config or a clear
// configure-time error.
func (s *StateSyncConfigurer) reachableWitnesses(ctx context.Context, candidates []string) []string {
	reachable := make([]string, 0, len(candidates))
	for _, ep := range candidates {
		if err := s.probeWitness(ctx, ep); err != nil {
			ssLog.Warn("state-sync witness unreachable, skipping", "endpoint", ep, "err", err)
			continue
		}
		reachable = append(reachable, ep)
	}
	return reachable
}

// probeWitness reports whether endpoint answers /status within the probe timeout.
func (s *StateSyncConfigurer) probeWitness(ctx context.Context, endpoint string) error {
	pctx, cancel := context.WithTimeout(ctx, witnessProbeTimeout)
	defer cancel()
	_, err := s.rpcClientForEndpoint(endpoint).Get(pctx, "/status")
	return err
}

// discoverLocalSnapshotHeight scans the Tendermint snapshots directory for the
// highest available snapshot height. Snapshots are stored as
// <home>/data/snapshots/<height>/<format>/.
func discoverLocalSnapshotHeight(homeDir string) (int64, error) {
	snapshotDir := filepath.Join(homeDir, "data", "snapshots")
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return 0, fmt.Errorf("reading snapshots directory: %w", err)
	}

	var maxHeight int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		h, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue
		}
		if h > maxHeight {
			maxHeight = h
		}
	}

	if maxHeight == 0 {
		return 0, fmt.Errorf("no snapshot found in %s", snapshotDir)
	}
	return maxHeight, nil
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

// rpcClientForEndpoint builds an rpc.Client targeting a full "host:port" RPC endpoint.
func (s *StateSyncConfigurer) rpcClientForEndpoint(endpoint string) *rpc.Client {
	return rpc.NewClient("http://"+endpoint, s.httpClient)
}

func (s *StateSyncConfigurer) queryLatestHeight(ctx context.Context, endpoint string) (int64, error) {
	raw, err := s.rpcClientForEndpoint(endpoint).Get(ctx, "/status")
	if err != nil {
		return 0, err
	}

	var status rpc.StatusResult
	if err := json.Unmarshal(raw, &status); err != nil {
		return 0, fmt.Errorf("parsing status response: %w", err)
	}

	height, err := strconv.ParseInt(status.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing height %q: %w", status.SyncInfo.LatestBlockHeight, err)
	}
	return height, nil
}

func (s *StateSyncConfigurer) queryBlockHash(ctx context.Context, endpoint string, height int64) (string, error) {
	path := fmt.Sprintf("/block?height=%d", height)
	raw, err := s.rpcClientForEndpoint(endpoint).Get(ctx, path)
	if err != nil {
		return "", err
	}

	var block rpc.BlockResult
	if err := json.Unmarshal(raw, &block); err != nil {
		return "", fmt.Errorf("parsing block response: %w", err)
	}
	hash := block.BlockID.Hash
	if hash == "" {
		return "", fmt.Errorf("empty block hash at height %d", height)
	}
	const sha256HexLen = 64
	if len(hash) != sha256HexLen {
		return "", fmt.Errorf("unexpected block hash length at height %d: got %d, want %d", height, len(hash), sha256HexLen)
	}
	return hash, nil
}

func writeStateSyncToConfig(homeDir string, cfg StateSyncConfig) error {
	configPath := filepath.Join(homeDir, "config", "config.toml")
	ss := map[string]any{
		"enable":             true,
		"trust-height":       cfg.TrustHeight,
		"trust-hash":         cfg.TrustHash,
		"rpc-servers":        cfg.RpcServers,
		"use-local-snapshot": cfg.UseLocalSnapshot,
	}
	if cfg.TrustPeriod != "" {
		ss["trust-period"] = cfg.TrustPeriod
	}
	if cfg.BackfillBlocks > 0 {
		ss["backfill-blocks"] = cfg.BackfillBlocks
	}
	return mergeAndWrite(configPath, map[string]any{"statesync": ss})
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
