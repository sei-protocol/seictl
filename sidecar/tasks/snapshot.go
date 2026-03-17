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
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var restoreLog = seilog.NewLogger("seictl", "task", "snapshot-restore")

const snapshotMarkerFile = ".sei-sidecar-snapshot-done"

// SnapshotHeightFile stores the height of the restored snapshot so that
// downstream tasks (e.g. result export) can auto-discover their start point.
const SnapshotHeightFile = ".sei-sidecar-snapshot-height"

// S3TransferClient abstracts S3 downloads and listing. DownloadObject uses
// the transfer manager's io.WriterAt path for parallel byte-range downloads.
// ListObjectsV2 is used for height-based snapshot discovery.
type S3TransferClient interface {
	DownloadObject(ctx context.Context, input *transfermanager.DownloadObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.DownloadObjectOutput, error)
	ListObjectsV2(ctx context.Context, input *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// s3CompositeClient bundles the transfer manager (download) and raw S3 client
// (list) behind the single S3TransferClient interface.
type s3CompositeClient struct {
	tm       *transfermanager.Client
	s3Client *s3.Client
}

func (c *s3CompositeClient) DownloadObject(ctx context.Context, input *transfermanager.DownloadObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.DownloadObjectOutput, error) {
	return c.tm.DownloadObject(ctx, input, opts...)
}

func (c *s3CompositeClient) ListObjectsV2(ctx context.Context, input *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return c.s3Client.ListObjectsV2(ctx, input, opts...)
}

// S3TransferClientFactory builds an S3TransferClient for a given region.
type S3TransferClientFactory func(ctx context.Context, region string) (S3TransferClient, error)

// DefaultS3TransferClientFactory creates a composite client backed by a real
// S3 service client supporting both downloads and listing.
func DefaultS3TransferClientFactory(ctx context.Context, region string) (S3TransferClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(cfg)
	return &s3CompositeClient{
		tm:       transfermanager.New(s3Client),
		s3Client: s3Client,
	}, nil
}

// SnapshotConfig holds S3 coordinates for snapshot download.
type SnapshotConfig struct {
	Bucket       string
	Prefix       string
	Region       string
	ChainID      string
	TargetHeight int64
}

// SnapshotRestorer downloads and extracts a snapshot archive from S3.
type SnapshotRestorer struct {
	homeDir       string
	clientFactory S3TransferClientFactory
}

// NewSnapshotRestorer creates a restorer targeting the given home directory.
// Pass nil to use the default AWS transfer manager.
func NewSnapshotRestorer(homeDir string, factory S3TransferClientFactory) *SnapshotRestorer {
	if factory == nil {
		factory = DefaultS3TransferClientFactory
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

	var snapshotKey string
	if cfg.TargetHeight > 0 {
		snapshotKey, err = resolveSnapshotByHeight(ctx, client, cfg, cfg.TargetHeight)
	} else {
		snapshotKey, err = resolveSnapshotKey(ctx, client, cfg)
	}
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
func resolveSnapshotKey(ctx context.Context, client S3TransferClient, cfg SnapshotConfig) (string, error) {
	var buf writeAtBuffer
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

	var targetHeight int64
	if v, ok := params["targetHeight"].(float64); ok {
		targetHeight = int64(v)
	}

	return SnapshotConfig{Bucket: bucket, Prefix: prefix, Region: region, ChainID: chainID, TargetHeight: targetHeight}, nil
}

// snapshotHeightRe extracts heights from S3 keys like snapshot_198030000_pacific-1_eu-central-1.tar.gz.
var snapshotHeightRe = regexp.MustCompile(`snapshot_(\d+)_`)

func parseHeightFromKey(key string) int64 {
	m := snapshotHeightRe.FindStringSubmatch(key)
	if len(m) < 2 {
		return 0
	}
	h, _ := strconv.ParseInt(m[1], 10, 64)
	return h
}

// resolveSnapshotByHeight lists S3 objects under the configured prefix and
// selects the snapshot with the highest height at or below targetHeight.
func resolveSnapshotByHeight(ctx context.Context, client S3TransferClient, cfg SnapshotConfig, targetHeight int64) (string, error) {
	prefix := normalizePrefix(cfg.Prefix)
	var bestHeight int64
	var bestKey string

	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(cfg.Bucket),
		Prefix: aws.String(prefix),
	}

	for {
		out, err := client.ListObjectsV2(ctx, input)
		if err != nil {
			return "", fmt.Errorf("listing snapshots in s3://%s/%s: %w", cfg.Bucket, prefix, err)
		}
		for _, obj := range out.Contents {
			key := aws.ToString(obj.Key)
			matches := snapshotHeightRe.FindStringSubmatch(key)
			if len(matches) < 2 {
				continue
			}
			h, err := strconv.ParseInt(matches[1], 10, 64)
			if err != nil {
				continue
			}
			if h <= targetHeight && h > bestHeight {
				bestHeight = h
				bestKey = key
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		input.ContinuationToken = out.NextContinuationToken
	}

	if bestKey == "" {
		return "", fmt.Errorf("no snapshot found at or below height %d in s3://%s/%s", targetHeight, cfg.Bucket, prefix)
	}

	restoreLog.Info("resolved snapshot by height",
		"target", targetHeight,
		"selected", bestHeight,
		"key", bestKey)
	return bestKey, nil
}

// writeAtBuffer is a goroutine-safe in-memory io.WriterAt, used for
// downloading small S3 objects (e.g. latest.txt) via DownloadObject.
type writeAtBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (w *writeAtBuffer) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	end := int(off) + len(p)
	if end > len(w.buf) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[off:], p)
	return len(p), nil
}

func (w *writeAtBuffer) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf
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
