package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var artifactLog = seilog.NewLogger("seictl", "task", "upload-genesis-artifacts")

const artifactUploadMarkerFile = ".sei-sidecar-artifact-upload-done"

// UploadArtifactsRequest holds the typed parameters for the upload-genesis-artifacts task.
type UploadArtifactsRequest struct {
	Bucket   string `json:"s3Bucket"`
	Prefix   string `json:"s3Prefix"`
	Region   string `json:"s3Region"`
	NodeName string `json:"nodeName"`
}

// GenesisArtifactUploader uploads the gentx file and a node identity
// manifest to S3 so the assembler can collect them.
type GenesisArtifactUploader struct {
	homeDir           string
	s3UploaderFactory seis3.UploaderFactory
}

// NewGenesisArtifactUploader creates an uploader targeting the given home directory.
func NewGenesisArtifactUploader(homeDir string, factory seis3.UploaderFactory) *GenesisArtifactUploader {
	if factory == nil {
		factory = seis3.DefaultUploaderFactory
	}
	return &GenesisArtifactUploader{homeDir: homeDir, s3UploaderFactory: factory}
}

// Handler returns an engine.TaskHandler for the upload-genesis-artifacts task type.
//
// Expected params:
//
//	{"s3Bucket": "...", "s3Prefix": "...", "s3Region": "...", "nodeName": "..."}
//
// Uploads two objects:
//
//	<prefix>/<nodeName>/gentx.json   - the validator's genesis transaction
//	<prefix>/<nodeName>/identity.json - node ID and validator pubkey metadata
func (u *GenesisArtifactUploader) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, cfg UploadArtifactsRequest) error {
		if markerExists(u.homeDir, artifactUploadMarkerFile) {
			artifactLog.Debug("already completed, skipping")
			return nil
		}

		if cfg.Bucket == "" {
			return fmt.Errorf("upload-genesis-artifacts: missing required param 's3Bucket'")
		}
		if cfg.Region == "" {
			return fmt.Errorf("upload-genesis-artifacts: missing required param 's3Region'")
		}
		if cfg.NodeName == "" {
			return fmt.Errorf("upload-genesis-artifacts: missing required param 'nodeName'")
		}

		uploader, err := u.s3UploaderFactory(ctx, cfg.Region)
		if err != nil {
			return fmt.Errorf("upload-genesis-artifacts: building S3 uploader: %w", err)
		}

		prefix := normalizePrefix(cfg.Prefix)
		nodePrefix := prefix + cfg.NodeName + "/"

		if err := u.uploadGentx(ctx, uploader, cfg.Bucket, nodePrefix); err != nil {
			return err
		}

		if err := u.uploadIdentity(ctx, uploader, cfg.Bucket, nodePrefix); err != nil {
			return err
		}

		artifactLog.Info("artifacts uploaded", "bucket", cfg.Bucket, "prefix", nodePrefix)
		return writeMarker(u.homeDir, artifactUploadMarkerFile)
	})
}

// uploadGentx finds the single gentx file in config/gentx/ and uploads it.
func (u *GenesisArtifactUploader) uploadGentx(ctx context.Context, uploader seis3.Uploader, bucket, nodePrefix string) error {
	gentxDir := filepath.Join(u.homeDir, "config", "gentx")
	entries, err := os.ReadDir(gentxDir)
	if err != nil {
		return fmt.Errorf("upload-genesis-artifacts: reading gentx dir: %w", err)
	}

	var gentxFile string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			gentxFile = e.Name()
			break
		}
	}
	if gentxFile == "" {
		return fmt.Errorf("upload-genesis-artifacts: no gentx JSON file found in %s", gentxDir)
	}

	data, err := os.ReadFile(filepath.Join(gentxDir, gentxFile))
	if err != nil {
		return fmt.Errorf("upload-genesis-artifacts: reading %s: %w", gentxFile, err)
	}

	key := nodePrefix + "gentx.json"
	artifactLog.Info("uploading gentx", "key", key)
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("upload-genesis-artifacts: uploading gentx: %w", err)
	}
	return nil
}

// uploadIdentity reads node_key.json, extracts the node ID, and uploads
// a minimal identity manifest. The assembler uses this to know which
// nodes participated.
func (u *GenesisArtifactUploader) uploadIdentity(ctx context.Context, uploader seis3.Uploader, bucket, nodePrefix string) error {
	nodeKeyPath := filepath.Join(u.homeDir, "config", "node_key.json")
	nodeKeyData, err := os.ReadFile(nodeKeyPath)
	if err != nil {
		return fmt.Errorf("upload-genesis-artifacts: reading node_key.json: %w", err)
	}

	identity := map[string]any{
		"node_key": json.RawMessage(nodeKeyData),
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("upload-genesis-artifacts: marshaling identity: %w", err)
	}

	key := nodePrefix + "identity.json"
	artifactLog.Info("uploading identity", "key", key)
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("upload-genesis-artifacts: uploading identity: %w", err)
	}
	return nil
}
