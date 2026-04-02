package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/sidecar/shadow/analysis"
)

var reportSummaryCmd = cli.Command{
	Name:  "summary",
	Usage: "Aggregate comparison statistics for a block range",
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
			Name:  "max-pages",
			Usage: "Maximum S3 pages to fetch",
			Value: 100,
		},
	),
	Action: runReportSummary,
}

func runReportSummary(ctx context.Context, cmd *cli.Command) error {
	store, err := storeFromFlags(ctx, cmd)
	if err != nil {
		return err
	}

	out, err := store.Summarize(ctx, analysis.SummarizeInput{
		Start:    int64(cmd.Int("start")),
		End:      int64(cmd.Int("end")),
		MaxPages: int(cmd.Int("max-pages")),
	})
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	renderSummaryOutput(out)
	return nil
}

func renderSummaryOutput(out *analysis.SummarizeOutput) {
	fmt.Fprintf(os.Stdout, "Range:            %d - %d (%d blocks, %d pages)\n",
		out.Range.Start, out.Range.End, out.Range.BlockCount, out.Range.PagesFetched)

	pct := out.Totals.MatchRate * 100
	fmt.Fprintf(os.Stdout, "Match rate:       %.1f%% (%d / %d)\n",
		pct, out.Totals.MatchedBlocks, out.Totals.TotalBlocks)
	fmt.Fprintf(os.Stdout, "Diverged blocks:  %d\n", out.Totals.DivergedBlocks)

	if out.Truncated {
		fmt.Fprintf(os.Stdout, "\n** Results truncated — increase --max-pages for full coverage **\n")
	}

	if out.Totals.DivergedBlocks > 0 {
		fmt.Fprintf(os.Stdout, "\nLayer 0 breakdown:\n")
		fmt.Fprintf(os.Stdout, "  AppHash:           %d\n", out.Layer0Breakdown.AppHashMismatches)
		fmt.Fprintf(os.Stdout, "  LastResultsHash:   %d\n", out.Layer0Breakdown.LastResultsHashMismatches)
		fmt.Fprintf(os.Stdout, "  GasUsed:           %d\n", out.Layer0Breakdown.GasUsedMismatches)
	}

	if out.Layer1Breakdown != nil {
		fmt.Fprintf(os.Stdout, "\nLayer 1 breakdown:\n")
		fmt.Fprintf(os.Stdout, "  TX count mismatches: %d\n", out.Layer1Breakdown.TxCountMismatches)
		for field, count := range out.Layer1Breakdown.ByField {
			fmt.Fprintf(os.Stdout, "  %s: %d\n", field, count)
		}
	}
}
