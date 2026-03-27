package tasks

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	seiconfig "github.com/sei-protocol/sei-config"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var exportLog = seilog.NewLogger("seictl", "task", "result-export")

const (
	exportStateFile  = ".sei-sidecar-last-export.json"
	defaultPageSize  = 1000
	exportRPCTimeout = 10 * time.Second
)

var defaultRPCEndpoint = fmt.Sprintf("http://localhost:%d", seiconfig.PortRPC)

// ResultExportConfig holds the parameters for the result-export task.
type ResultExportConfig struct {
	Bucket      string
	Prefix      string
	Region      string
	RPCEndpoint string

	// CanonicalRPC enables comparison mode. When set, the exporter compares
	// local block execution against this canonical RPC endpoint and completes
	// when app-hash divergence is detected.
	CanonicalRPC string
}

type exportState struct {
	LastExportedHeight int64 `json:"lastExportedHeight"`
}

// ResultExporter queries the local seid RPC for block results and uploads
// them in compressed NDJSON pages to S3.
type ResultExporter struct {
	homeDir           string
	s3UploaderFactory seis3.UploaderFactory
}

func NewResultExporter(homeDir string, factory seis3.UploaderFactory) *ResultExporter {
	if factory == nil {
		factory = seis3.DefaultUploaderFactory
	}
	return &ResultExporter{homeDir: homeDir, s3UploaderFactory: factory}
}

func (e *ResultExporter) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		cfg, err := parseExportConfig(params)
		if err != nil {
			return err
		}
		if cfg.CanonicalRPC != "" {
			return e.ExportAndCompare(ctx, cfg)
		}
		return e.Export(ctx, cfg)
	}
}

// Export queries the local node for block results and uploads pages to S3.
// Each invocation exports as many complete pages as are available since the
// last export height. The state file tracks progress across invocations.
func (e *ResultExporter) Export(ctx context.Context, cfg ResultExportConfig) error {
	last := e.readExportState()
	startHeight := last.LastExportedHeight + 1

	latestHeight, err := queryLatestHeight(ctx, cfg.RPCEndpoint)
	if err != nil {
		exportLog.Warn("RPC unavailable, deferring to next scheduled run", "err", err)
		return nil
	}

	if startHeight > latestHeight {
		exportLog.Debug("no new blocks to export",
			"last-exported", last.LastExportedHeight,
			"latest", latestHeight)
		return nil
	}

	uploader, err := e.s3UploaderFactory(ctx, cfg.Region)
	if err != nil {
		exportLog.Warn("failed to build S3 uploader, deferring to next scheduled run", "err", err)
		return nil
	}

	prefix := normalizePrefix(cfg.Prefix)

	availableBlocks := latestHeight - startHeight + 1
	fullPages := int(availableBlocks) / defaultPageSize

	if fullPages == 0 {
		exportLog.Debug("not enough blocks for a full page yet",
			"available", availableBlocks,
			"page-size", defaultPageSize)
		return nil
	}

	for page := 0; page < fullPages; page++ {
		pageStart := startHeight + int64(page*defaultPageSize)
		pageEnd := pageStart + int64(defaultPageSize) - 1

		exportLog.Info("exporting result page",
			"start", pageStart,
			"end", pageEnd,
			"bucket", cfg.Bucket)

		if err := e.exportPage(ctx, cfg.RPCEndpoint, uploader, cfg.Bucket, prefix, pageStart, pageEnd); err != nil {
			exportLog.Warn("page export failed, deferring remaining pages to next scheduled run",
				"start", pageStart, "end", pageEnd, "err", err)
			return nil
		}

		if err := e.writeExportState(exportState{LastExportedHeight: pageEnd}); err != nil {
			exportLog.Warn("failed to persist export state, deferring to next scheduled run",
				"last-exported", pageEnd, "err", err)
			return nil
		}
	}

	lastExported := startHeight + int64(fullPages*defaultPageSize) - 1
	exportLog.Info("export complete",
		"pages", fullPages,
		"last-exported", lastExported,
		"latest-available", latestHeight)

	return nil
}

// exportPage collects block results for [start, end] and streams a gzipped
// NDJSON file to S3.
func (e *ResultExporter) exportPage(
	ctx context.Context,
	rpcEndpoint string,
	uploader seis3.Uploader,
	bucket, prefix string,
	start, end int64,
) error {
	key := fmt.Sprintf("%s%d-%d.ndjson.gz", prefix, start, end)

	pr, pw := io.Pipe()

	collectErr := make(chan error, 1)
	go func() {
		collectErr <- e.collectResults(ctx, rpcEndpoint, pw, start, end)
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

	cErr := <-collectErr
	if uploadErr != nil {
		return uploadErr
	}
	return cErr
}

// collectResults queries block_results for each height and writes gzipped
// NDJSON to wc. Each line is a JSON object with height, time, and the raw
// block_results response.
func (e *ResultExporter) collectResults(ctx context.Context, rpcEndpoint string, wc io.WriteCloser, start, end int64) (retErr error) {
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

	for h := start; h <= end; h++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		result, err := queryBlockResults(ctx, rpcEndpoint, h)
		if err != nil {
			return fmt.Errorf("querying block_results at height %d: %w", h, err)
		}

		record := map[string]any{
			"height":        h,
			"exported_at":   time.Now().UTC().Format(time.RFC3339),
			"block_results": result,
		}

		line, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshaling result at height %d: %w", h, err)
		}
		line = append(line, '\n')

		if _, err := gw.Write(line); err != nil {
			return fmt.Errorf("writing result at height %d: %w", h, err)
		}
	}

	return nil
}

func queryLatestHeight(ctx context.Context, rpcEndpoint string) (int64, error) {
	url := rpcEndpoint + "/status"
	ctx, cancel := context.WithTimeout(ctx, exportRPCTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	var rpcResp struct {
		SyncInfo struct {
			LatestBlockHeight string `json:"latest_block_height"`
		} `json:"sync_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return 0, fmt.Errorf("decoding /status response: %w", err)
	}

	h, err := strconv.ParseInt(rpcResp.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing latest_block_height %q: %w", rpcResp.SyncInfo.LatestBlockHeight, err)
	}
	if h <= 0 {
		return 0, fmt.Errorf("latest_block_height is %d, node may still be syncing", h)
	}
	return h, nil
}

func queryBlockResults(ctx context.Context, rpcEndpoint string, height int64) (json.RawMessage, error) {
	url := fmt.Sprintf("%s/block_results?height=%d", rpcEndpoint, height)
	ctx, cancel := context.WithTimeout(ctx, exportRPCTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	return json.RawMessage(body), nil
}

func parseExportConfig(params map[string]any) (ResultExportConfig, error) {
	bucket, _ := params["bucket"].(string)
	prefix, _ := params["prefix"].(string)
	region, _ := params["region"].(string)
	rpcEndpoint, _ := params["rpcEndpoint"].(string)
	canonicalRPC, _ := params["canonicalRpc"].(string)

	if bucket == "" {
		return ResultExportConfig{}, fmt.Errorf("result-export: missing required param 'bucket'")
	}
	if region == "" {
		return ResultExportConfig{}, fmt.Errorf("result-export: missing required param 'region'")
	}
	if rpcEndpoint == "" {
		rpcEndpoint = defaultRPCEndpoint
	}

	return ResultExportConfig{
		Bucket:       bucket,
		Prefix:       prefix,
		Region:       region,
		RPCEndpoint:  rpcEndpoint,
		CanonicalRPC: canonicalRPC,
	}, nil
}

func (e *ResultExporter) readExportState() exportState {
	data, err := os.ReadFile(filepath.Join(e.homeDir, exportStateFile))
	if err != nil {
		return e.bootstrapExportState()
	}
	var state exportState
	if err := json.Unmarshal(data, &state); err != nil {
		return e.bootstrapExportState()
	}
	return state
}

// bootstrapExportState reads the snapshot height file written by the restorer
// and uses it as the initial last-exported height, so the exporter begins at
// the first block after the restored snapshot rather than from block 1.
func (e *ResultExporter) bootstrapExportState() exportState {
	data, err := os.ReadFile(filepath.Join(e.homeDir, SnapshotHeightFile))
	if err != nil {
		return exportState{}
	}
	h, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || h <= 0 {
		return exportState{}
	}
	exportLog.Info("bootstrapping export state from snapshot height", "height", h)
	return exportState{LastExportedHeight: h}
}

func (e *ResultExporter) writeExportState(state exportState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling export state: %w", err)
	}
	path := filepath.Join(e.homeDir, exportStateFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing export state: %w", err)
	}
	return nil
}
