package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seictl/sidecar/shadow"
	"github.com/sei-protocol/seictl/sidecar/shadow/analysis"
)

var reportCmd = cli.Command{
	Name:  "report",
	Usage: "Analyze shadow chain comparison data",
	Commands: []*cli.Command{
		&reportDivergenceCmd,
		&reportListCmd,
		&reportSummaryCmd,
		&reportSearchCmd,
		&reportTrendsCmd,
	},
}

var reportDivergenceCmd = cli.Command{
	Name:  "divergence",
	Usage: "Fetch and render a divergence report from S3",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "env",
			Usage: "Environment shorthand (expands to '{env}-sei-shadow-results')",
		},
		&cli.StringFlag{
			Name:    "bucket",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_BUCKET"),
			Usage:   "S3 bucket containing the report",
		},
		&cli.StringFlag{
			Name:  "key",
			Usage: "S3 object key (e.g. shadow-results/divergence-198740042.report.json.gz)",
		},
		&cli.IntFlag{
			Name:  "height",
			Usage: "Block height of the divergence report (alternative to --key)",
		},
		&cli.StringFlag{
			Name:    "prefix",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_PREFIX"),
			Usage:   "S3 key prefix (used with --height)",
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
			Usage: "Output raw JSON instead of markdown",
		},
	},
	Action: runReportDivergence,
}

func runReportDivergence(ctx context.Context, cmd *cli.Command) error {
	bucket := cmd.String("bucket")
	key := cmd.String("key")
	region := cmd.String("region")
	prefix := cmd.String("prefix")
	outputJSON := cmd.Bool("json")

	// Resolve --env/--bucket and --key/--height.
	if cmd.IsSet("key") && cmd.IsSet("height") {
		return fmt.Errorf("--key and --height are mutually exclusive")
	}

	if cmd.IsSet("height") || cmd.IsSet("env") {
		resolvedBucket, resolvedPrefix, resolvedRegion, err := analysis.ResolveRef(
			cmd.String("env"), bucket, prefix, region,
		)
		if err != nil {
			return err
		}
		bucket = resolvedBucket
		region = resolvedRegion
		if cmd.IsSet("height") {
			key = fmt.Sprintf("%sdivergence-%d.report.json.gz", resolvedPrefix, cmd.Int("height"))
		}
	}

	if bucket == "" {
		return fmt.Errorf("one of --env or --bucket is required")
	}
	if key == "" {
		return fmt.Errorf("one of --key or --height is required")
	}

	downloader, err := seis3.DefaultDownloaderFactory(ctx, region)
	if err != nil {
		return err
	}

	report, err := shadow.FetchReport(ctx, downloader, bucket, key)
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
