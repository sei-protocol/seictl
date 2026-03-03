package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ---------------------------------------------------------------------------
// Mock S3 upload client
// ---------------------------------------------------------------------------

type mockS3UploadClient struct {
	uploads map[string][]byte
}

func newMockS3UploadClient() *mockS3UploadClient {
	return &mockS3UploadClient{uploads: make(map[string][]byte)}
}

func (m *mockS3UploadClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	var buf bytes.Buffer
	if input.Body != nil {
		buf.ReadFrom(input.Body)
	}
	key := *input.Bucket + "/" + *input.Key
	m.uploads[key] = buf.Bytes()
	return &s3.PutObjectOutput{}, nil
}

func mockUploadFactory(client *mockS3UploadClient) S3UploadClientFactory {
	return func(_ context.Context, _ string) (S3UploadClient, error) {
		return client, nil
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Tests: pickUploadCandidate
// ---------------------------------------------------------------------------

func TestPickUploadCandidate_TwoSnapshots_ReturnsSecondToLatest(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 2000})

	h, err := pickUploadCandidate(filepath.Join(homeDir, "data", "snapshots"))
	if err != nil {
		t.Fatalf("pickUploadCandidate() error = %v", err)
	}
	if h != 1000 {
		t.Errorf("height = %d, want 1000", h)
	}
}

func TestPickUploadCandidate_ThreeSnapshots_ReturnsSecondToLatest(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 3000, 2000})

	h, err := pickUploadCandidate(filepath.Join(homeDir, "data", "snapshots"))
	if err != nil {
		t.Fatalf("pickUploadCandidate() error = %v", err)
	}
	if h != 2000 {
		t.Errorf("height = %d, want 2000", h)
	}
}

func TestPickUploadCandidate_SingleSnapshot_ReturnsZero(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000})

	h, err := pickUploadCandidate(filepath.Join(homeDir, "data", "snapshots"))
	if err != nil {
		t.Fatalf("pickUploadCandidate() error = %v", err)
	}
	if h != 0 {
		t.Errorf("height = %d, want 0", h)
	}
}

func TestPickUploadCandidate_NoSnapshots_ReturnsZero(t *testing.T) {
	homeDir := t.TempDir()
	h, err := pickUploadCandidate(filepath.Join(homeDir, "data", "snapshots"))
	if err != nil {
		t.Fatalf("pickUploadCandidate() error = %v", err)
	}
	if h != 0 {
		t.Errorf("height = %d, want 0", h)
	}
}

// ---------------------------------------------------------------------------
// Tests: Upload
// ---------------------------------------------------------------------------

func TestUpload_UploadsArchiveAndLatestTxt(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000, 2000})

	mock := newMockS3UploadClient()
	uploader := NewSnapshotUploader(homeDir, mockUploadFactory(mock))

	err := uploader.Upload(context.Background(), SnapshotUploadConfig{
		Bucket: "my-bucket",
		Prefix: "state-sync",
		Region: "eu-central-1",
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if _, ok := mock.uploads["my-bucket/state-sync/1000.tar.gz"]; !ok {
		t.Error("expected archive upload at state-sync/1000.tar.gz")
	}

	latest, ok := mock.uploads["my-bucket/state-sync/latest.txt"]
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

	// Pre-seed the upload state with height 1000 already uploaded.
	state := uploadState{LastUploadedHeight: 1000}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(homeDir, uploadStateFile), data, 0o644)

	mock := newMockS3UploadClient()
	uploader := NewSnapshotUploader(homeDir, mockUploadFactory(mock))

	err := uploader.Upload(context.Background(), SnapshotUploadConfig{
		Bucket: "my-bucket",
		Prefix: "state-sync",
		Region: "eu-central-1",
	})
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

	// Pre-seed: height 1000 was uploaded. Now 2000 is the candidate.
	state := uploadState{LastUploadedHeight: 1000}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(homeDir, uploadStateFile), data, 0o644)

	mock := newMockS3UploadClient()
	uploader := NewSnapshotUploader(homeDir, mockUploadFactory(mock))

	err := uploader.Upload(context.Background(), SnapshotUploadConfig{
		Bucket: "my-bucket",
		Prefix: "state-sync",
		Region: "eu-central-1",
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if _, ok := mock.uploads["my-bucket/state-sync/2000.tar.gz"]; !ok {
		t.Error("expected archive upload at state-sync/2000.tar.gz")
	}
}

func TestUpload_NoOpsWhenTooFewSnapshots(t *testing.T) {
	homeDir := t.TempDir()
	setupSnapshotDirs(t, homeDir, []int64{1000})

	mock := newMockS3UploadClient()
	uploader := NewSnapshotUploader(homeDir, mockUploadFactory(mock))

	err := uploader.Upload(context.Background(), SnapshotUploadConfig{
		Bucket: "my-bucket",
		Prefix: "state-sync",
		Region: "eu-central-1",
	})
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

	mock := newMockS3UploadClient()
	uploader := NewSnapshotUploader(homeDir, mockUploadFactory(mock))

	err := uploader.Upload(context.Background(), SnapshotUploadConfig{
		Bucket: "my-bucket",
		Prefix: "state-sync",
		Region: "eu-central-1",
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	state := uploader.readUploadState()
	if state.LastUploadedHeight != 1000 {
		t.Errorf("LastUploadedHeight = %d, want 1000", state.LastUploadedHeight)
	}
}

// ---------------------------------------------------------------------------
// Tests: normalizePrefix
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Tests: parseUploadConfig
// ---------------------------------------------------------------------------

func TestParseUploadConfig_Valid(t *testing.T) {
	params := map[string]any{
		"bucket": "my-bucket",
		"prefix": "state-sync",
		"region": "eu-central-1",
	}
	cfg, err := parseUploadConfig(params)
	if err != nil {
		t.Fatalf("parseUploadConfig() error = %v", err)
	}
	if cfg.Bucket != "my-bucket" {
		t.Errorf("Bucket = %q, want %q", cfg.Bucket, "my-bucket")
	}
	if cfg.Prefix != "state-sync" {
		t.Errorf("Prefix = %q, want %q", cfg.Prefix, "state-sync")
	}
	if cfg.Region != "eu-central-1" {
		t.Errorf("Region = %q, want %q", cfg.Region, "eu-central-1")
	}
}

func TestParseUploadConfig_MissingBucket(t *testing.T) {
	_, err := parseUploadConfig(map[string]any{"region": "us-east-1"})
	if err == nil {
		t.Fatal("expected error for missing bucket")
	}
}

func TestParseUploadConfig_MissingRegion(t *testing.T) {
	_, err := parseUploadConfig(map[string]any{"bucket": "my-bucket"})
	if err == nil {
		t.Fatal("expected error for missing region")
	}
}

func TestParseUploadConfig_PrefixOptional(t *testing.T) {
	cfg, err := parseUploadConfig(map[string]any{
		"bucket": "my-bucket",
		"region": "us-east-1",
	})
	if err != nil {
		t.Fatalf("parseUploadConfig() error = %v", err)
	}
	if cfg.Prefix != "" {
		t.Errorf("Prefix = %q, want empty", cfg.Prefix)
	}
}
