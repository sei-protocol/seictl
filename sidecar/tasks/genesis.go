package tasks

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// ConfigureGenesisRequest holds the typed parameters for the configure-genesis task.
// All fields are optional — the fetcher resolves genesis from the chain ID using
// embedded config or S3 fallback.
type ConfigureGenesisRequest struct{}

// GenesisFetcher writes genesis.json to the config directory. It first checks
// for an embedded genesis in sei-config for the chain ID. If not found, it
// falls back to downloading from S3 at {bucket}/{chainID}/genesis.json.
type GenesisFetcher struct {
	homeDir         string
	chainID         string
	genesisBucket   string
	genesisRegion   string
	s3ClientFactory S3ClientFactory
}

// NewGenesisFetcher creates a fetcher targeting the given home directory.
// chainID is the chain this sidecar is running for (typically from SEI_CHAIN_ID).
// genesisBucket and genesisRegion configure the S3 fallback location when the
// chain is not embedded in sei-config.
func NewGenesisFetcher(homeDir, chainID, genesisBucket, genesisRegion string, factory S3ClientFactory) *GenesisFetcher {
	if factory == nil {
		factory = DefaultS3ClientFactory
	}
	return &GenesisFetcher{
		homeDir:         homeDir,
		chainID:         chainID,
		genesisBucket:   genesisBucket,
		genesisRegion:   genesisRegion,
		s3ClientFactory: factory,
	}
}

// Handler returns an engine.TaskHandler that resolves genesis from embedded
// config or S3 fallback. No task parameters are required.
func (g *GenesisFetcher) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, _ ConfigureGenesisRequest) error {
		if markerExists(g.homeDir, genesisMarkerFile) {
			genesisLog.Debug("already completed, skipping")
			return nil
		}

		// Try embedded genesis first.
		if _, err := seiconfig.GenesisForChain(g.chainID); err == nil {
			return g.writeEmbeddedGenesis()
		}

		// Fall back to S3.
		if g.genesisBucket == "" || g.genesisRegion == "" {
			return fmt.Errorf("configure-genesis: chain %q is not embedded and SEI_GENESIS_BUCKET/SEI_GENESIS_REGION are not set", g.chainID)
		}

		key := g.chainID + "/genesis.json"
		genesisLog.Info("chain not embedded, fetching from S3", "chainId", g.chainID, "bucket", g.genesisBucket, "key", key)
		return g.fetchFromS3(ctx, GenesisS3Config{Bucket: g.genesisBucket, Key: key, Region: g.genesisRegion})
	})
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
