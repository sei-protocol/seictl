package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/sidecar/shadow/analysis"
)

var reportSearchCmd = cli.Command{
	Name:  "search",
	Usage: "Find divergences matching specific criteria",
	Flags: append(s3Flags(),
		&cli.IntFlag{
			Name:  "start",
			Usage: "Start block height (inclusive)",
		},
		&cli.IntFlag{
			Name:  "end",
			Usage: "End block height (inclusive)",
		},
		&cli.IntFlag{
			Name:  "layer",
			Usage: "Filter to divergences at this layer (0 or 1)",
			Value: -1,
		},
		&cli.StringSliceFlag{
			Name:  "field",
			Usage: "Filter to specific mismatched fields (repeatable: appHash, lastResultsHash, gasUsed, code, gasWanted, log, events, presence)",
		},
		&cli.IntFlag{
			Name:  "limit",
			Usage: "Maximum results to return",
			Value: 50,
		},
		&cli.IntFlag{
			Name:  "offset",
			Usage: "Skip first N matches",
			Value: 0,
		},
	),
	Action: runReportSearch,
}

func runReportSearch(ctx context.Context, cmd *cli.Command) error {
	store, err := storeFromFlags(ctx, cmd)
	if err != nil {
		return err
	}

	input := analysis.SearchInput{
		Fields: cmd.StringSlice("field"),
		Limit:  int(cmd.Int("limit")),
		Offset: int(cmd.Int("offset")),
	}
	if cmd.IsSet("start") {
		v := int64(cmd.Int("start"))
		input.Start = &v
	}
	if cmd.IsSet("end") {
		v := int64(cmd.Int("end"))
		input.End = &v
	}
	if cmd.Int("layer") >= 0 {
		v := int(cmd.Int("layer"))
		input.Layer = &v
	}

	out, err := store.Search(ctx, input)
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	renderSearchOutput(out)
	return nil
}

func renderSearchOutput(out *analysis.SearchOutput) {
	fmt.Fprintf(os.Stderr, "Found %d match(es)\n", out.TotalMatches)
	if out.HasMore {
		fmt.Fprintf(os.Stderr, "(more results available — increase --limit or use --offset)\n")
	}

	if len(out.Results) == 0 {
		return
	}

	fmt.Fprintln(os.Stderr)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "HEIGHT\tLAYER\tFIELDS")
	for _, m := range out.Results {
		var fields []string
		fields = append(fields, m.Layer0Fields...)
		if m.Layer1Summary != nil {
			fields = append(fields, m.Layer1Summary.DivergedFields...)
		}
		fmt.Fprintf(w, "%d\t%d\t%s\n", m.Height, m.DivergenceLayer, strings.Join(fields, ", "))
	}
	w.Flush()
}
