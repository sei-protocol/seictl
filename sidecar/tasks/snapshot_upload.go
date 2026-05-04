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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var uploadLog = seilog.NewLogger("seictl", "task", "snapshot-upload")

const (
	uploadStateFile       = ".sei-sidecar-last-upload.json"
	defaultUploadInterval = 7 * 24 * time.Hour // weekly
)

// SnapshotUploadRequest holds the parameters for the snapshot upload task.
// S3 bucket, region, and prefix are derived from the sidecar's environment.
type SnapshotUploadRequest struct{}

// uploadState tracks the last successfully uploaded snapshot height.
type uploadState struct {
	LastUploadedHeight int64 `json:"lastUploadedHeight"`
}

// SnapshotUploader scans for locally produced Tendermint state-sync snapshots
// and uploads new ones to S3. When submitted as a task, it runs in a loop
// at the configured interval until the context is cancelled.
type SnapshotUploader struct {
	homeDir           string
	bucket            string
	region            string
	chainID           string
	uploadInterval    time.Duration
	s3UploaderFactory seis3.UploaderFactory
}

// NewSnapshotUploader creates an uploader targeting the given home directory.
// Bucket, region, and chainID are read from environment at construction time.
func NewSnapshotUploader(homeDir, bucket, region, chainID string, uploadInterval time.Duration, factory seis3.UploaderFactory) *SnapshotUploader {
	if factory == nil {
		factory = seis3.DefaultUploaderFactory
	}
	if uploadInterval <= 0 {
		uploadInterval = defaultUploadInterval
	}
	return &SnapshotUploader{
		homeDir:           homeDir,
		bucket:            bucket,
		region:            region,
		chainID:           chainID,
		uploadInterval:    uploadInterval,
		s3UploaderFactory: factory,
	}
}

// Handler returns an engine.TaskHandler for the snapshot-upload task.
// The handler runs in a loop, attempting an upload on each tick and
// sleeping for the configured interval between attempts. It stays
// running until the context is cancelled.
func (u *SnapshotUploader) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, _ SnapshotUploadRequest) error {
		return u.runLoop(ctx)
	})
}

func (u *SnapshotUploader) runLoop(ctx context.Context) error {
	uploadLog.Info("starting snapshot upload loop", "interval", u.uploadInterval, "bucket", u.bucket)
	for {
		if err := u.Upload(ctx); err != nil {
			uploadLog.Warn("upload attempt failed, will retry next interval", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(u.uploadInterval):
		}
	}
}

// Upload finds the latest complete snapshot, archives it, and streams it to S3.
// It picks the second-to-latest snapshot height to avoid uploading an
// in-progress snapshot. If the snapshot has already been uploaded (tracked
// via a local state file), it no-ops.
//
// The archive is streamed through an io.Pipe so it never needs to be buffered
// entirely in memory; the transfermanager handles multipart upload automatically.
func (u *SnapshotUploader) Upload(ctx context.Context) error {
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

	uploadLog.Info("uploading snapshot", "height", height, "bucket", u.bucket, "region", u.region)

	uploader, err := u.s3UploaderFactory(ctx, u.region)
	if err != nil {
		return fmt.Errorf("building S3 uploader: %w", err)
	}

	prefix := u.chainID + "/state-sync/"

	archiveKey := fmt.Sprintf("%ssnapshot_%d_%s_%s.tar.gz", prefix, height, u.chainID, u.region)
	uploadLog.Info("streaming archive to S3", "key", archiveKey)
	if err := u.streamUpload(ctx, uploader, u.bucket, archiveKey, snapshotsDir, height); err != nil {
		return fmt.Errorf("uploading %s: %w", archiveKey, err)
	}

	latestKey := prefix + "latest.txt"
	latestBody := []byte(strconv.FormatInt(height, 10))
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(u.bucket),
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
func (u *SnapshotUploader) streamUpload(ctx context.Context, uploader seis3.Uploader, bucket, key, snapshotsDir string, height int64) error {
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
