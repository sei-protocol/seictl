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
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var uploadLog = seilog.NewLogger("seictl", "task", "snapshot-upload")

const uploadStateFile = ".sei-sidecar-last-upload.json"

// S3Uploader abstracts the transfermanager upload call for testing.
type S3Uploader interface {
	UploadObject(ctx context.Context, input *transfermanager.UploadObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error)
}

// S3UploaderFactory builds an S3Uploader for a given region.
type S3UploaderFactory func(ctx context.Context, region string) (S3Uploader, error)

// DefaultS3UploaderFactory creates a transfermanager.Client backed by a real S3 client.
func DefaultS3UploaderFactory(ctx context.Context, region string) (S3Uploader, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return transfermanager.New(s3.NewFromConfig(cfg)), nil
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
	homeDir           string
	s3UploaderFactory S3UploaderFactory
}

// NewSnapshotUploader creates an uploader targeting the given home directory.
func NewSnapshotUploader(homeDir string, factory S3UploaderFactory) *SnapshotUploader {
	if factory == nil {
		factory = DefaultS3UploaderFactory
	}
	return &SnapshotUploader{
		homeDir:           homeDir,
		s3UploaderFactory: factory,
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

// Upload finds the latest complete snapshot, archives it, and streams it to S3.
// It picks the second-to-latest snapshot height to avoid uploading an
// in-progress snapshot. If the snapshot has already been uploaded (tracked
// via a local state file), it no-ops.
//
// The archive is streamed through an io.Pipe so it never needs to be buffered
// entirely in memory; the transfermanager handles multipart upload automatically.
func (u *SnapshotUploader) Upload(ctx context.Context, cfg SnapshotUploadConfig) error {
	snapshotsDir := filepath.Join(u.homeDir, "data", "snapshots")

	height, err := pickUploadCandidate(snapshotsDir)
	if err != nil {
		return err
	}
	if height == 0 {
		uploadLog.Debug("fewer than 2 snapshots on disk, nothing to upload")
		return nil
	}

	last := u.readUploadState()
	if last.LastUploadedHeight >= height {
		uploadLog.Debug("height already uploaded", "height", height, "last-uploaded", last.LastUploadedHeight)
		return nil
	}

	uploadLog.Info("uploading snapshot", "height", height, "bucket", cfg.Bucket, "region", cfg.Region)

	uploader, err := u.s3UploaderFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("building S3 uploader: %w", err)
	}

	prefix := normalizePrefix(cfg.Prefix)

	archiveKey := fmt.Sprintf("%s%d.tar.gz", prefix, height)
	uploadLog.Info("streaming archive to S3", "key", archiveKey)
	if err := u.streamUpload(ctx, uploader, cfg.Bucket, archiveKey, snapshotsDir, height); err != nil {
		return fmt.Errorf("uploading %s: %w", archiveKey, err)
	}

	latestKey := prefix + "latest.txt"
	latestBody := []byte(strconv.FormatInt(height, 10))
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(latestKey),
		Body:   bytes.NewReader(latestBody),
	})
	if err != nil {
		return fmt.Errorf("uploading %s: %w", latestKey, err)
	}
	uploadLog.Info("updated latest.txt", "key", latestKey, "height", height)

	return u.writeUploadState(uploadState{LastUploadedHeight: height})
}

// streamUpload pipes a tar.gz archive directly into the transfermanager,
// avoiding in-memory buffering of the full archive.
func (u *SnapshotUploader) streamUpload(ctx context.Context, uploader S3Uploader, bucket, key, snapshotsDir string, height int64) error {
	pr, pw := io.Pipe()

	archiveErr := make(chan error, 1)
	go func() {
		archiveErr <- writeArchive(ctx, pw, snapshotsDir, height)
	}()

	_, uploadErr := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        pr,
		ContentType: aws.String("application/gzip"),
	})

	if uploadErr != nil {
		// Unblock the archiver goroutine if it's still writing.
		pr.CloseWithError(uploadErr)
	}

	aErr := <-archiveErr
	if uploadErr != nil {
		return uploadErr
	}
	return aErr
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

// writeArchive streams a tar.gz archive of the snapshot at the given height
// into wc (typically the write half of an io.Pipe). It always closes wc when
// done, propagating any archiving error so the reader side sees it.
func writeArchive(ctx context.Context, wc io.WriteCloser, snapshotsDir string, height int64) (retErr error) {
	defer func() {
		if retErr != nil {
			wc.(*io.PipeWriter).CloseWithError(retErr)
		} else {
			_ = wc.Close()
		}
	}()

	gw := gzip.NewWriter(wc)
	tw := tar.NewWriter(gw)

	heightDir := filepath.Join(snapshotsDir, strconv.FormatInt(height, 10))
	if err := addDirToTar(ctx, tw, heightDir, strconv.FormatInt(height, 10)); err != nil {
		return err
	}

	metadataPath := filepath.Join(snapshotsDir, "metadata.db")
	if info, err := os.Stat(metadataPath); err == nil {
		if err := addFileToTar(tw, metadataPath, "metadata.db", info); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar writer: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("closing gzip writer: %w", err)
	}
	return nil
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
		defer func() { _ = f.Close() }()
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
	defer func() { _ = f.Close() }()
	_, err = io.Copy(tw, f)
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

func (u *SnapshotUploader) writeUploadState(state uploadState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling upload state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(u.homeDir, uploadStateFile), data, 0o644); err != nil {
		return fmt.Errorf("writing upload state: %w", err)
	}
	return nil
}
