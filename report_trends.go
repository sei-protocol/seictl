package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/sidecar/shadow/analysis"
)

var reportTrendsCmd = cli.Command{
	Name:  "trends",
	Usage: "Analyze divergence patterns over a block range",
	Flags: append(s3Flags(),
		&cli.IntFlag{
			Name:     "start",
			Usage:    "Start block height (inclusive)",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "end",
			Usage:    "End block height (inclusive)",
			Required: true,
		},
		&cli.IntFlag{
			Name:  "window-size",
			Usage: "Blocks per analysis window",
			Value: 10000,
		},
		&cli.IntFlag{
			Name:  "max-pages",
			Usage: "Maximum S3 pages to fetch",
			Value: 200,
		},
	),
	Action: runReportTrends,
}

func runReportTrends(ctx context.Context, cmd *cli.Command) error {
	store, err := storeFromFlags(ctx, cmd)
	if err != nil {
		return err
	}

	out, err := store.Trends(ctx, analysis.TrendsInput{
		Start:      int64(cmd.Int("start")),
		End:        int64(cmd.Int("end")),
		WindowSize: int64(cmd.Int("window-size")),
		MaxPages:   int(cmd.Int("max-pages")),
	})
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	renderTrendsOutput(out)
	return nil
}

func renderTrendsOutput(out *analysis.TrendsOutput) {
	fmt.Fprintf(os.Stdout, "Range:          %d - %d (%d blocks, %d pages)\n",
		out.Range.Start, out.Range.End, out.Range.BlockCount, out.Range.PagesFetched)
	fmt.Fprintf(os.Stdout, "Overall rate:   %.1f%%\n\n", out.OverallRate*100)

	if len(out.Buckets) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "WINDOW\tBLOCKS\tDIVERGED\tRATE")
		for _, b := range out.Buckets {
			fmt.Fprintf(w, "%d - %d\t%d\t%d\t%.1f%%\n",
				b.StartHeight, b.EndHeight, b.TotalBlocks, b.DivergedBlocks, b.DivergenceRate*100)
		}
		w.Flush()
	}

	if len(out.Clusters) > 0 {
		fmt.Fprintf(os.Stdout, "\nClusters (%d):\n", len(out.Clusters))
		for _, c := range out.Clusters {
			fmt.Fprintf(os.Stdout, "  %d - %d (%d blocks)\n", c.StartHeight, c.EndHeight, c.Length)
		}
	}

	if len(out.Transitions) > 0 {
		fmt.Fprintf(os.Stdout, "\nTransitions (%d):\n", len(out.Transitions))
		for _, t := range out.Transitions {
			fmt.Fprintf(os.Stdout, "  Height %d: %.1f%% → %.1f%% (%s)\n",
				t.Height, t.RateBefore*100, t.RateAfter*100, t.Direction)
		}
	}
}
