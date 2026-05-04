package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
)

type mockS3Uploader struct {
	uploads map[string][]byte
}

func newMockS3Uploader() *mockS3Uploader {
	return &mockS3Uploader{uploads: make(map[string][]byte)}
}

func (m *mockS3Uploader) UploadObject(_ context.Context, input *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	var buf bytes.Buffer
	if input.Body != nil {
		_, _ = io.Copy(&buf, input.Body)
	}
	key := *input.Bucket + "/" + *input.Key
	m.uploads[key] = buf.Bytes()
	return &transfermanager.UploadObjectOutput{}, nil
}

func mockUploaderFactory(client *mockS3Uploader) seis3.UploaderFactory {
	return func(_ context.Context, _ string) (seis3.Uploader, error) {
		return client, nil
	}
}

func setupSnapshotDirs(t *testing.T, homeDir string, heights []int64) {
	t.Helper()
	snapshotsDir := filepath.Join(homeDir, "data", "snapshots")
	if err := os.MkdirAll(snapshotsDir, 0o755); err != nil {
		t.Fatalf("creating snapshots dir: %v", err)
	}

	for _, h := range heights {
		dir := filepath.Join(snapshotsDir, strconv.FormatInt(h, 10))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating height dir %d: %v", h, err)
		}
		chunkDir := filepath.Join(dir, "1")
		if err := os.MkdirAll(chunkDir, 0o755); err != nil {
			t.Fatalf("creating chunk dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(chunkDir, "0"), []byte("chunk-data"), 0o644); err != nil {
			t.Fatalf("writing chunk file: %v", err)
		}
	}

	// Create metadata.db
	metadataPath := filepath.Join(snapshotsDir, "metadata.db")
	if err := os.WriteFile(metadataPath, []byte("metadata-content"), 0o644); err != nil {
		t.Fatalf("writing metadata.db: %v", err)
	}
}

func TestPickUploadCandidate(t *testing.T) {
	cases := []struct {
		name    string
		heights []int64
		want    int64
	}{
		{"two snapshots returns second to latest", []int64{1000, 2000}, 1000},
		{"three snapshots returns second to latest", []int64{1000, 3000, 2000}, 2000},
		{"single snapshot returns zero", []int64{1000}, 0},
		{"no snapshots returns zero", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tc.heights != nil {
				setupSnapshotDirs(t, homeDir, tc.heights)
			}
			h, err := pickUploadCandidate(filepath.Join(homeDir, "data", "snapshots"))
			if err != nil {
				t.Fatalf("pickUploadCandidate() error = %v", err)
			}
			if h != tc.want {
				t.Errorf("height = %d, want %d", h, tc.want)
			}
		})
	}
}

func TestUpload_UploadsArchiveAndLatestTxt(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 2000})

	mock := newMockS3Uploader()
	uploader := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))

	err := uploader.Upload(context.Background())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if _, ok := mock.uploads["my-bucket/testchain/state-sync/1000.tar.gz"]; !ok {
		t.Error("expected archive upload at testchain/state-sync/1000.tar.gz")
	}

	latest, ok := mock.uploads["my-bucket/testchain/state-sync/latest.txt"]
	if !ok {
		t.Fatal("expected latest.txt upload")
	}
	if string(latest) != "1000" {
		t.Errorf("latest.txt = %q, want %q", string(latest), "1000")
	}
}

func TestUpload_SkipsWhenAlreadyUploaded(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 2000})

	state := uploadState{LastUploadedHeight: 1000}
	data, _ := json.Marshal(state)
	_ = os.WriteFile(filepath.Join(homeDir, uploadStateFile), data, 0o644)

	mock := newMockS3Uploader()
	uploader := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))

	err := uploader.Upload(context.Background())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if len(mock.uploads) != 0 {
		t.Errorf("expected no uploads (already uploaded), got %d", len(mock.uploads))
	}
}

func TestUpload_UploadsNewerSnapshot(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 2000, 3000})

	state := uploadState{LastUploadedHeight: 1000}
	data, _ := json.Marshal(state)
	_ = os.WriteFile(filepath.Join(homeDir, uploadStateFile), data, 0o644)

	mock := newMockS3Uploader()
	uploader := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))

	err := uploader.Upload(context.Background())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if _, ok := mock.uploads["my-bucket/testchain/state-sync/2000.tar.gz"]; !ok {
		t.Error("expected archive upload at testchain/state-sync/2000.tar.gz")
	}
}

func TestUpload_NoOpsWhenTooFewSnapshots(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000})

	mock := newMockS3Uploader()
	uploader := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))

	err := uploader.Upload(context.Background())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if len(mock.uploads) != 0 {
		t.Errorf("expected no uploads, got %d", len(mock.uploads))
	}
}

func TestUpload_WritesUploadState(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 2000})

	mock := newMockS3Uploader()
	uploader := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))

	err := uploader.Upload(context.Background())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	state := uploader.readUploadState()
	if state.LastUploadedHeight != 1000 {
		t.Errorf("LastUploadedHeight = %d, want 1000", state.LastUploadedHeight)
	}
}

func TestNormalizePrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"state-sync", "state-sync/"},
		{"state-sync/", "state-sync/"},
		{"a/b/c", "a/b/c/"},
		{"a/b/c/", "a/b/c/"},
	}
	for _, tt := range tests {
		got := normalizePrefix(tt.input)
		if got != tt.want {
			t.Errorf("normalizePrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
