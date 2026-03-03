package tasks

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
)

const uploadStateFile = ".sei-sidecar-last-upload.json"

// S3PutObjectAPI abstracts the S3 PutObject call for testing.
type S3PutObjectAPI interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3UploadClient combines get and put operations needed by the upload task.
type S3UploadClient interface {
	S3PutObjectAPI
}

// S3UploadClientFactory builds an S3 upload client for a given region.
type S3UploadClientFactory func(ctx context.Context, region string) (S3UploadClient, error)

// DefaultS3UploadClientFactory creates a real S3 client using IRSA credentials.
func DefaultS3UploadClientFactory(ctx context.Context, region string) (S3UploadClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return s3.NewFromConfig(cfg), nil
}

// SnapshotUploadConfig holds the parameters for the snapshot upload task.
type SnapshotUploadConfig struct {
	Bucket string
	Prefix string
	Region string
}

// uploadState tracks the last successfully uploaded snapshot height.
type uploadState struct {
	LastUploadedHeight int64 `json:"lastUploadedHeight"`
}

// SnapshotUploader scans for locally produced Tendermint state-sync snapshots
// and uploads new ones to S3.
type SnapshotUploader struct {
	homeDir             string
	s3UploadClientFactory S3UploadClientFactory
}

// NewSnapshotUploader creates an uploader targeting the given home directory.
func NewSnapshotUploader(homeDir string, factory S3UploadClientFactory) *SnapshotUploader {
	if factory == nil {
		factory = DefaultS3UploadClientFactory
	}
	return &SnapshotUploader{
		homeDir:             homeDir,
		s3UploadClientFactory: factory,
	}
}

// Handler returns an engine.TaskHandler for the snapshot-upload task.
func (u *SnapshotUploader) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		cfg, err := parseUploadConfig(params)
		if err != nil {
			return err
		}
		return u.Upload(ctx, cfg)
	}
}

// Upload finds the latest complete snapshot, archives it, and uploads to S3.
// It picks the second-to-latest snapshot height to avoid uploading an
// in-progress snapshot. If the snapshot has already been uploaded (tracked
// via a local state file), it no-ops.
func (u *SnapshotUploader) Upload(ctx context.Context, cfg SnapshotUploadConfig) error {
	snapshotsDir := filepath.Join(u.homeDir, "data", "snapshots")

	height, err := pickUploadCandidate(snapshotsDir)
	if err != nil {
		return err
	}
	if height == 0 {
		return nil // no snapshots available yet
	}

	last := u.readUploadState()
	if last.LastUploadedHeight >= height {
		return nil // already uploaded this or a newer snapshot
	}

	s3Client, err := u.s3UploadClientFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("building S3 client: %w", err)
	}

	prefix := normalizePrefix(cfg.Prefix)

	archiveBuf, err := archiveSnapshot(ctx, snapshotsDir, height)
	if err != nil {
		return fmt.Errorf("archiving snapshot at height %d: %w", height, err)
	}

	archiveKey := fmt.Sprintf("%s%d.tar.gz", prefix, height)
	if err := putObject(ctx, s3Client, cfg.Bucket, archiveKey, archiveBuf.Bytes()); err != nil {
		return fmt.Errorf("uploading %s: %w", archiveKey, err)
	}

	latestKey := prefix + "latest.txt"
	if err := putObject(ctx, s3Client, cfg.Bucket, latestKey, []byte(strconv.FormatInt(height, 10))); err != nil {
		return fmt.Errorf("uploading %s: %w", latestKey, err)
	}

	u.writeUploadState(uploadState{LastUploadedHeight: height})
	return nil
}

// pickUploadCandidate scans the snapshots directory and returns the
// second-to-latest height. This avoids uploading an in-progress snapshot.
// Returns 0 if fewer than 2 snapshots exist.
func pickUploadCandidate(snapshotsDir string) (int64, error) {
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading snapshots directory: %w", err)
	}

	var heights []int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		h, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue // skip non-numeric directories
		}
		heights = append(heights, h)
	}

	if len(heights) < 2 {
		return 0, nil
	}

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	return heights[len(heights)-2], nil
}

// archiveSnapshot creates a tar.gz archive of a snapshot at the given height.
// The archive includes the height directory contents and metadata.db from the
// snapshots root (if it exists).
func archiveSnapshot(ctx context.Context, snapshotsDir string, height int64) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	heightDir := filepath.Join(snapshotsDir, strconv.FormatInt(height, 10))
	if err := addDirToTar(ctx, tw, heightDir, strconv.FormatInt(height, 10)); err != nil {
		return nil, err
	}

	metadataPath := filepath.Join(snapshotsDir, "metadata.db")
	if info, err := os.Stat(metadataPath); err == nil {
		if err := addFileToTar(tw, metadataPath, "metadata.db", info); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar writer: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("closing gzip writer: %w", err)
	}
	return &buf, nil
}

func addDirToTar(ctx context.Context, tw *tar.Writer, dir, base string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		rel, err := filepath.Rel(filepath.Dir(dir), path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

func addFileToTar(tw *tar.Writer, path, name string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func putObject(ctx context.Context, client S3PutObjectAPI, bucket, key string, data []byte) error {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	return err
}

func parseUploadConfig(params map[string]any) (SnapshotUploadConfig, error) {
	bucket, _ := params["bucket"].(string)
	prefix, _ := params["prefix"].(string)
	region, _ := params["region"].(string)

	if bucket == "" {
		return SnapshotUploadConfig{}, fmt.Errorf("snapshot-upload: missing required param 'bucket'")
	}
	if region == "" {
		return SnapshotUploadConfig{}, fmt.Errorf("snapshot-upload: missing required param 'region'")
	}

	return SnapshotUploadConfig{Bucket: bucket, Prefix: prefix, Region: region}, nil
}

func normalizePrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	if !strings.HasSuffix(prefix, "/") {
		return prefix + "/"
	}
	return prefix
}

func (u *SnapshotUploader) readUploadState() uploadState {
	data, err := os.ReadFile(filepath.Join(u.homeDir, uploadStateFile))
	if err != nil {
		return uploadState{}
	}
	var state uploadState
	if err := json.Unmarshal(data, &state); err != nil {
		return uploadState{}
	}
	return state
}

func (u *SnapshotUploader) writeUploadState(state uploadState) {
	data, _ := json.Marshal(state)
	_ = os.WriteFile(filepath.Join(u.homeDir, uploadStateFile), data, 0o644)
}
