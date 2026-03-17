package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
)

type mockResultUploader struct{}

func (m *mockResultUploader) UploadObject(_ context.Context, in *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	if in.Body != nil {
		_, _ = io.Copy(io.Discard, in.Body)
	}
	return &transfermanager.UploadObjectOutput{}, nil
}

func mockResultUploaderFactory() S3UploaderFactory {
	return func(_ context.Context, _ string) (S3Uploader, error) {
		return &mockResultUploader{}, nil
	}
}

func failingUploaderFactory(errMsg string) S3UploaderFactory {
	return func(_ context.Context, _ string) (S3Uploader, error) {
		return nil, fmt.Errorf("%s", errMsg)
	}
}

// fakeRPCServer returns an httptest.Server that responds to /status and /block_results.
func fakeRPCServer(latestHeight int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status":
			fmt.Fprintf(w, `{"result":{"sync_info":{"latest_block_height":"%d"}}}`, latestHeight)
		case r.URL.Path == "/block_results":
			fmt.Fprint(w, `{"result":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestExportBootstrapFromSnapshotHeight(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, SnapshotHeightFile), []byte("198030000"), 0o644); err != nil {
		t.Fatalf("writing snapshot height file: %v", err)
	}

	e := NewResultExporter(tmpDir, nil)
	state := e.readExportState()

	if state.LastExportedHeight != 198030000 {
		t.Errorf("LastExportedHeight = %d, want 198030000", state.LastExportedHeight)
	}
}

func TestExportBootstrapNoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, nil)
	state := e.readExportState()

	if state.LastExportedHeight != 0 {
		t.Errorf("LastExportedHeight = %d, want 0", state.LastExportedHeight)
	}
}

func TestExportBootstrapPreferStateFile(t *testing.T) {
	tmpDir := t.TempDir()

	stateData, _ := json.Marshal(exportState{LastExportedHeight: 200000000})
	if err := os.WriteFile(filepath.Join(tmpDir, exportStateFile), stateData, 0o644); err != nil {
		t.Fatalf("writing state file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, SnapshotHeightFile), []byte("198030000"), 0o644); err != nil {
		t.Fatalf("writing snapshot height file: %v", err)
	}

	e := NewResultExporter(tmpDir, nil)
	state := e.readExportState()

	if state.LastExportedHeight != 200000000 {
		t.Errorf("LastExportedHeight = %d, want 200000000", state.LastExportedHeight)
	}
}

func TestExportRPCUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())

	err := e.Export(context.Background(), ResultExportConfig{
		Bucket:      "test-bucket",
		Region:      "us-east-1",
		RPCEndpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("Export() returned error %v, want nil (fail-safe)", err)
	}
}

func TestExportRPCNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "node is syncing")
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())

	err := e.Export(context.Background(), ResultExportConfig{
		Bucket:      "test-bucket",
		Region:      "us-east-1",
		RPCEndpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("Export() returned error %v, want nil (fail-safe on HTTP error)", err)
	}
}

func TestQueryLatestHeight_ZeroHeight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":{"sync_info":{"latest_block_height":"0"}}}`)
	}))
	defer srv.Close()

	_, err := queryLatestHeight(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for zero height, got nil")
	}
}

func TestExportS3UploaderFactoryError(t *testing.T) {
	srv := fakeRPCServer(100000)
	defer srv.Close()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, SnapshotHeightFile), []byte("1"), 0o644); err != nil {
		t.Fatalf("writing snapshot height file: %v", err)
	}

	e := NewResultExporter(tmpDir, failingUploaderFactory("simulated AWS error"))

	err := e.Export(context.Background(), ResultExportConfig{
		Bucket:      "test-bucket",
		Region:      "us-east-1",
		RPCEndpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("Export() returned error %v, want nil (fail-safe)", err)
	}
}

func TestExportWritesStateAfterPage(t *testing.T) {
	// Latest height 1001 with start at 1 gives exactly 1001 available blocks,
	// which is 1 full page (heights 1–1000). The remaining 1 block is deferred.
	srv := fakeRPCServer(1001)
	defer srv.Close()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, SnapshotHeightFile), []byte("0"), 0o644); err != nil {
		t.Fatalf("writing snapshot height file: %v", err)
	}

	e := NewResultExporter(tmpDir, mockResultUploaderFactory())
	err := e.Export(context.Background(), ResultExportConfig{
		Bucket:      "test-bucket",
		Prefix:      "results",
		Region:      "us-east-1",
		RPCEndpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	state := e.readExportState()
	if state.LastExportedHeight != 1000 {
		t.Errorf("LastExportedHeight = %d, want 1000", state.LastExportedHeight)
	}
}

func TestParseExportConfig(t *testing.T) {
	cases := []struct {
		name            string
		params          map[string]any
		wantErr         bool
		wantBucket      string
		wantRegion      string
		wantRPCEndpoint string
	}{
		{
			name:    "missing bucket",
			params:  map[string]any{"region": "us-east-1"},
			wantErr: true,
		},
		{
			name:    "missing region",
			params:  map[string]any{"bucket": "my-bucket"},
			wantErr: true,
		},
		{
			name:            "valid with defaults",
			params:          map[string]any{"bucket": "my-bucket", "region": "us-east-1"},
			wantBucket:      "my-bucket",
			wantRegion:      "us-east-1",
			wantRPCEndpoint: defaultRPCEndpoint,
		},
		{
			name:            "valid with custom rpc",
			params:          map[string]any{"bucket": "b", "region": "r", "rpcEndpoint": "http://custom:26657"},
			wantBucket:      "b",
			wantRegion:      "r",
			wantRPCEndpoint: "http://custom:26657",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseExportConfig(tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseExportConfig() error = %v", err)
			}
			if cfg.Bucket != tc.wantBucket {
				t.Errorf("Bucket = %q, want %q", cfg.Bucket, tc.wantBucket)
			}
			if cfg.Region != tc.wantRegion {
				t.Errorf("Region = %q, want %q", cfg.Region, tc.wantRegion)
			}
			if cfg.RPCEndpoint != tc.wantRPCEndpoint {
				t.Errorf("RPCEndpoint = %q, want %q", cfg.RPCEndpoint, tc.wantRPCEndpoint)
			}
		})
	}
}
