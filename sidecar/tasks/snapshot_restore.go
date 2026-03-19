package tasks

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var restoreLog = seilog.NewLogger("seictl", "task", "snapshot-restore")

const snapshotMarkerFile = ".sei-sidecar-snapshot-done"

// SnapshotHeightFile stores the height of the restored snapshot so that
// downstream tasks (e.g. result export) can auto-discover their start point.
const SnapshotHeightFile = ".sei-sidecar-snapshot-height"

// snapshotHeightRe extracts heights from S3 keys like snapshot_198030000_pacific-1_eu-central-1.tar.gz.
var snapshotHeightRe = regexp.MustCompile(`snapshot_(\d+)_`)

// SnapshotConfig holds S3 coordinates for snapshot download.
type SnapshotConfig struct {
	Bucket  string
	Prefix  string
	Region  string
	ChainID string
}

// SnapshotRestorer downloads and extracts a snapshot archive from S3.
type SnapshotRestorer struct {
	homeDir       string
	clientFactory seis3.TransferClientFactory
}

// NewSnapshotRestorer creates a restorer targeting the given home directory.
// Pass nil to use the default AWS transfer manager.
func NewSnapshotRestorer(homeDir string, factory seis3.TransferClientFactory) *SnapshotRestorer {
	if factory == nil {
		factory = seis3.DefaultTransferClientFactory
	}
	return &SnapshotRestorer{
		homeDir:       homeDir,
		clientFactory: factory,
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
// It reads latest.txt to resolve the current snapshot key, then uses the transfer
// manager's DownloadObject (io.WriterAt path) for parallel byte-range downloads
// to a temp file, and finally streams the temp file through gzip+tar extraction.
func (r *SnapshotRestorer) Restore(ctx context.Context, cfg SnapshotConfig) error {
	if markerExists(r.homeDir, snapshotMarkerFile) {
		restoreLog.Debug("already completed, skipping")
		return nil
	}

	client, err := r.clientFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("building S3 transfer client: %w", err)
	}

	snapshotKey, err := resolveSnapshotKey(ctx, client, cfg)
	if err != nil {
		return err
	}

	tmpDir := filepath.Join(r.homeDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("creating tmp directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(tmpDir, "snapshot-*.tar.gz")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	restoreLog.Info("downloading snapshot", "bucket", cfg.Bucket, "key", snapshotKey, "dest", tmpPath)
	_, err = client.DownloadObject(ctx, &transfermanager.DownloadObjectInput{
		Bucket:   aws.String(cfg.Bucket),
		Key:      aws.String(snapshotKey),
		WriterAt: tmpFile,
	})
	_ = tmpFile.Close()
	if err != nil {
		return fmt.Errorf("s3 DownloadObject %s: %w", snapshotKey, err)
	}

	snapshotDir := filepath.Join(r.homeDir, "data", "snapshots")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return fmt.Errorf("creating snapshot directory: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("opening downloaded archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	restoreLog.Info("extracting archive", "dest", snapshotDir)
	if err := extractTarStream(ctx, f, snapshotDir); err != nil {
		return fmt.Errorf("extracting snapshot: %w", err)
	}

	if h := parseHeightFromKey(snapshotKey); h > 0 {
		if err := os.WriteFile(
			filepath.Join(r.homeDir, SnapshotHeightFile),
			[]byte(strconv.FormatInt(h, 10)),
			0o644,
		); err != nil {
			restoreLog.Warn("failed to write snapshot height file", "err", err)
		}
	}

	restoreLog.Info("restore complete")
	return writeMarker(r.homeDir, snapshotMarkerFile)
}

// resolveSnapshotKey reads <prefix>latest.txt to find the current snapshot object key.
func resolveSnapshotKey(ctx context.Context, client seis3.TransferClient, cfg SnapshotConfig) (string, error) {
	var buf seis3.WriteAtBuffer
	_, err := client.DownloadObject(ctx, &transfermanager.DownloadObjectInput{
		Bucket:   aws.String(cfg.Bucket),
		Key:      aws.String(cfg.Prefix + "latest.txt"),
		WriterAt: &buf,
	})
	if err != nil {
		return "", fmt.Errorf("reading latest.txt: %w", err)
	}

	height := strings.TrimSpace(string(buf.Bytes()))
	if height == "" {
		return "", fmt.Errorf("latest.txt is empty")
	}

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

func parseHeightFromKey(key string) int64 {
	m := snapshotHeightRe.FindStringSubmatch(key)
	if len(m) < 2 {
		return 0
	}
	h, _ := strconv.ParseInt(m[1], 10, 64)
	return h
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
