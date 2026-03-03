package tasks

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sei-sidecar/engine"
)

const genesisMarkerFile = ".sei-sidecar-genesis-done"

// GenesisS3Config holds S3 coordinates for genesis.json download.
type GenesisS3Config struct {
	Bucket string
	Key    string
	Region string
}

// GenesisFetcher downloads genesis.json from S3 and writes it to the config directory.
type GenesisFetcher struct {
	homeDir         string
	s3ClientFactory S3ClientFactory
}

// NewGenesisFetcher creates a fetcher targeting the given home directory.
func NewGenesisFetcher(homeDir string, factory S3ClientFactory) *GenesisFetcher {
	if factory == nil {
		factory = DefaultS3ClientFactory
	}
	return &GenesisFetcher{
		homeDir:         homeDir,
		s3ClientFactory: factory,
	}
}

// Handler returns an engine.TaskHandler that parses params and delegates to Fetch.
func (g *GenesisFetcher) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		cfg, err := parseGenesisConfig(params)
		if err != nil {
			return err
		}
		return g.Fetch(ctx, cfg)
	}
}

// Fetch downloads genesis.json from S3, skipping if the marker file exists.
func (g *GenesisFetcher) Fetch(ctx context.Context, cfg GenesisS3Config) error {
	if markerExists(g.homeDir, genesisMarkerFile) {
		return nil
	}

	s3Client, err := g.s3ClientFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("building S3 client: %w", err)
	}

	output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(cfg.Key),
	})
	if err != nil {
		return fmt.Errorf("s3 GetObject %s/%s: %w", cfg.Bucket, cfg.Key, err)
	}
	defer output.Body.Close()

	destDir := filepath.Join(g.homeDir, "config")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	destPath := filepath.Join(destDir, "genesis.json")
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, output.Body); err != nil {
		return fmt.Errorf("writing %s: %w", destPath, err)
	}

	return writeMarker(g.homeDir, genesisMarkerFile)
}

func parseGenesisConfig(params map[string]any) (GenesisS3Config, error) {
	uri, _ := params["uri"].(string)
	region, _ := params["region"].(string)

	if uri == "" {
		return GenesisS3Config{}, fmt.Errorf("configure-genesis: missing required param 'uri'")
	}
	if region == "" {
		return GenesisS3Config{}, fmt.Errorf("configure-genesis: missing required param 'region'")
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return GenesisS3Config{}, fmt.Errorf("configure-genesis: invalid uri %q: %w", uri, err)
	}
	if parsed.Scheme != "s3" {
		return GenesisS3Config{}, fmt.Errorf("configure-genesis: uri must use s3:// scheme, got %q", parsed.Scheme)
	}

	bucket := parsed.Host
	key := strings.TrimPrefix(parsed.Path, "/")
	if bucket == "" || key == "" {
		return GenesisS3Config{}, fmt.Errorf("configure-genesis: uri must be s3://bucket/key, got %q", uri)
	}

	return GenesisS3Config{Bucket: bucket, Key: key, Region: region}, nil
}
