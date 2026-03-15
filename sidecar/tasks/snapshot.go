package tasks

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var restoreLog = seilog.NewLogger("seictl", "task", "snapshot-restore")

const snapshotMarkerFile = ".sei-sidecar-snapshot-done"

// S3GetObjectAPI abstracts the S3 GetObject call for testing.
type S3GetObjectAPI interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3ClientFactory builds an S3 client for a given region.
type S3ClientFactory func(ctx context.Context, region string) (S3GetObjectAPI, error)

// DefaultS3ClientFactory creates a real S3 client using IRSA credentials.
func DefaultS3ClientFactory(ctx context.Context, region string) (S3GetObjectAPI, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return s3.NewFromConfig(cfg), nil
}

// S3TransferClient abstracts the transfer manager GetObject call for testing.
// The transfer manager's GetObject downloads byte ranges in parallel and
// reassembles them into a sequential io.Reader, giving us parallel throughput
// with a streaming API.
type S3TransferClient interface {
	GetObject(ctx context.Context, input *transfermanager.GetObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.GetObjectOutput, error)
}

// S3TransferClientFactory builds an S3TransferClient for a given region.
type S3TransferClientFactory func(ctx context.Context, region string) (S3TransferClient, error)

// DefaultS3TransferClientFactory creates a transfermanager.Client backed by a real S3 client.
func DefaultS3TransferClientFactory(ctx context.Context, region string) (S3TransferClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return transfermanager.New(s3.NewFromConfig(cfg)), nil
}

// SnapshotConfig holds S3 coordinates for snapshot download.
type SnapshotConfig struct {
	Bucket  string
	Prefix  string
	Region  string
	ChainID string
}

// SnapshotRestorer downloads and extracts a snapshot archive from S3.
type SnapshotRestorer struct {
	homeDir                 string
	s3TransferClientFactory S3TransferClientFactory
}

// NewSnapshotRestorer creates a restorer targeting the given home directory.
func NewSnapshotRestorer(homeDir string, factory S3TransferClientFactory) *SnapshotRestorer {
	if factory == nil {
		factory = DefaultS3TransferClientFactory
	}
	return &SnapshotRestorer{
		homeDir:                 homeDir,
		s3TransferClientFactory: factory,
	}
}

// Handler returns an engine.TaskHandler that adapts the map[string]any params
// to a typed SnapshotConfig and delegates to Restore.
func (r *SnapshotRestorer) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		cfg, err := parseSnapshotConfig(params)
		if err != nil {
			return err
		}
		return r.Restore(ctx, cfg)
	}
}

// Restore downloads and extracts the snapshot, skipping if the marker file exists.
// It reads latest.txt from the prefix to resolve the current snapshot object key.
// The transfer manager downloads byte ranges in parallel behind the scenes,
// presenting the data as a sequential io.Reader for streaming extraction.
func (r *SnapshotRestorer) Restore(ctx context.Context, cfg SnapshotConfig) error {
	if markerExists(r.homeDir, snapshotMarkerFile) {
		restoreLog.Debug("already completed, skipping")
		return nil
	}

	tmClient, err := r.s3TransferClientFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("building S3 transfer client: %w", err)
	}

	snapshotKey, err := resolveSnapshotKey(ctx, tmClient, cfg)
	if err != nil {
		return err
	}

	restoreLog.Info("downloading snapshot", "bucket", cfg.Bucket, "key", snapshotKey)
	output, err := tmClient.GetObject(ctx, &transfermanager.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(snapshotKey),
	})
	if err != nil {
		return fmt.Errorf("s3 GetObject %s: %w", snapshotKey, err)
	}

	snapshotDir := filepath.Join(r.homeDir, "data", "snapshots")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return fmt.Errorf("creating snapshot directory: %w", err)
	}

	restoreLog.Info("extracting archive", "dest", snapshotDir)
	if err := extractTarStream(ctx, output.Body, snapshotDir); err != nil {
		return fmt.Errorf("extracting snapshot: %w", err)
	}

	restoreLog.Info("restore complete")
	return writeMarker(r.homeDir, snapshotMarkerFile)
}

// resolveSnapshotKey reads <prefix>latest.txt to find the current snapshot object key.
func resolveSnapshotKey(ctx context.Context, tmClient S3TransferClient, cfg SnapshotConfig) (string, error) {
	out, err := tmClient.GetObject(ctx, &transfermanager.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(cfg.Prefix + "latest.txt"),
	})
	if err != nil {
		return "", fmt.Errorf("reading latest.txt: %w", err)
	}

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("reading latest.txt body: %w", err)
	}

	height := strings.TrimSpace(string(data))
	if height == "" {
		return "", fmt.Errorf("latest.txt is empty")
	}

	// Snapshot files follow the naming convention:
	// snapshot_<height>_<chainId>_<region>.tar.gz
	filename := fmt.Sprintf("snapshot_%s_%s_%s.tar.gz", height, cfg.ChainID, cfg.Region)
	return cfg.Prefix + filename, nil
}

func parseSnapshotConfig(params map[string]any) (SnapshotConfig, error) {
	bucket, _ := params["bucket"].(string)
	prefix, _ := params["prefix"].(string)
	region, _ := params["region"].(string)
	chainID, _ := params["chainId"].(string)

	if bucket == "" {
		return SnapshotConfig{}, fmt.Errorf("snapshot-restore: missing required param 'bucket'")
	}
	if prefix == "" {
		return SnapshotConfig{}, fmt.Errorf("snapshot-restore: missing required param 'prefix'")
	}
	if region == "" {
		return SnapshotConfig{}, fmt.Errorf("snapshot-restore: missing required param 'region'")
	}
	if chainID == "" {
		return SnapshotConfig{}, fmt.Errorf("snapshot-restore: missing required param 'chainId'")
	}

	return SnapshotConfig{Bucket: bucket, Prefix: prefix, Region: region, ChainID: chainID}, nil
}

// extractTarStream reads a gzip-compressed tar archive from r and extracts
// entries to destDir.
func extractTarStream(ctx context.Context, r io.Reader, destDir string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gzr.Close() }()
	tr := tar.NewReader(gzr)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar header: %w", err)
		}

		target := filepath.Join(destDir, filepath.Clean(header.Name))

		// Path traversal guard: reject entries that escape destDir.
		if !isInsideDir(target, destDir) {
			return fmt.Errorf("tar entry %q escapes destination directory", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)|0o700); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := extractFile(tr, target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			linkTarget := filepath.Join(filepath.Dir(target), header.Linkname)
			if !isInsideDir(linkTarget, destDir) {
				return fmt.Errorf("symlink %q points outside destination directory", header.Name)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("creating symlink %s: %w", target, err)
			}
		default:
			// Skip unsupported entry types (block devices, char devices, etc.)
		}
	}
}

func extractFile(r io.Reader, path string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating parent directory for %s: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return fmt.Errorf("creating file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("writing file %s: %w", path, err)
	}
	return nil
}

// isInsideDir checks that target is within or equal to baseDir.
func isInsideDir(target, baseDir string) bool {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return false
	}
	if absTarget == absBase {
		return true
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return len(rel) > 0 && rel[0] != '.'
}

func markerExists(homeDir, name string) bool {
	_, err := os.Stat(filepath.Join(homeDir, name))
	return err == nil
}

func writeMarker(homeDir, name string) error {
	path := filepath.Join(homeDir, name)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("writing marker file %s: %w", path, err)
	}
	return f.Close()
}
