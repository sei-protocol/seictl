package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seictl/sidecar/shadow/analysis"
)

// s3Flags returns the shared flags for all S3-backed report commands.
func s3Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "env",
			Usage: "Environment shorthand (expands to '{env}-sei-shadow-results')",
		},
		&cli.StringFlag{
			Name:    "bucket",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_BUCKET"),
			Usage:   "S3 bucket name",
		},
		&cli.StringFlag{
			Name:    "prefix",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_PREFIX"),
			Usage:   "S3 key prefix",
			Value:   "shadow-results/",
		},
		&cli.StringFlag{
			Name:    "region",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_REGION"),
			Usage:   "AWS region",
			Value:   "eu-central-1",
		},
		&cli.BoolFlag{
			Name:  "json",
			Usage: "Output raw JSON instead of human-readable format",
		},
	}
}

// storeFromFlags builds an analysis.Store from the common S3 CLI flags.
func storeFromFlags(ctx context.Context, cmd *cli.Command) (*analysis.Store, error) {
	bucket, prefix, region, err := analysis.ResolveRef(
		cmd.String("env"),
		cmd.String("bucket"),
		cmd.String("prefix"),
		cmd.String("region"),
	)
	if err != nil {
		return nil, fmt.Errorf("resolving S3 ref: %w", err)
	}

	lister, err := seis3.DefaultObjectListerFactory(ctx, region)
	if err != nil {
		return nil, err
	}
	downloader, err := seis3.DefaultDownloaderFactory(ctx, region)
	if err != nil {
		return nil, err
	}

	return analysis.NewStore(lister, downloader, bucket, prefix), nil
}
