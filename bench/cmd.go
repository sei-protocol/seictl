// Package bench registers the `seictl bench` verb tree: render the seiload
// benchmark Job from the sei-k8s-controller harness template so GitOps
// experiments run the same Job the nightly integration suite does.
package bench

import (
	"context"
	"fmt"
	"os"

	"github.com/sei-protocol/sei-k8s-controller/harness/bench"
	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// Cmd is the `seictl bench` command tree.
var Cmd = cli.Command{
	Name:  "bench",
	Usage: "Render the seiload benchmark Job from the harness template",
	Commands: []*cli.Command{
		&renderCmd,
	},
}

var renderCmd = cli.Command{
	Name:  "render",
	Usage: "Render a seiload Job manifest",
	Description: "Renders the Job the harness runs seiload with: the profile is " +
		"read from --profile-configmap (key profile.json) and the run is labelled " +
		"sei.io/harness-run=<run-id>. The Job's activeDeadlineSeconds defaults to " +
		"--duration plus 15 minutes of slack for image pull and the post-summary " +
		"flush; the ConfigMap itself is not rendered.",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "run-id", Usage: "Run token; names the Job seiload-<run-id> and sets SEILOAD_RUN_ID", Required: true},
		&cli.StringFlag{Name: "chain-id", Usage: "Chain under test; sets SEILOAD_CHAIN_ID", Required: true},
		&cli.StringFlag{Name: "image", Usage: "seiload image reference (pin by digest)", Required: true},
		&cli.StringFlag{Name: "profile-configmap", Usage: "ConfigMap holding profile.json", Required: true},
		&cli.IntFlag{Name: "duration", Usage: "Load duration in minutes", Required: true},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace for the Job (omitted when empty)"},
		&cli.StringFlag{Name: "commit", Usage: "sei-chain commit under test; sets SEILOAD_COMMIT_ID"},
		&cli.StringFlag{Name: "workload", Usage: "Workload label; sets SEILOAD_WORKLOAD", Value: bench.DefaultWorkload},
		&cli.IntFlag{Name: "deadline-seconds", Usage: "activeDeadlineSeconds override (default: duration + 15m)"},
	},
	Action: func(_ context.Context, c *cli.Command) error {
		out, err := Render(bench.Params{
			RunID:           c.String("run-id"),
			ChainID:         c.String("chain-id"),
			Commit:          c.String("commit"),
			Image:           c.String("image"),
			DurationMinutes: c.Int("duration"),
			ProfileCM:       c.String("profile-configmap"),
			DeadlineSeconds: c.Int("deadline-seconds"),
			Namespace:       c.String("namespace"),
			Workload:        c.String("workload"),
		})
		if err != nil {
			cliutil.EmitStatus(os.Stderr, cliutil.UsageError("%s", err.Error()))
			return cli.Exit("", 1)
		}
		_, err = os.Stdout.Write(out)
		return err
	},
}

func Render(p bench.Params) ([]byte, error) {
	if err := cliutil.RequireHarnessNames(p.ChainID, p.RunID, "seiload"); err != nil {
		return nil, err
	}
	if p.DurationMinutes <= 0 {
		return nil, fmt.Errorf("--duration must be a positive number of minutes, got %d", p.DurationMinutes)
	}
	return bench.Render(p)
}
