package tasks

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	uploader, err := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))
	if err != nil {
		t.Fatalf("NewSnapshotUploader: %v", err)
	}

	err = uploader.Upload(context.Background())
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
	uploader, err := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))
	if err != nil {
		t.Fatalf("NewSnapshotUploader: %v", err)
	}

	err = uploader.Upload(context.Background())
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
	uploader, err := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))
	if err != nil {
		t.Fatalf("NewSnapshotUploader: %v", err)
	}

	err = uploader.Upload(context.Background())
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
	uploader, err := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))
	if err != nil {
		t.Fatalf("NewSnapshotUploader: %v", err)
	}

	err = uploader.Upload(context.Background())
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
	uploader, err := NewSnapshotUploader(homeDir, "my-bucket", "eu-central-1", "testchain", 0, mockUploaderFactory(mock))
	if err != nil {
		t.Fatalf("NewSnapshotUploader: %v", err)
	}

	err = uploader.Upload(context.Background())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	state := uploader.readUploadState()
	if state.LastUploadedHeight != 1000 {
		t.Errorf("LastUploadedHeight = %d, want 1000", state.LastUploadedHeight)
	}
}

// readTarGzNames decompresses + reads a tar.gz produced by writeArchive
// and returns the entry names + their typeflags.
func readTarGzNames(t *testing.T, body []byte) map[string]byte {
	t.Helper()
	gzr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gzr.Close() }()
	tr := tar.NewReader(gzr)
	out := map[string]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		out[h.Name] = h.Typeflag
	}
	return out
}

// runWriteArchive captures writeArchive output via an io.Pipe.
func runWriteArchive(t *testing.T, snapshotsDir string, height int64) []byte {
	t.Helper()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- writeArchive(context.Background(), pw, snapshotsDir, height)
	}()
	body, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("writeArchive error: %v", err)
	}
	return body
}

func TestWriteArchive_MetadataAsDirectory(t *testing.T) {
	homeDir := t.TempDir()
	snapshotsDir := filepath.Join(homeDir, "data", "snapshots")
	if err := os.MkdirAll(filepath.Join(snapshotsDir, "1000", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotsDir, "1000", "1", "0"), []byte("chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	// metadata.db as a directory containing typical LevelDB files
	mdDir := filepath.Join(snapshotsDir, "metadata.db")
	if err := os.MkdirAll(mdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdDir, "CURRENT"), []byte("MANIFEST-000001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdDir, "MANIFEST-000001"), []byte("manifest-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := runWriteArchive(t, snapshotsDir, 1000)
	names := readTarGzNames(t, body)

	// Height directory entries present and recursive
	if _, ok := names["1000/1/0"]; !ok {
		t.Errorf("missing height-dir file 1000/1/0 in archive; entries: %v", names)
	}
	// metadata.db directory header + recursive contents
	if tf, ok := names["metadata.db"]; !ok || tf != tar.TypeDir {
		t.Errorf("metadata.db missing or not a directory entry (typeflag=%d)", tf)
	}
	if _, ok := names["metadata.db/CURRENT"]; !ok {
		t.Errorf("missing metadata.db/CURRENT in archive; entries: %v", names)
	}
	if _, ok := names["metadata.db/MANIFEST-000001"]; !ok {
		t.Errorf("missing metadata.db/MANIFEST-000001 in archive; entries: %v", names)
	}
}

func TestWriteArchive_MetadataAsFile(t *testing.T) {
	homeDir := t.TempDir()
	snapshotsDir := filepath.Join(homeDir, "data", "snapshots")
	if err := os.MkdirAll(filepath.Join(snapshotsDir, "1000", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotsDir, "1000", "1", "0"), []byte("chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotsDir, "metadata.db"), []byte("metadata-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := runWriteArchive(t, snapshotsDir, 1000)
	names := readTarGzNames(t, body)

	if tf, ok := names["metadata.db"]; !ok || tf != tar.TypeReg {
		t.Errorf("metadata.db missing or not a regular file (typeflag=%d)", tf)
	}
	if _, ok := names["1000/1/0"]; !ok {
		t.Errorf("missing height-dir file 1000/1/0 in archive; entries: %v", names)
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
