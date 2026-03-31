package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var genesisPeersLog = seilog.NewLogger("seictl", "task", "set-genesis-peers")

// SetGenesisPeersRequest holds the typed parameters for the set-genesis-peers task.
type SetGenesisPeersRequest struct {
	Bucket string `json:"s3Bucket"`
	Key    string `json:"s3Key"`
	Region string `json:"s3Region"`
}

// GenesisPeersSetter downloads a peers.json file produced by the genesis
// assembler and writes the entries into config.toml as persistent_peers,
// filtering out the current node's own entry.
type GenesisPeersSetter struct {
	homeDir         string
	s3ClientFactory S3ClientFactory
}

// NewGenesisPeersSetter creates a setter targeting the given home directory.
func NewGenesisPeersSetter(homeDir string, s3Factory S3ClientFactory) *GenesisPeersSetter {
	if s3Factory == nil {
		s3Factory = DefaultS3ClientFactory
	}
	return &GenesisPeersSetter{homeDir: homeDir, s3ClientFactory: s3Factory}
}

// Handler returns an engine.TaskHandler for the set-genesis-peers task.
//
// Expected params:
//
//	{"s3Bucket": "...", "s3Key": "...", "s3Region": "..."}
func (g *GenesisPeersSetter) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, cfg SetGenesisPeersRequest) error {
		if cfg.Bucket == "" {
			return fmt.Errorf("set-genesis-peers: missing required param 's3Bucket'")
		}
		if cfg.Key == "" {
			return fmt.Errorf("set-genesis-peers: missing required param 's3Key'")
		}
		if cfg.Region == "" {
			return fmt.Errorf("set-genesis-peers: missing required param 's3Region'")
		}

		s3Client, err := g.s3ClientFactory(ctx, cfg.Region)
		if err != nil {
			return fmt.Errorf("set-genesis-peers: building S3 client: %w", err)
		}

		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(cfg.Key),
		})
		if err != nil {
			return fmt.Errorf("set-genesis-peers: downloading peers.json: %w", err)
		}
		data, err := io.ReadAll(output.Body)
		_ = output.Body.Close()
		if err != nil {
			return fmt.Errorf("set-genesis-peers: reading peers.json: %w", err)
		}

		var allPeers []string
		if err := json.Unmarshal(data, &allPeers); err != nil {
			return fmt.Errorf("set-genesis-peers: parsing peers.json: %w", err)
		}

		selfID, err := readLocalNodeID(g.homeDir)
		if err != nil {
			return err
		}

		var filtered []string
		for _, peer := range allPeers {
			if !strings.HasPrefix(peer, selfID+"@") {
				filtered = append(filtered, peer)
			}
		}

		genesisPeersLog.Info("applying genesis peers",
			"total", len(allPeers), "self", selfID, "peers", len(filtered))

		return writePeersToConfig(g.homeDir, filtered)
	})
}

// readLocalNodeID reads the Tendermint node ID from the local node_key.json.
func readLocalNodeID(homeDir string) (string, error) {
	path := filepath.Join(homeDir, "config", "node_key.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("set-genesis-peers: reading node_key.json: %w", err)
	}

	var nodeKey struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &nodeKey); err != nil {
		return "", fmt.Errorf("set-genesis-peers: parsing node_key.json: %w", err)
	}
	if nodeKey.ID == "" {
		return "", fmt.Errorf("set-genesis-peers: node_key.json has empty id")
	}
	return nodeKey.ID, nil
}
