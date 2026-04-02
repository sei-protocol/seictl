package tasks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"

	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var stateExportLog = seilog.NewLogger("sidecar", "task", "export-state")

const exportMarkerFile = ".sei-sidecar-export-done"

// ExportStateRequest holds the typed parameters for the export-state task.
type ExportStateRequest struct {
	Height   int64  `json:"height,omitempty"`
	ChainID  string `json:"chainId"`
	S3Bucket string `json:"s3Bucket"`
	S3Key    string `json:"s3Key,omitempty"`
	S3Region string `json:"s3Region"`
}

// StateExporter runs seid export and uploads the result to S3.
type StateExporter struct {
	homeDir           string
	s3UploaderFactory seis3.UploaderFactory
}

// NewStateExporter creates an exporter targeting the given home directory.
func NewStateExporter(homeDir string, uploaderFactory seis3.UploaderFactory) *StateExporter {
	if uploaderFactory == nil {
		uploaderFactory = seis3.DefaultUploaderFactory
	}
	return &StateExporter{
		homeDir:           homeDir,
		s3UploaderFactory: uploaderFactory,
	}
}

// Handler returns an engine.TaskHandler for the export-state task type.
func (e *StateExporter) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, req ExportStateRequest) error {
		if markerExists(e.homeDir, exportMarkerFile) {
			stateExportLog.Debug("already completed, skipping")
			return nil
		}
		if req.ChainID == "" {
			return fmt.Errorf("export-state: missing 'chainId'")
		}
		if req.S3Bucket == "" {
			return fmt.Errorf("export-state: missing 's3Bucket'")
		}
		if req.S3Region == "" {
			return fmt.Errorf("export-state: missing 's3Region'")
		}

		args := []string{"export", "--home", e.homeDir}
		if req.Height > 0 {
			args = append(args, "--height", strconv.FormatInt(req.Height, 10))
		}

		stateExportLog.Info("running seid export", "height", req.Height)
		cmd := exec.CommandContext(ctx, "seid", args...)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("export-state: seid export failed: %w", err)
		}

		s3Key := req.S3Key
		if s3Key == "" {
			s3Key = req.ChainID + "/exported-state.json"
		}

		uploader, err := e.s3UploaderFactory(ctx, req.S3Region)
		if err != nil {
			return fmt.Errorf("export-state: building S3 uploader: %w", err)
		}

		// Write to temp file first to avoid holding multi-GB in memory
		// during the S3 multipart upload.
		tmpDir := filepath.Join(e.homeDir, "tmp")
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			return fmt.Errorf("export-state: creating tmp dir: %w", err)
		}
		tmpFile, err := os.CreateTemp(tmpDir, "export-*.json")
		if err != nil {
			return fmt.Errorf("export-state: creating temp file: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer func() { _ = os.Remove(tmpPath) }()

		if _, err := tmpFile.Write(output); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("export-state: writing temp file: %w", err)
		}
		_ = tmpFile.Close()

		stateExportLog.Info("uploading exported state", "bucket", req.S3Bucket, "key", s3Key, "bytes", len(output))
		_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
			Bucket:      aws.String(req.S3Bucket),
			Key:         aws.String(s3Key),
			Body:        bytes.NewReader(output),
			ContentType: aws.String("application/json"),
		})
		if err != nil {
			return fmt.Errorf("export-state: uploading to S3: %w", err)
		}

		stateExportLog.Info("export complete", "key", s3Key)
		return writeMarker(e.homeDir, exportMarkerFile)
	})
}
