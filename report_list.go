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

var reportListCmd = cli.Command{
	Name:  "list",
	Usage: "List available shadow comparison data in S3",
	Flags: append(s3Flags(),
		&cli.IntFlag{
			Name:  "start",
			Usage: "Minimum block height (inclusive)",
		},
		&cli.IntFlag{
			Name:  "end",
			Usage: "Maximum block height (inclusive)",
		},
		&cli.IntFlag{
			Name:  "limit",
			Usage: "Maximum entries to return",
			Value: 100,
		},
	),
	Action: runReportList,
}

func runReportList(ctx context.Context, cmd *cli.Command) error {
	store, err := storeFromFlags(ctx, cmd)
	if err != nil {
		return err
	}

	input := analysis.ListInput{
		Limit: int(cmd.Int("limit")),
	}
	if cmd.IsSet("start") {
		v := int64(cmd.Int("start"))
		input.Start = &v
	}
	if cmd.IsSet("end") {
		v := int64(cmd.Int("end"))
		input.End = &v
	}

	out, err := store.List(ctx, input)
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	renderListOutput(out)
	return nil
}

func renderListOutput(out *analysis.ListOutput) {
	fmt.Fprintf(os.Stderr, "Comparison data: %d pages, %d divergence reports\n",
		out.Summary.TotalPages, out.Summary.TotalDivergenceReports)
	if out.Summary.TotalPages > 0 {
		fmt.Fprintf(os.Stderr, "Height range: %d - %d (%d blocks)\n\n",
			out.Summary.MinHeight, out.Summary.MaxHeight, out.Summary.TotalBlocksCovered)
	}

	if len(out.Pages) > 0 || len(out.DivergenceReports) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "TYPE\tHEIGHT RANGE\tBLOCKS\tSIZE")
		for _, p := range out.Pages {
			fmt.Fprintf(w, "page\t%d - %d\t%d\t%s\n",
				p.StartHeight, p.EndHeight, p.BlockCount, humanSize(p.SizeBytes))
		}
		for _, r := range out.DivergenceReports {
			fmt.Fprintf(w, "divergence\t%d\t1\t%s\n",
				r.Height, humanSize(r.SizeBytes))
		}
		w.Flush()
	}
}

func humanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}
