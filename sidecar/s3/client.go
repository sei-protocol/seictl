package s3

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TransferClient abstracts S3 downloads. DownloadObject uses the transfer
// manager's io.WriterAt path for parallel byte-range downloads.
type TransferClient interface {
	DownloadObject(ctx context.Context, input *transfermanager.DownloadObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.DownloadObjectOutput, error)
}

// TransferClientFactory builds a TransferClient for a given region.
type TransferClientFactory func(ctx context.Context, region string) (TransferClient, error)

// DefaultTransferClientFactory creates a transfer manager backed by a real
// S3 service client.
func DefaultTransferClientFactory(ctx context.Context, region string) (TransferClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return transfermanager.New(s3.NewFromConfig(cfg)), nil
}

// Uploader abstracts the transfermanager upload call for testing.
type Uploader interface {
	UploadObject(ctx context.Context, input *transfermanager.UploadObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error)
}

// UploaderFactory builds an Uploader for a given region.
type UploaderFactory func(ctx context.Context, region string) (Uploader, error)

// DefaultUploaderFactory creates a transfermanager.Client backed by a real S3 client.
func DefaultUploaderFactory(ctx context.Context, region string) (Uploader, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return transfermanager.New(s3.NewFromConfig(cfg)), nil
}

// ObjectLister abstracts S3 ListObjectsV2 for snapshot discovery.
type ObjectLister interface {
	ListObjectsV2(ctx context.Context, input *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// ObjectListerFactory builds an ObjectLister for a given region.
type ObjectListerFactory func(ctx context.Context, region string) (ObjectLister, error)

// DefaultObjectListerFactory creates a real S3 client for listing objects.
func DefaultObjectListerFactory(ctx context.Context, region string) (ObjectLister, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return s3.NewFromConfig(cfg), nil
}

// WriteAtBuffer is a goroutine-safe in-memory io.WriterAt, used for
// downloading small S3 objects (e.g. latest.txt) via DownloadObject.
type WriteAtBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (w *WriteAtBuffer) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	end := int(off) + len(p)
	if end > len(w.buf) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[off:], p)
	return len(p), nil
}

func (w *WriteAtBuffer) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]byte, len(w.buf))
	copy(out, w.buf)
	return out
}
