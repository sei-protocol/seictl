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

// comparisonLoop holds the state for a running block comparison session.
type comparisonLoop struct {
	exporter     *ResultExporter
	comparator   *shadow.Comparator
	uploader     seis3.Uploader
	cfg          ResultExportConfig
	prefix       string
	height       int64
	pageBuf      []shadow.CompareResult
	pollInterval time.Duration
}

// ExportAndCompare runs a continuous comparison between the local shadow node
// and a canonical chain. It completes successfully when app-hash divergence is
// detected, uploading a DivergenceReport alongside the comparison pages.
func (e *ResultExporter) ExportAndCompare(ctx context.Context, cfg ResultExportConfig) error {
	loop, err := e.newComparisonLoop(ctx, cfg)
	if err != nil {
		return err
	}

	exportLog.Info("starting block comparison",
		"start-height", loop.height,
		"canonical-rpc", cfg.CanonicalRPC,
		"bucket", cfg.Bucket)

	return loop.run(ctx)
}

func (e *ResultExporter) newComparisonLoop(ctx context.Context, cfg ResultExportConfig) (*comparisonLoop, error) {
	uploader, err := e.s3UploaderFactory(ctx, cfg.Region)
	if err != nil {
		return nil, fmt.Errorf("building S3 uploader: %w", err)
	}

	last := e.readExportState()
	return &comparisonLoop{
		exporter:     e,
		comparator:   shadow.NewComparator(cfg.RPCEndpoint, cfg.CanonicalRPC),
		uploader:     uploader,
		cfg:          cfg,
		prefix:       normalizePrefix(cfg.Prefix),
		height:       last.LastExportedHeight + 1,
		pollInterval: comparePollInterval,
	}, nil
}

func (l *comparisonLoop) run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		latestHeight, err := l.waitForBlocks(ctx)
		if err != nil {
			return err
		}

		diverged, err := l.compareBlocksUpTo(ctx, latestHeight)
		if err != nil {
			return err
		}
		if diverged {
			return nil
		}
	}
}

func (l *comparisonLoop) waitForBlocks(ctx context.Context) (int64, error) {
	for {
		latestHeight, err := queryLatestHeight(ctx, l.cfg.RPCEndpoint)
		if err != nil {
			exportLog.Debug("shadow RPC unavailable, will retry", "err", err)
			if err := sleep(ctx, l.pollInterval); err != nil {
				return 0, err
			}
			continue
		}

		if l.height <= latestHeight {
			return latestHeight, nil
		}

		if err := sleep(ctx, l.pollInterval); err != nil {
			return 0, err
		}
	}
}

func (l *comparisonLoop) compareBlocksUpTo(ctx context.Context, latestHeight int64) (diverged bool, _ error) {
	for l.height <= latestHeight {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		result, err := l.comparator.CompareBlock(ctx, l.height)
		if err != nil {
			exportLog.Warn("comparison failed, will retry", "height", l.height, "err", err)
			return false, sleep(ctx, l.pollInterval)
		}

		l.pageBuf = append(l.pageBuf, *result)

		if result.Diverged() {
			return true, l.handleDivergence(ctx, *result)
		}

		if err := l.flushPageIfFull(ctx); err != nil {
			return false, err
		}

		l.height++
	}
	return false, nil
}

func (l *comparisonLoop) handleDivergence(ctx context.Context, result shadow.CompareResult) error {
	exportLog.Info("app-hash divergence detected",
		"height", l.height,
		"shadow-app-hash", result.Layer0.ShadowAppHash,
		"canonical-app-hash", result.Layer0.CanonicalAppHash)

	if err := l.uploadDivergenceReport(ctx, result); err != nil {
		exportLog.Warn("failed to upload divergence report, continuing", "err", err)
	}

	if err := flushComparePage(ctx, l.uploader, l.cfg.Bucket, l.prefix, l.pageBuf); err != nil {
		return fmt.Errorf("flushing final comparison page: %w", err)
	}

	l.exporter.persistHeight(l.height)
	return nil
}

func (l *comparisonLoop) uploadDivergenceReport(ctx context.Context, result shadow.CompareResult) error {
	report, err := l.comparator.BuildDivergenceReport(ctx, l.height, result)
	if err != nil {
		return fmt.Errorf("building divergence report: %w", err)
	}

	key := fmt.Sprintf("%sdivergence-%d.report.json.gz", l.prefix, l.height)
	return uploadGzipJSON(ctx, l.uploader, l.cfg.Bucket, key, report)
}

func (l *comparisonLoop) flushPageIfFull(ctx context.Context) error {
	if len(l.pageBuf) < comparePageSize {
		return nil
	}

	if err := flushComparePage(ctx, l.uploader, l.cfg.Bucket, l.prefix, l.pageBuf); err != nil {
		exportLog.Warn("page flush failed, will retry", "err", err)
		return nil
	}

	l.exporter.persistHeight(l.height)
	l.pageBuf = l.pageBuf[:0]
	return nil
}

// persistHeight saves the last exported height to disk, logging on failure.
func (e *ResultExporter) persistHeight(height int64) {
	if err := e.writeExportState(exportState{LastExportedHeight: height}); err != nil {
		exportLog.Warn("failed to persist export state", "err", err)
	}
}

// --- S3 upload helpers ---

func flushComparePage(ctx context.Context, uploader seis3.Uploader, bucket, prefix string, results []shadow.CompareResult) error {
	if len(results) == 0 {
		return nil
	}

	start := results[0].Height
	end := results[len(results)-1].Height
	key := fmt.Sprintf("%s%d-%d.compare.ndjson.gz", prefix, start, end)

	exportLog.Info("flushing comparison page", "key", key, "blocks", len(results))
	return streamGzipNDJSON(ctx, uploader, bucket, key, results)
}

func streamGzipNDJSON(ctx context.Context, uploader seis3.Uploader, bucket, key string, results []shadow.CompareResult) error {
	pr, pw := io.Pipe()

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- writeGzipNDJSON(results, pw)
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

func writeGzipNDJSON(results []shadow.CompareResult, wc io.WriteCloser) (retErr error) {
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
		if _, err := gw.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("writing comparison at height %d: %w", r.Height, err)
		}
	}
	return nil
}

func uploadGzipJSON(ctx context.Context, uploader seis3.Uploader, bucket, key string, v any) error {
	pr, pw := io.Pipe()

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- writeGzipSingleJSON(v, pw)
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

func writeGzipSingleJSON(v any, wc io.WriteCloser) (retErr error) {
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

	enc := json.NewEncoder(gw)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

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
