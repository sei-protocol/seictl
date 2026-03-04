package tasks

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// mockS3Client implements S3GetObjectAPI for testing.
// responses maps S3 object keys to their body bytes.
// If a key is not in responses, errDefault is returned.
type mockS3Client struct {
	responses  map[string][]byte
	errDefault error
}

func (m *mockS3Client) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := ""
	if in.Key != nil {
		key = *in.Key
	}
	if body, ok := m.responses[key]; ok {
		return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
	}
	if m.errDefault != nil {
		return nil, m.errDefault
	}
	return nil, fmt.Errorf("unexpected key: %s", key)
}

func mockS3Factory(client S3GetObjectAPI) S3ClientFactory {
	return func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return client, nil
	}
}

// buildTarGzArchive creates a gzip-compressed tar archive with the given file entries.
func buildTarGzArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatalf("writing tar header for %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content for %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestSnapshotRestoreExtractsArchive(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"data/chain.db": "chaindata",
		"config.toml":   "[p2p]\npersistent_peers = \"\"",
	})

	client := &mockS3Client{
		responses: map[string][]byte{
			"snapshots/latest.txt": []byte("100000000"),
			"snapshots/snapshot_100000000_testchain_us-east-1.tar.gz": archive,
		},
	}
	restorer := NewSnapshotRestorer(homeDir, mockS3Factory(client))
	err := restorer.Restore(context.Background(), SnapshotConfig{
		Bucket:  "test-bucket",
		Prefix:  "snapshots/",
		Region:  "us-east-1",
		ChainID: "testchain",
	})
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(homeDir, "data", "chain.db"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(content) != "chaindata" {
		t.Fatalf("expected 'chaindata', got %q", string(content))
	}

	if !markerExists(homeDir, snapshotMarkerFile) {
		t.Fatal("marker file should exist after successful restore")
	}
}

func TestSnapshotRestoreSkipsWhenMarkerExists(t *testing.T) {
	homeDir := t.TempDir()

	// Pre-create the marker file.
	if err := writeMarker(homeDir, snapshotMarkerFile); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	// S3 client that would fail if called — proves we skip the download.
	restorer := NewSnapshotRestorer(homeDir, mockS3Factory(&mockS3Client{
		errDefault: fmt.Errorf("should not be called"),
	}))

	err := restorer.Restore(context.Background(), SnapshotConfig{
		Bucket:  "test-bucket",
		Prefix:  "snapshots/",
		Region:  "us-east-1",
		ChainID: "testchain",
	})
	if err != nil {
		t.Fatalf("expected nil error when marker exists, got: %v", err)
	}
}

func TestSnapshotRestoreNoMarkerOnLatestTxtError(t *testing.T) {
	homeDir := t.TempDir()

	restorer := NewSnapshotRestorer(homeDir, mockS3Factory(&mockS3Client{
		errDefault: fmt.Errorf("access denied"),
	}))

	err := restorer.Restore(context.Background(), SnapshotConfig{
		Bucket:  "test-bucket",
		Prefix:  "snapshots/",
		Region:  "us-east-1",
		ChainID: "testchain",
	})
	if err == nil {
		t.Fatal("expected error on S3 failure")
	}

	if markerExists(homeDir, snapshotMarkerFile) {
		t.Fatal("marker file should not exist after failed restore")
	}
}

func TestSnapshotRestoreNoMarkerOnDownloadError(t *testing.T) {
	homeDir := t.TempDir()

	client := &mockS3Client{
		responses: map[string][]byte{
			"snapshots/latest.txt": []byte("100000000"),
		},
		errDefault: fmt.Errorf("access denied"),
	}
	restorer := NewSnapshotRestorer(homeDir, mockS3Factory(client))

	err := restorer.Restore(context.Background(), SnapshotConfig{
		Bucket:  "test-bucket",
		Prefix:  "snapshots/",
		Region:  "us-east-1",
		ChainID: "testchain",
	})
	if err == nil {
		t.Fatal("expected error on snapshot download failure")
	}

	if markerExists(homeDir, snapshotMarkerFile) {
		t.Fatal("marker file should not exist after failed restore")
	}
}

func TestSnapshotRestoreRejectsPathTraversal(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"../../etc/passwd": "malicious",
	})

	client := &mockS3Client{
		responses: map[string][]byte{
			"snapshots/latest.txt": []byte("100000000"),
			"snapshots/snapshot_100000000_testchain_us-east-1.tar.gz": archive,
		},
	}
	restorer := NewSnapshotRestorer(homeDir, mockS3Factory(client))
	err := restorer.Restore(context.Background(), SnapshotConfig{
		Bucket:  "test-bucket",
		Prefix:  "snapshots/",
		Region:  "us-east-1",
		ChainID: "testchain",
	})
	if err == nil {
		t.Fatal("expected error for path traversal attempt")
	}
}

func TestSnapshotHandlerParamValidation(t *testing.T) {
	restorer := NewSnapshotRestorer(t.TempDir(), nil)
	handler := restorer.Handler()

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"missing bucket", map[string]any{"prefix": "snapshots/", "region": "us-east-1", "chainId": "c"}},
		{"missing prefix", map[string]any{"bucket": "b", "region": "us-east-1", "chainId": "c"}},
		{"missing region", map[string]any{"bucket": "b", "prefix": "snapshots/", "chainId": "c"}},
		{"missing chainId", map[string]any{"bucket": "b", "prefix": "snapshots/", "region": "us-east-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler(context.Background(), tt.params)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
