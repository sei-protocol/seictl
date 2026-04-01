package tasks

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
)

// mockTransferClient implements seis3.TransferClient for testing.
type mockTransferClient struct {
	responses  map[string][]byte
	errDefault error
}

func (m *mockTransferClient) DownloadObject(_ context.Context, in *transfermanager.DownloadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.DownloadObjectOutput, error) {
	key := ""
	if in.Key != nil {
		key = *in.Key
	}
	if body, ok := m.responses[key]; ok {
		if _, err := in.WriterAt.WriteAt(body, 0); err != nil {
			return nil, fmt.Errorf("writing to WriterAt: %w", err)
		}
		return &transfermanager.DownloadObjectOutput{}, nil
	}
	if m.errDefault != nil {
		return nil, m.errDefault
	}
	return nil, fmt.Errorf("unexpected key: %s", key)
}

func mockClientFactory(client seis3.TransferClient) seis3.TransferClientFactory {
	return func(_ context.Context, _ string) (seis3.TransferClient, error) {
		return client, nil
	}
}

// mockObjectLister implements seis3.ObjectLister for testing.
// pageSize controls pagination — 0 means return all keys in one page.
type mockObjectLister struct {
	keys     []string
	pageSize int
}

func (m *mockObjectLister) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	pageSize := m.pageSize
	if pageSize <= 0 {
		pageSize = len(m.keys)
	}

	startIdx := 0
	if input.ContinuationToken != nil {
		for i, k := range m.keys {
			if k == *input.ContinuationToken {
				startIdx = i
				break
			}
		}
	}

	end := startIdx + pageSize
	if end > len(m.keys) {
		end = len(m.keys)
	}

	var contents []types.Object
	for _, k := range m.keys[startIdx:end] {
		key := k
		contents = append(contents, types.Object{Key: &key})
	}

	truncated := end < len(m.keys)
	var nextToken *string
	if truncated {
		nextToken = &m.keys[end]
	}

	return &s3.ListObjectsV2Output{
		Contents:              contents,
		IsTruncated:           &truncated,
		NextContinuationToken: nextToken,
	}, nil
}

func mockListerFactory(lister seis3.ObjectLister) seis3.ObjectListerFactory {
	return func(_ context.Context, _ string) (seis3.ObjectLister, error) {
		return lister, nil
	}
}

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
	})

	client := &mockTransferClient{
		responses: map[string][]byte{
			"testchain/latest.txt":       []byte("100000000"),
			"testchain/100000000.tar.gz": archive,
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "test-bucket", "us-east-1", "testchain", mockClientFactory(client), nil)
	if err := restorer.Restore(context.Background(), 0); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(homeDir, "data", "snapshots", "data", "chain.db"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(content) != "chaindata" {
		t.Fatalf("expected 'chaindata', got %q", string(content))
	}

	if !markerExists(homeDir, restoreMarkerFile) {
		t.Fatal("marker file should exist after successful restore")
	}
}

func TestSnapshotRestoreSkipsWhenMarkerExists(t *testing.T) {
	homeDir := t.TempDir()
	if err := writeMarker(homeDir, restoreMarkerFile); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(&mockTransferClient{
		errDefault: fmt.Errorf("should not be called"),
	}), nil)

	if err := restorer.Restore(context.Background(), 0); err != nil {
		t.Fatalf("expected nil error when marker exists, got: %v", err)
	}
}

func TestSnapshotRestoreNoMarkerOnLatestTxtError(t *testing.T) {
	homeDir := t.TempDir()
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(&mockTransferClient{
		errDefault: fmt.Errorf("access denied"),
	}), nil)

	if err := restorer.Restore(context.Background(), 0); err == nil {
		t.Fatal("expected error on S3 failure")
	}

	if markerExists(homeDir, restoreMarkerFile) {
		t.Fatal("marker file should not exist after failed restore")
	}
}

func TestSnapshotRestoreNoMarkerOnDownloadError(t *testing.T) {
	homeDir := t.TempDir()
	client := &mockTransferClient{
		responses: map[string][]byte{
			"c/latest.txt": []byte("100000000"),
		},
		errDefault: fmt.Errorf("access denied"),
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(client), nil)

	if err := restorer.Restore(context.Background(), 0); err == nil {
		t.Fatal("expected error on snapshot download failure")
	}

	if markerExists(homeDir, restoreMarkerFile) {
		t.Fatal("marker file should not exist after failed restore")
	}
}

func TestSnapshotRestoreRejectsPathTraversal(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"../../etc/passwd": "malicious",
	})

	client := &mockTransferClient{
		responses: map[string][]byte{
			"c/latest.txt":       []byte("100000000"),
			"c/100000000.tar.gz": archive,
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(client), nil)
	if err := restorer.Restore(context.Background(), 0); err == nil {
		t.Fatal("expected error for path traversal attempt")
	}
}

func TestSnapshotRestoreCleansUpTempFile(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"data/chain.db": "chaindata",
	})

	client := &mockTransferClient{
		responses: map[string][]byte{
			"c/latest.txt":       []byte("100000000"),
			"c/100000000.tar.gz": archive,
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(client), nil)
	if err := restorer.Restore(context.Background(), 0); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	tmpDir := filepath.Join(homeDir, "tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading tmp dir: %v", err)
	}
	for _, e := range entries {
		if matched, _ := filepath.Match("snapshot-*.tar.gz", e.Name()); matched {
			t.Fatalf("temp file %s was not cleaned up", e.Name())
		}
	}
}

func TestSnapshotRestoreWritesHeightFile(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"data/chain.db": "chaindata",
	})

	client := &mockTransferClient{
		responses: map[string][]byte{
			"c/latest.txt":       []byte("100000000"),
			"c/100000000.tar.gz": archive,
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(client), nil)
	if err := restorer.Restore(context.Background(), 0); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	heightBytes, err := os.ReadFile(filepath.Join(homeDir, SnapshotHeightFile))
	if err != nil {
		t.Fatalf("reading snapshot height file: %v", err)
	}
	if string(heightBytes) != "100000000" {
		t.Errorf("snapshot height file = %q, want %q", string(heightBytes), "100000000")
	}
}

func TestSnapshotRestoreWithTargetHeight(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"data/chain.db": "chaindata",
	})

	client := &mockTransferClient{
		responses: map[string][]byte{
			"c/99000000.tar.gz": archive,
		},
	}
	lister := &mockObjectLister{
		keys: []string{
			"c/98000000.tar.gz",
			"c/99000000.tar.gz",
			"c/100000000.tar.gz",
			"c/latest.txt",
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(client), mockListerFactory(lister))
	// Target 99500000 — should pick 99000000 (highest <= target)
	if err := restorer.Restore(context.Background(), 99500000); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	heightBytes, err := os.ReadFile(filepath.Join(homeDir, SnapshotHeightFile))
	if err != nil {
		t.Fatalf("reading snapshot height file: %v", err)
	}
	if string(heightBytes) != "99000000" {
		t.Errorf("snapshot height = %q, want %q", string(heightBytes), "99000000")
	}
}

func TestSnapshotRestoreTargetHeightNoMatch(t *testing.T) {
	homeDir := t.TempDir()
	lister := &mockObjectLister{
		keys: []string{
			"c/100000000.tar.gz",
			"c/200000000.tar.gz",
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", nil, mockListerFactory(lister))
	// Target 50000000 — no snapshots at or below
	err := restorer.Restore(context.Background(), 50000000)
	if err == nil {
		t.Fatal("expected error when no snapshot found at or below target height")
	}
}

func TestSnapshotRestoreTargetHeightPagination(t *testing.T) {
	homeDir := t.TempDir()
	archive := buildTarGzArchive(t, map[string]string{
		"data/chain.db": "chaindata",
	})

	client := &mockTransferClient{
		responses: map[string][]byte{
			"c/99000000.tar.gz": archive,
		},
	}
	// Page size of 2 forces pagination across 3 pages
	lister := &mockObjectLister{
		pageSize: 2,
		keys: []string{
			"c/97000000.tar.gz",
			"c/98000000.tar.gz",
			"c/99000000.tar.gz",
			"c/100000000.tar.gz",
			"c/latest.txt",
		},
	}
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", mockClientFactory(client), mockListerFactory(lister))
	if err := restorer.Restore(context.Background(), 99500000); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	heightBytes, err := os.ReadFile(filepath.Join(homeDir, SnapshotHeightFile))
	if err != nil {
		t.Fatalf("reading snapshot height file: %v", err)
	}
	if string(heightBytes) != "99000000" {
		t.Errorf("snapshot height = %q, want %q", string(heightBytes), "99000000")
	}
}

func TestSnapshotRestoreNegativeTargetHeight(t *testing.T) {
	homeDir := t.TempDir()
	restorer := NewSnapshotRestorer(homeDir, "b", "r", "c", nil, nil)
	err := restorer.Restore(context.Background(), -1)
	if err == nil {
		t.Fatal("expected error for negative targetHeight")
	}
}
