package tasks

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/smithy-go"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
)

const (
	uploadTestBucket = "test-bucket"
	uploadTestKey    = "test-chain/exported-state.json"
	uploadTestRegion = "eu-central-1"
	uploadTestBody   = "exported-state-payload"
)

// mockSmithyAPIError satisfies smithy.APIError for tests that exercise the
// S3 error classification path through ClassifyS3Error.
type mockSmithyAPIError struct {
	code, msg string
}

func (m mockSmithyAPIError) Error() string                 { return m.msg }
func (m mockSmithyAPIError) ErrorCode() string             { return m.code }
func (m mockSmithyAPIError) ErrorMessage() string          { return m.msg }
func (m mockSmithyAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

// errorUploader returns the configured error from UploadObject.
type errorUploader struct{ err error }

func (e *errorUploader) UploadObject(_ context.Context, _ *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	return nil, e.err
}

func errorUploaderFactory(err error) seis3.UploaderFactory {
	return func(_ context.Context, _ string) (seis3.Uploader, error) {
		return &errorUploader{err: err}, nil
	}
}

func writeFixtureFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(uploadTestBody), 0o600); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	return path
}

func TestUploadFileTask_Success(t *testing.T) {
	dir := t.TempDir()
	file := writeFixtureFile(t, dir, "exported-state.json")
	mock := newMockS3Uploader()

	task := NewUploadFileTask(mockUploaderFactory(mock))
	err := task.Run(context.Background(), UploadFileRequest{
		File:   file,
		Bucket: uploadTestBucket,
		Key:    uploadTestKey,
		Region: uploadTestRegion,
	})
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}

	stored := mock.uploads[uploadTestBucket+"/"+uploadTestKey]
	if string(stored) != uploadTestBody {
		t.Errorf("uploaded body = %q, want %q", string(stored), uploadTestBody)
	}
}

func TestUploadFileTask_RequiredFields(t *testing.T) {
	dir := t.TempDir()
	file := writeFixtureFile(t, dir, "f.json")
	tests := []struct {
		name      string
		req       UploadFileRequest
		errSubstr string
	}{
		{"empty file", UploadFileRequest{Bucket: uploadTestBucket, Key: uploadTestKey, Region: uploadTestRegion}, "'file'"},
		{"empty bucket", UploadFileRequest{File: file, Key: uploadTestKey, Region: uploadTestRegion}, "'bucket'"},
		{"empty key", UploadFileRequest{File: file, Bucket: uploadTestBucket, Region: uploadTestRegion}, "'key'"},
		{"empty region", UploadFileRequest{File: file, Bucket: uploadTestBucket, Key: uploadTestKey}, "'region'"},
	}
	task := NewUploadFileTask(mockUploaderFactory(newMockS3Uploader()))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := task.Run(context.Background(), tt.req)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("error %q does not mention %s", err.Error(), tt.errSubstr)
			}
		})
	}
}

func TestUploadFileTask_FileNotFound(t *testing.T) {
	task := NewUploadFileTask(mockUploaderFactory(newMockS3Uploader()))
	err := task.Run(context.Background(), UploadFileRequest{
		File:   "/no/such/file.json",
		Bucket: uploadTestBucket,
		Key:    uploadTestKey,
		Region: uploadTestRegion,
	})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	// Not an S3 error — should NOT be a TaskError.
	var te *engine.TaskError
	if errors.As(err, &te) {
		t.Errorf("expected plain error for missing file, got TaskError: %v", te)
	}
}

func TestUploadFileTask_ClassifiesS3Errors(t *testing.T) {
	dir := t.TempDir()
	file := writeFixtureFile(t, dir, "exported-state.json")

	tests := []struct {
		name           string
		injected       error
		wantRetryable  bool
		wantMsgContain string
	}{
		{
			name:           "AccessDenied is terminal",
			injected:       mockSmithyAPIError{code: "AccessDenied", msg: "access denied"},
			wantRetryable:  false,
			wantMsgContain: "access denied",
		},
		{
			name:           "NoSuchBucket is terminal",
			injected:       mockSmithyAPIError{code: "NoSuchBucket", msg: "bucket missing"},
			wantRetryable:  false,
			wantMsgContain: "does not exist",
		},
		{
			name:           "SlowDown is retryable",
			injected:       mockSmithyAPIError{code: "SlowDown", msg: "slow down"},
			wantRetryable:  true,
			wantMsgContain: "throttled",
		},
		{
			name:           "ServiceUnavailable is retryable",
			injected:       mockSmithyAPIError{code: "ServiceUnavailable", msg: "503"},
			wantRetryable:  true,
			wantMsgContain: "throttled",
		},
		{
			name:           "net.OpError is retryable",
			injected:       &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			wantRetryable:  true,
			wantMsgContain: "network error",
		},
		{
			name:           "unclassified error is terminal",
			injected:       errors.New("something exploded"),
			wantRetryable:  false,
			wantMsgContain: "unexpected error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := NewUploadFileTask(errorUploaderFactory(tt.injected))
			err := task.Run(context.Background(), UploadFileRequest{
				File:   file,
				Bucket: uploadTestBucket,
				Key:    uploadTestKey,
				Region: uploadTestRegion,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var te *engine.TaskError
			if !errors.As(err, &te) {
				t.Fatalf("expected *engine.TaskError, got %T: %v", err, err)
			}
			if te.Retryable != tt.wantRetryable {
				t.Errorf("Retryable = %v, want %v", te.Retryable, tt.wantRetryable)
			}
			if !strings.Contains(te.Message, tt.wantMsgContain) {
				t.Errorf("Message %q does not contain %q", te.Message, tt.wantMsgContain)
			}
			if te.Task != "upload-file" {
				t.Errorf("Task = %q, want upload-file", te.Task)
			}
		})
	}
}

// TestUploadFileTask_ContractKeys pins the JSON keys this task decodes. The
// wire contract is `{"file":"...","bucket":"...","key":"...","region":"..."}`;
// any rename here silently breaks every caller that POSTs against the wire
// shape rather than reflectively building the body.
func TestUploadFileTask_ContractKeys(t *testing.T) {
	tags := map[string]string{
		"File":   "file",
		"Bucket": "bucket",
		"Key":    "key",
		"Region": "region",
	}
	rt := reflect.TypeOf(UploadFileRequest{})
	for fieldName, wantTag := range tags {
		f, ok := rt.FieldByName(fieldName)
		if !ok {
			t.Fatalf("UploadFileRequest has no field %s", fieldName)
		}
		got := f.Tag.Get("json")
		if got != wantTag {
			t.Errorf("field %s json tag = %q, want %q (controller bash POSTs this key)", fieldName, got, wantTag)
		}
	}
}
