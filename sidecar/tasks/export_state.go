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

var exportLog = seilog.NewLogger("seictl", "task", "export-state")

const exportMarkerFile = ".sei-sidecar-export-done"

// CommandRunner abstracts os/exec for testability.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

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
	cmdRunner         CommandRunner
	s3UploaderFactory seis3.UploaderFactory
}

// NewStateExporter creates an exporter targeting the given home directory.
func NewStateExporter(homeDir string, cmdRunner CommandRunner, uploaderFactory seis3.UploaderFactory) *StateExporter {
	if cmdRunner == nil {
		cmdRunner = defaultCommandRunner
	}
	if uploaderFactory == nil {
		uploaderFactory = seis3.DefaultUploaderFactory
	}
	return &StateExporter{
		homeDir:           homeDir,
		cmdRunner:         cmdRunner,
		s3UploaderFactory: uploaderFactory,
	}
}

// Handler returns an engine.TaskHandler for the export-state task type.
func (e *StateExporter) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, req ExportStateRequest) error {
		if markerExists(e.homeDir, exportMarkerFile) {
			exportLog.Debug("already completed, skipping")
			return nil
		}

		if req.ChainID == "" {
			return fmt.Errorf("export-state: missing required param 'chainId'")
		}
		if req.S3Bucket == "" {
			return fmt.Errorf("export-state: missing required param 's3Bucket'")
		}
		if req.S3Region == "" {
			return fmt.Errorf("export-state: missing required param 's3Region'")
		}

		tmpDir := filepath.Join(e.homeDir, "tmp")
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			return fmt.Errorf("export-state: creating tmp dir: %w", err)
		}

		args := []string{"export", "--home", e.homeDir}
		if req.Height > 0 {
			args = append(args, "--height", strconv.FormatInt(req.Height, 10))
		}

		exportLog.Info("running seid export", "height", req.Height, "home", e.homeDir)
		output, err := e.cmdRunner(ctx, "seid", args...)
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

		exportLog.Info("uploading exported state", "bucket", req.S3Bucket, "key", s3Key, "size", len(output))
		_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
			Bucket:      aws.String(req.S3Bucket),
			Key:         aws.String(s3Key),
			Body:        bytes.NewReader(output),
			ContentType: aws.String("application/json"),
		})
		if err != nil {
			return fmt.Errorf("export-state: uploading to S3: %w", err)
		}

		exportLog.Info("export complete", "key", s3Key)
		return writeMarker(e.homeDir, exportMarkerFile)
	})
}
