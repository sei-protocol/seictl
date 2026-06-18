package seinode

import (
	"context"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// nodePhases is the SeiNodePhase enum (seinode_types.go:251). A SeiNode
// reaches Running, NOT Ready — `watch --until=Ready` against a node is a
// usage error, not a 15m timeout (LLD §1.5 / D2).
var nodePhases = []string{"Pending", "Initializing", "Running", "Failed", "Terminating"}

func watchAction(ctx context.Context, c *cli.Command) error {
	name := c.StringArg("name")
	if name == "" {
		cliutil.EmitStatus(os.Stderr, cliutil.UsageError("name argument required: seictl node watch <name>"))
		return cli.Exit("", 1)
	}
	until := c.String("until")
	if until == "" {
		cliutil.EmitStatus(os.Stderr, cliutil.UsageError("--until=<phase> is required (e.g. --until=Running)"))
		return cli.Exit("", 1)
	}
	if err := cliutil.ValidatePhase(until, nodePhases); err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	timeout := c.Duration("timeout")

	kc := cliutil.LoadKubeconfig(c.String("kubeconfig"), c.String("namespace"))
	cfg, err := kc.RESTConfig()
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	ns, err := kc.Namespace()
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	if err := cliutil.RunWatch(ctx, cfg, kind.GVR, ns, name, until, timeout, os.Stdout); err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

var watchCmd = cli.Command{
	Name:      "watch",
	Usage:     "Stream SeiNode events as NDJSON until a phase is reached",
	ArgsUsage: "<name>",
	Description: "Streams every SeiNode event for <name> as one NDJSON line " +
		"on stdout, exiting 0 when .status.phase matches --until or 1 on " +
		"timeout, terminal Failed phase, or transient API error. " +
		"Legal --until phases: Pending, Initializing, Running, Failed, " +
		"Terminating. A SeiNode reaches Running (there is no Ready). " +
		"Discrimination on stderr via metav1.Status.reason (Timeout, " +
		"InternalError, etc.).",
	Arguments: []cli.Argument{
		&cli.StringArg{Name: "name", UsageText: "metadata.name of the SeiNode"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:     "until",
			Required: true,
			Usage:    "Phase to wait for (e.g. --until=Running). Matches .status.phase exactly; validated against the SeiNode enum at parse.",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Value: 15 * time.Minute,
			Usage: "Watch timeout; exits with metav1.Status reason=Timeout when exceeded",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (honors KUBECONFIG colon-merge); defaults to $HOME/.kube/config or in-cluster",
		},
	},
	Action: watchAction,
}
