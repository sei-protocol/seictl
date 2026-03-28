package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/sidecar/shadow"
)

var reportCmd = cli.Command{
	Name:  "report",
	Usage: "Analyze shadow chain comparison data",
	Commands: []*cli.Command{
		&reportDivergenceCmd,
	},
}

var reportDivergenceCmd = cli.Command{
	Name:  "divergence",
	Usage: "Fetch and render a divergence report from S3",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "bucket",
			Usage:    "S3 bucket containing the report",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "key",
			Usage:    "S3 object key (e.g. shadow-results/pacific-1/divergence-198740042.report.json.gz)",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "region",
			Usage: "AWS region",
			Value: "eu-central-1",
		},
		&cli.BoolFlag{
			Name:  "json",
			Usage: "Output raw JSON instead of markdown",
		},
	},
	Action: runReportDivergence,
}

func runReportDivergence(ctx context.Context, cmd *cli.Command) error {
	bucket := cmd.String("bucket")
	key := cmd.String("key")
	region := cmd.String("region")
	outputJSON := cmd.Bool("json")

	report, err := downloadDivergenceReport(ctx, bucket, key, region)
	if err != nil {
		return err
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	fmt.Print(shadow.RenderMarkdown(report))
	return nil
}

func downloadDivergenceReport(ctx context.Context, bucket, key, region string) (*shadow.DivergenceReport, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)
	resp, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("downloading s3://%s/%s: %w", bucket, key, err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if isGzipped(key) {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("decompressing report: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	var report shadow.DivergenceReport
	if err := json.NewDecoder(reader).Decode(&report); err != nil {
		return nil, fmt.Errorf("decoding report: %w", err)
	}

	return &report, nil
}

func isGzipped(key string) bool {
	return len(key) > 3 && key[len(key)-3:] == ".gz"
}
