package tasks

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var uploadFileLog = seilog.NewLogger("seictl", "task", "upload-file")

// UploadFileRequest is the typed body of a POST /v0/tasks/upload-file. All
// fields are required; the task validates and rejects empties before opening
// any file or building an S3 client.
type UploadFileRequest struct {
	File   string `json:"file"`
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Region string `json:"region"`
}

// UploadFileTask streams a file at request-supplied path to s3://bucket/key
// in region. The factory is injectable for tests; production callers pass nil
// to get the default transfermanager-backed uploader.
type UploadFileTask struct {
	uploaderFactory seis3.UploaderFactory
}

// NewUploadFileTask builds the task with the given uploader factory. nil falls
// back to seis3.DefaultUploaderFactory.
func NewUploadFileTask(factory seis3.UploaderFactory) *UploadFileTask {
	if factory == nil {
		factory = seis3.DefaultUploaderFactory
	}
	return &UploadFileTask{uploaderFactory: factory}
}

// Handler returns the engine.TaskHandler for the upload-file task.
func (u *UploadFileTask) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, req UploadFileRequest) error {
		return u.Run(ctx, req)
	})
}

// Run streams req.File to S3. Errors from S3 are classified into a
// *engine.TaskError carrying the operator-actionable message and Retryable
// hint that the engine surfaces to clients.
func (u *UploadFileTask) Run(ctx context.Context, req UploadFileRequest) error {
	if req.File == "" {
		return fmt.Errorf("upload-file: missing 'file'")
	}
	if req.Bucket == "" {
		return fmt.Errorf("upload-file: missing 'bucket'")
	}
	if req.Key == "" {
		return fmt.Errorf("upload-file: missing 'key'")
	}
	if req.Region == "" {
		return fmt.Errorf("upload-file: missing 'region'")
	}

	f, err := os.Open(req.File)
	if err != nil {
		return fmt.Errorf("opening %s: %w", req.File, err)
	}
	defer func() { _ = f.Close() }()

	uploadFileLog.Info("uploading file", "file", req.File, "bucket", req.Bucket, "key", req.Key, "region", req.Region)

	uploader, err := u.uploaderFactory(ctx, req.Region)
	if err != nil {
		return fmt.Errorf("building S3 uploader for region %s: %w", req.Region, err)
	}

	if _, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(req.Bucket),
		Key:    aws.String(req.Key),
		Body:   f,
	}); err != nil {
		return seis3.ClassifyS3Error("upload-file", req.Bucket, req.Key, req.Region, err)
	}

	uploadFileLog.Info("uploaded", "bucket", req.Bucket, "key", req.Key)
	return nil
}
