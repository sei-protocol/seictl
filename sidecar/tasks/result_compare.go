package tasks

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seictl/sidecar/shadow"
)

const (
	comparePollInterval = 5 * time.Second
	comparePageSize     = 100
)

// ExportAndCompare runs a continuous comparison loop between the local
// shadow node and a canonical chain. For each block it performs a layered
// comparison via shadow.Comparator and streams the results as gzipped
// NDJSON pages to S3. When app-hash divergence is detected, the final
// page (including the divergent block) is flushed and the task completes
// successfully — signaling the controller that divergence was found.
func (e *ResultExporter) ExportAndCompare(ctx context.Context, cfg ResultExportConfig) error {
	comp := shadow.NewComparator(cfg.RPCEndpoint, cfg.CanonicalRPC)
	last := e.readExportState()
	height := last.LastExportedHeight + 1
	prefix := normalizePrefix(cfg.Prefix)

	uploader, err := e.s3UploaderFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("building S3 uploader: %w", err)
	}

	exportLog.Info("starting block comparison",
		"start-height", height,
		"canonical-rpc", cfg.CanonicalRPC,
		"bucket", cfg.Bucket)

	var pageBuf []shadow.CompareResult

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Wait for the shadow node to produce blocks at or beyond our target.
		latestHeight, err := queryLatestHeight(ctx, cfg.RPCEndpoint)
		if err != nil {
			exportLog.Debug("shadow RPC unavailable, will retry", "err", err)
			if err := sleep(ctx, comparePollInterval); err != nil {
				return err
			}
			continue
		}

		if height > latestHeight {
			if err := sleep(ctx, comparePollInterval); err != nil {
				return err
			}
			continue
		}

		// Compare all available blocks up to the latest height.
		for height <= latestHeight {
			if err := ctx.Err(); err != nil {
				return err
			}

			result, err := comp.CompareBlock(ctx, height)
			if err != nil {
				exportLog.Warn("comparison failed, will retry", "height", height, "err", err)
				if err := sleep(ctx, comparePollInterval); err != nil {
					return err
				}
				break
			}

			pageBuf = append(pageBuf, *result)

			if result.Diverged() {
				exportLog.Info("app-hash divergence detected",
					"height", height,
					"shadow-app-hash", result.Layer0.ShadowAppHash,
					"canonical-app-hash", result.Layer0.CanonicalAppHash)

				// Flush the final page including the divergent block.
				if err := flushComparePage(ctx, uploader, cfg.Bucket, prefix, pageBuf); err != nil {
					return fmt.Errorf("flushing final comparison page: %w", err)
				}
				if err := e.writeExportState(exportState{LastExportedHeight: height}); err != nil {
					exportLog.Warn("failed to persist export state", "err", err)
				}

				// Task completes successfully — the controller sees "completed"
				// and knows divergence was found.
				return nil
			}

			// Flush page when buffer is full.
			if len(pageBuf) >= comparePageSize {
				if err := flushComparePage(ctx, uploader, cfg.Bucket, prefix, pageBuf); err != nil {
					exportLog.Warn("page flush failed, will retry", "err", err)
					break
				}
				if err := e.writeExportState(exportState{LastExportedHeight: height}); err != nil {
					exportLog.Warn("failed to persist export state", "err", err)
				}
				pageBuf = pageBuf[:0]
			}

			height++
		}
	}
}

// flushComparePage streams a page of comparison results as gzipped NDJSON to S3.
func flushComparePage(ctx context.Context, uploader seis3.Uploader, bucket, prefix string, results []shadow.CompareResult) error {
	if len(results) == 0 {
		return nil
	}

	start := results[0].Height
	end := results[len(results)-1].Height
	key := fmt.Sprintf("%s%d-%d.compare.ndjson.gz", prefix, start, end)

	exportLog.Info("flushing comparison page", "key", key, "blocks", len(results))

	pr, pw := io.Pipe()

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- writeCompareNDJSON(results, pw)
	}()

	_, uploadErr := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        pr,
		ContentType: aws.String("application/gzip"),
	})

	if uploadErr != nil {
		pr.CloseWithError(uploadErr)
	}

	wErr := <-writeErr
	if uploadErr != nil {
		return uploadErr
	}
	return wErr
}

// writeCompareNDJSON writes gzipped NDJSON comparison results to wc.
func writeCompareNDJSON(results []shadow.CompareResult, wc io.WriteCloser) (retErr error) {
	defer func() {
		if retErr != nil {
			wc.(*io.PipeWriter).CloseWithError(retErr)
		} else {
			_ = wc.Close()
		}
	}()

	gw := gzip.NewWriter(wc)
	defer func() {
		if err := gw.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("closing gzip writer: %w", err)
		}
	}()

	for _, r := range results {
		line, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshaling comparison at height %d: %w", r.Height, err)
		}
		line = append(line, '\n')
		if _, err := gw.Write(line); err != nil {
			return fmt.Errorf("writing comparison at height %d: %w", r.Height, err)
		}
	}

	return nil
}

// sleep blocks until the duration elapses or ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
