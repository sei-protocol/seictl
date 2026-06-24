package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/urfave/cli/v3"

	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seictl/sidecar/shadow"
	"github.com/sei-protocol/seictl/sidecar/tasks"
)

var reportCmd = cli.Command{
	Name:  "report",
	Usage: "Analyze shadow chain comparison data",
	Commands: []*cli.Command{
		&reportDivergenceCmd,
		&reportDigestCmd,
		&reportListCmd,
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
			Usage:   "S3 key prefix (used with --height to compute key)",
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
	if cmd.IsSet("key") && cmd.IsSet("height") {
		return fmt.Errorf("--key and --height are mutually exclusive")
	}

	bucket := cmd.String("bucket")
	key := cmd.String("key")
	region := cmd.String("region")
	prefix := cmd.String("prefix")

	// When --env or --height is used, resolve to bucket/key.
	if cmd.IsSet("env") || cmd.IsSet("height") {
		resolved, resolvedPrefix, resolvedRegion, err := resolveS3Ref(
			cmd.String("env"), bucket, prefix, region,
		)
		if err != nil {
			return err
		}
		bucket = resolved
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

	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	fmt.Print(shadow.RenderMarkdown(report))
	return nil
}

var reportDigestCmd = cli.Command{
	Name:  "digest",
	Usage: "Fetch and render an evm-logical-digest record from S3",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "env",
			Usage: "Environment shorthand (expands to '{env}-sei-shadow-results')",
		},
		&cli.StringFlag{
			Name:    "bucket",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_BUCKET"),
			Usage:   "S3 bucket containing the record",
		},
		&cli.StringFlag{
			Name:  "key",
			Usage: "S3 object key (e.g. shadow-results/endpoint-digest-198740042-semantic.json.gz)",
		},
		&cli.IntFlag{
			Name:  "height",
			Usage: "Block height of the digest record (used with --normalization, alternative to --key)",
		},
		&cli.StringFlag{
			Name:  "normalization",
			Usage: "Memiavl normalization of the digest record (used with --height)",
		},
		&cli.StringFlag{
			Name:    "prefix",
			Sources: cli.EnvVars("SEI_RESULT_EXPORT_PREFIX"),
			Usage:   "S3 key prefix (used with --height to compute key)",
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
	Action: runReportDigest,
}

func runReportDigest(ctx context.Context, cmd *cli.Command) error {
	if cmd.IsSet("key") && cmd.IsSet("height") {
		return fmt.Errorf("--key and --height are mutually exclusive")
	}

	bucket := cmd.String("bucket")
	key := cmd.String("key")
	region := cmd.String("region")
	prefix := cmd.String("prefix")

	if cmd.IsSet("env") || cmd.IsSet("height") {
		resolved, resolvedPrefix, resolvedRegion, err := resolveS3Ref(
			cmd.String("env"), bucket, prefix, region,
		)
		if err != nil {
			return err
		}
		bucket = resolved
		region = resolvedRegion
		if cmd.IsSet("height") {
			if !cmd.IsSet("normalization") {
				return fmt.Errorf("--normalization is required with --height")
			}
			key = fmt.Sprintf("%sendpoint-digest-%d-%s.json.gz",
				resolvedPrefix, cmd.Int("height"), cmd.String("normalization"))
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

	record, err := fetchDigestRecord(ctx, downloader, bucket, key)
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(record)
	}

	fmt.Print(renderDigestMarkdown(record))
	return nil
}

// fetchDigestRecord downloads and decodes an EndpointDigestRecord from S3,
// gunzipping when the key is .gz — mirroring shadow.FetchReport for the digest
// artifact the evm-logical-digest task publishes.
func fetchDigestRecord(ctx context.Context, downloader seis3.Downloader, bucket, key string) (*tasks.EndpointDigestRecord, error) {
	resp, err := downloader.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("downloading s3://%s/%s: %w", bucket, key, err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if strings.HasSuffix(key, ".gz") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("decompressing digest record: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	var record tasks.EndpointDigestRecord
	if err := json.NewDecoder(reader).Decode(&record); err != nil {
		return nil, fmt.Errorf("decoding digest record: %w", err)
	}
	return &record, nil
}

// renderDigestMarkdown produces a human-readable view of an EndpointDigestRecord
// in the same shape as the divergence report renderer: header, overall verdict,
// per-bucket flatkv/memiavl digests, and the axes the digest proves.
func renderDigestMarkdown(r *tasks.EndpointDigestRecord) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# EVM Logical Digest — Height %d (%s)\n\n", r.Height, r.Normalization)
	fmt.Fprintf(&b, "**Generated at:** %s\n\n", r.GeneratedAt)
	fmt.Fprintf(&b, "**Overall match:** %s\n\n", matchIcon(r.Match))
	fmt.Fprintf(&b, "| Backend | Final Digest |\n")
	fmt.Fprintf(&b, "|---------|--------------|\n")
	fmt.Fprintf(&b, "| flatkv | %s |\n", truncateDigest(r.FlatKVDigest))
	fmt.Fprintf(&b, "| memiavl | %s |\n\n", truncateDigest(r.MemIAVLDigest))

	fmt.Fprintf(&b, "## Per-Bucket Digests\n\n")
	fmt.Fprintf(&b, "| Bucket | FlatKV | MemIAVL | Match |\n")
	fmt.Fprintf(&b, "|--------|--------|---------|-------|\n")
	for _, name := range []string{"account", "code", "storage", "legacy"} {
		bkt, ok := r.PerBucket[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			name, truncateDigest(bkt.FlatKV), truncateDigest(bkt.MemIAVL), matchIcon(bkt.Match))
	}
	fmt.Fprintf(&b, "\n")

	axes := append([]string(nil), r.AxesProved...)
	sort.Strings(axes)
	fmt.Fprintf(&b, "**Axes proved:** %s\n", strings.Join(axes, ", "))
	return b.String()
}

func matchIcon(match bool) string {
	if match {
		return "✅"
	}
	return "❌"
}

func truncateDigest(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:8] + "..." + h[len(h)-4:]
}

// resolveS3Ref converts --env/--bucket/--prefix/--region flags to concrete values.
func resolveS3Ref(env, bucket, prefix, region string) (string, string, string, error) {
	switch {
	case env != "" && bucket != "":
		return "", "", "", fmt.Errorf("--env and --bucket are mutually exclusive")
	case env != "":
		bucket = env + "-sei-shadow-results"
	case bucket != "":
		// use as-is
	default:
		return "", "", "", fmt.Errorf("one of --env or --bucket is required")
	}
	if prefix == "" {
		prefix = "shadow-results/"
	}
	if prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	if region == "" {
		region = "eu-central-1"
	}
	return bucket, prefix, region, nil
}
