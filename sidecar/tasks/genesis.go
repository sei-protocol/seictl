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
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	seiconfig "github.com/sei-protocol/sei-config"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var genesisLog = seilog.NewLogger("seictl", "task", "genesis")

const genesisMarkerFile = ".sei-sidecar-genesis-done"

// S3GetObjectAPI abstracts a single-object S3 download for small files.
type S3GetObjectAPI interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3ClientFactory builds an S3GetObjectAPI for a given region.
type S3ClientFactory func(ctx context.Context, region string) (S3GetObjectAPI, error)

// DefaultS3ClientFactory creates a real S3 client using default credentials.
func DefaultS3ClientFactory(ctx context.Context, region string) (S3GetObjectAPI, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return s3.NewFromConfig(cfg), nil
}

// GenesisS3Config holds S3 coordinates for genesis.json download.
type GenesisS3Config struct {
	Bucket string
	Key    string
	Region string
}

// GenesisFetcher writes genesis.json to the config directory. When S3 params
// are provided in the task, it downloads from S3. Otherwise it writes the
// embedded genesis for the configured chain ID (from SEI_CHAIN_ID).
type GenesisFetcher struct {
	homeDir         string
	chainID         string
	s3ClientFactory S3ClientFactory
}

// NewGenesisFetcher creates a fetcher targeting the given home directory.
// chainID is the chain this sidecar is running for (typically from SEI_CHAIN_ID).
// When a task has no S3 params, the fetcher writes embedded genesis for this chain.
func NewGenesisFetcher(homeDir string, chainID string, factory S3ClientFactory) *GenesisFetcher {
	if factory == nil {
		factory = DefaultS3ClientFactory
	}
	return &GenesisFetcher{
		homeDir:         homeDir,
		chainID:         chainID,
		s3ClientFactory: factory,
	}
}

// Handler returns an engine.TaskHandler that parses params and delegates to
// the appropriate genesis source.
func (g *GenesisFetcher) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		if markerExists(g.homeDir, genesisMarkerFile) {
			genesisLog.Debug("already completed, skipping")
			return nil
		}

		s3Cfg, err := parseGenesisS3Config(params)
		if err != nil {
			return err
		}
		if s3Cfg != nil {
			return g.fetchFromS3(ctx, *s3Cfg)
		}
		return g.writeEmbeddedGenesis()
	}
}

// Fetch downloads genesis.json from S3, skipping if the marker file exists.
// Retained for backward compatibility with callers that build GenesisS3Config
// directly.
func (g *GenesisFetcher) Fetch(ctx context.Context, cfg GenesisS3Config) error {
	if markerExists(g.homeDir, genesisMarkerFile) {
		genesisLog.Debug("already completed, skipping")
		return nil
	}
	return g.fetchFromS3(ctx, cfg)
}

func (g *GenesisFetcher) fetchFromS3(ctx context.Context, cfg GenesisS3Config) error {
	genesisLog.Info("downloading genesis.json from S3", "bucket", cfg.Bucket, "key", cfg.Key)
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
	defer func() { _ = output.Body.Close() }()

	if err := g.writeGenesisFile(func(f *os.File) error {
		_, err := io.Copy(f, output.Body)
		return err
	}); err != nil {
		return err
	}

	genesisLog.Info("genesis download complete (S3)")
	return writeMarker(g.homeDir, genesisMarkerFile)
}

func (g *GenesisFetcher) writeEmbeddedGenesis() error {
	if g.chainID == "" {
		return fmt.Errorf("configure-genesis: no S3 params and SEI_CHAIN_ID is not set")
	}

	genesisLog.Info("writing embedded genesis", "chainId", g.chainID)
	data, err := seiconfig.GenesisForChain(g.chainID)
	if err != nil {
		return fmt.Errorf("configure-genesis: %w", err)
	}

	if err := g.writeGenesisFile(func(f *os.File) error {
		_, err := f.Write(data)
		return err
	}); err != nil {
		return err
	}

	genesisLog.Info("genesis written from embedded data", "chainId", g.chainID)
	return writeMarker(g.homeDir, genesisMarkerFile)
}

// writeGenesisFile creates the config directory and genesis.json file, calling
// writeFn to populate its contents.
func (g *GenesisFetcher) writeGenesisFile(writeFn func(*os.File) error) error {
	destDir := filepath.Join(g.homeDir, "config")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	destPath := filepath.Join(destDir, "genesis.json")
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer func() { _ = f.Close() }()

	if err := writeFn(f); err != nil {
		return fmt.Errorf("writing %s: %w", destPath, err)
	}
	return nil
}

// parseGenesisS3Config extracts S3 configuration from task params. Returns nil
// (not an error) when no S3 params are present, indicating the handler should
// use the embedded genesis.
func parseGenesisS3Config(params map[string]any) (*GenesisS3Config, error) {
	uri, _ := params["uri"].(string)
	if uri == "" {
		return nil, nil
	}

	region, _ := params["region"].(string)
	if region == "" {
		return nil, fmt.Errorf("configure-genesis: 'region' is required when 'uri' is set")
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("configure-genesis: invalid uri %q: %w", uri, err)
	}
	if parsed.Scheme != "s3" {
		return nil, fmt.Errorf("configure-genesis: uri must use s3:// scheme, got %q", parsed.Scheme)
	}

	bucket := parsed.Host
	key := strings.TrimPrefix(parsed.Path, "/")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("configure-genesis: uri must be s3://bucket/key, got %q", uri)
	}

	return &GenesisS3Config{Bucket: bucket, Key: key, Region: region}, nil
}
