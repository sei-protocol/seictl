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
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
)

type mockResultUploader struct{}

func (m *mockResultUploader) UploadObject(_ context.Context, in *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	if in.Body != nil {
		_, _ = io.Copy(io.Discard, in.Body)
	}
	return &transfermanager.UploadObjectOutput{}, nil
}

func mockResultUploaderFactory() seis3.UploaderFactory {
	return func(_ context.Context, _ string) (seis3.Uploader, error) {
		return &mockResultUploader{}, nil
	}
}

func failingUploaderFactory(errMsg string) seis3.UploaderFactory {
	return func(_ context.Context, _ string) (seis3.Uploader, error) {
		return nil, fmt.Errorf("%s", errMsg)
	}
}

// fakeRPCServer returns an httptest.Server that responds to /status and /block_results.
func fakeRPCServer(latestHeight int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status":
			fmt.Fprintf(w, `{"sync_info":{"latest_block_height":"%d"}}`, latestHeight)
		case r.URL.Path == "/block_results":
			fmt.Fprint(w, `{}`)
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

	err := e.Export(context.Background(), ResultExportRequest{
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

	err := e.Export(context.Background(), ResultExportRequest{
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
		fmt.Fprint(w, `{"sync_info":{"latest_block_height":"0"}}`)
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

	err := e.Export(context.Background(), ResultExportRequest{
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
	err := e.Export(context.Background(), ResultExportRequest{
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

func TestExportHandler_MissingParams(t *testing.T) {
	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())
	handler := e.Handler()

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing bucket", map[string]any{"region": "us-east-1"}},
		{"missing region", map[string]any{"bucket": "my-bucket"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := handler(context.Background(), tc.params)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestExportConfigJSONRoundTrip(t *testing.T) {
	cfg := ResultExportRequest{
		Bucket:       "my-bucket",
		Region:       "us-east-1",
		RPCEndpoint:  "http://custom:26657",
		CanonicalRPC: "http://canonical:26657",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}
	var decoded ResultExportRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}
	if decoded.Bucket != cfg.Bucket {
		t.Errorf("Bucket = %q, want %q", decoded.Bucket, cfg.Bucket)
	}
	if decoded.Region != cfg.Region {
		t.Errorf("Region = %q, want %q", decoded.Region, cfg.Region)
	}
	if decoded.RPCEndpoint != cfg.RPCEndpoint {
		t.Errorf("RPCEndpoint = %q, want %q", decoded.RPCEndpoint, cfg.RPCEndpoint)
	}
	if decoded.CanonicalRPC != cfg.CanonicalRPC {
		t.Errorf("CanonicalRPC = %q, want %q", decoded.CanonicalRPC, cfg.CanonicalRPC)
	}
}

// --- Handler routing tests ---

func TestHandlerRouting_WithCanonicalRPC_CallsExportAndCompare(t *testing.T) {
	srv := fakeRPCAndBlockServer(1, "AABB", "CCDD", nil)
	defer srv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := e.Handler()(ctx, map[string]any{
		"bucket":       "test-bucket",
		"region":       "us-east-1",
		"rpcEndpoint":  srv.URL,
		"canonicalRpc": srv.URL,
	})
	if err == nil {
		t.Fatal("expected context deadline exceeded error")
	}
}

func TestHandlerRouting_WithoutCanonicalRPC_CallsExport(t *testing.T) {
	srv := fakeRPCServer(0) // 0 blocks → nothing to export
	defer srv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())

	err := e.Handler()(context.Background(), map[string]any{
		"bucket":      "test-bucket",
		"region":      "us-east-1",
		"rpcEndpoint": srv.URL,
	})
	if err != nil {
		t.Fatalf("Handler() error = %v, want nil for empty export", err)
	}
}

// --- ExportAndCompare tests ---

func TestExportAndCompare_DivergenceDetected(t *testing.T) {
	// Shadow returns different app hash than canonical → immediate divergence.
	shadowSrv := fakeRPCAndBlockServer(5, "SHADOW_HASH", "RESULTS", nil)
	defer shadowSrv.Close()
	canonicalSrv := fakeRPCAndBlockServer(5, "CANONICAL_HASH", "RESULTS", nil)
	defer canonicalSrv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())

	err := e.ExportAndCompare(context.Background(), ResultExportRequest{
		Bucket:       "test-bucket",
		Prefix:       "compare/",
		Region:       "us-east-1",
		RPCEndpoint:  shadowSrv.URL,
		CanonicalRPC: canonicalSrv.URL,
	})
	// Divergence detected = task completes successfully (nil error).
	if err != nil {
		t.Fatalf("ExportAndCompare() error = %v, want nil (divergence = success)", err)
	}

	state := e.readExportState()
	if state.LastExportedHeight != 1 {
		t.Errorf("LastExportedHeight = %d, want 1 (diverged at first block)", state.LastExportedHeight)
	}
}

func TestExportAndCompare_ContextCancelled(t *testing.T) {
	srv := fakeRPCAndBlockServer(1, "SAME", "SAME", nil)
	defer srv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, mockResultUploaderFactory())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := e.ExportAndCompare(ctx, ResultExportRequest{
		Bucket:       "test-bucket",
		Region:       "us-east-1",
		RPCEndpoint:  srv.URL,
		CanonicalRPC: srv.URL,
	})
	if err == nil {
		t.Fatal("expected context deadline exceeded error")
	}
}

func TestExportAndCompare_S3UploaderError(t *testing.T) {
	srv := fakeRPCAndBlockServer(5, "AA", "BB", nil)
	defer srv.Close()

	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, failingUploaderFactory("AWS creds expired"))

	err := e.ExportAndCompare(context.Background(), ResultExportRequest{
		Bucket:       "test-bucket",
		Region:       "us-east-1",
		RPCEndpoint:  srv.URL,
		CanonicalRPC: srv.URL,
	})
	if err == nil {
		t.Fatal("expected error from S3 uploader factory failure")
	}
}

func TestExportAndCompare_ResumesFromExportState(t *testing.T) {
	// Set export state to height 3, shadow serves up to 5.
	// With matching hashes, it compares blocks 4 and 5 (no divergence).
	// Set up divergence at block 4 by using different servers.
	shadowSrv := fakeRPCAndBlockServer(5, "SHADOW", "RES", nil)
	defer shadowSrv.Close()
	canonicalSrv := fakeRPCAndBlockServer(5, "CANONICAL", "RES", nil)
	defer canonicalSrv.Close()

	tmpDir := t.TempDir()
	stateData, _ := json.Marshal(exportState{LastExportedHeight: 3})
	if err := os.WriteFile(filepath.Join(tmpDir, exportStateFile), stateData, 0o644); err != nil {
		t.Fatalf("writing state: %v", err)
	}

	e := NewResultExporter(tmpDir, mockResultUploaderFactory())
	err := e.ExportAndCompare(context.Background(), ResultExportRequest{
		Bucket:       "test-bucket",
		Region:       "us-east-1",
		RPCEndpoint:  shadowSrv.URL,
		CanonicalRPC: canonicalSrv.URL,
	})
	if err != nil {
		t.Fatalf("ExportAndCompare() error = %v", err)
	}

	state := e.readExportState()
	if state.LastExportedHeight != 4 {
		t.Errorf("LastExportedHeight = %d, want 4 (first block after resume)", state.LastExportedHeight)
	}
}

// --- Divergence report tests ---

func TestExportAndCompare_UploadsDivergenceReport(t *testing.T) {
	shadowSrv := fakeRPCAndBlockServer(5, "SHADOW", "RESULTS", nil)
	defer shadowSrv.Close()
	canonicalSrv := fakeRPCAndBlockServer(5, "CANONICAL", "RESULTS", nil)
	defer canonicalSrv.Close()

	recorder := &recordingUploader{}
	tmpDir := t.TempDir()
	e := NewResultExporter(tmpDir, func(_ context.Context, _ string) (seis3.Uploader, error) {
		return recorder, nil
	})

	err := e.ExportAndCompare(context.Background(), ResultExportRequest{
		Bucket:       "test-bucket",
		Prefix:       "shadow/pacific-1/",
		Region:       "us-east-1",
		RPCEndpoint:  shadowSrv.URL,
		CanonicalRPC: canonicalSrv.URL,
	})
	if err != nil {
		t.Fatalf("ExportAndCompare() error = %v", err)
	}

	var reportKey string
	var compareKey string
	for _, key := range recorder.keys {
		if strings.Contains(key, "divergence-") && strings.Contains(key, ".report.json.gz") {
			reportKey = key
		}
		if strings.Contains(key, ".compare.ndjson.gz") {
			compareKey = key
		}
	}

	if reportKey == "" {
		t.Errorf("expected divergence report upload, got keys: %v", recorder.keys)
	}
	if compareKey == "" {
		t.Errorf("expected comparison page upload, got keys: %v", recorder.keys)
	}

	if reportKey != "" && reportKey != "shadow/pacific-1/divergence-1.report.json.gz" {
		t.Errorf("report key = %q, want %q", reportKey, "shadow/pacific-1/divergence-1.report.json.gz")
	}
}

// --- flushComparePage tests ---

func TestFlushComparePage_EmptyResults(t *testing.T) {
	err := flushComparePage(context.Background(), &mockResultUploader{}, "bucket", "prefix/", nil)
	if err != nil {
		t.Errorf("flushComparePage(nil) error = %v, want nil", err)
	}
}

// --- Test helpers ---

type recordingUploader struct {
	keys []string
}

func (r *recordingUploader) UploadObject(_ context.Context, in *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	if in.Key != nil {
		r.keys = append(r.keys, *in.Key)
	}
	if in.Body != nil {
		_, _ = io.Copy(io.Discard, in.Body)
	}
	return &transfermanager.UploadObjectOutput{}, nil
}

// fakeRPCAndBlockServer responds to /status, /block, and /block_results.
func fakeRPCAndBlockServer(latestHeight int64, appHash, lastResultsHash string, txResults []json.RawMessage) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status":
			fmt.Fprintf(w, `{"sync_info":{"latest_block_height":"%d"}}`, latestHeight)
		case r.URL.Path == "/block":
			resp := map[string]any{
				"result": map[string]any{
					"block": map[string]any{
						"header": map[string]any{
							"app_hash":          appHash,
							"last_results_hash": lastResultsHash,
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/block_results":
			resp := map[string]any{
				"result": map[string]any{
					"txs_results": txResults,
				},
			}
			json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
}
