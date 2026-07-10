package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// stateSyncPreset is the embedded preset the state-sync command renders.
const stateSyncPreset = "state-sync"

// phaseComplete is the SeiNodeTaskWorkflow terminal-success phase the paved-road
// command waits for. Reaching it exits 0; the Failed dual exits nonzero (via
// cliutil.MatchPhase), giving kubectl-wait-compatible semantics.
const phaseComplete = "Complete"

func stateSyncAction(ctx context.Context, c *cli.Command) error {
	node := c.StringArg("node")
	if node == "" {
		cliutil.EmitStatus(os.Stderr, cliutil.UsageError("node argument required: seictl workflow state-sync <node>"))
		return cli.Exit("", 1)
	}
	name := c.String("name")
	if name == "" {
		name = node + "-state-sync"
	}

	args := renderArgs{
		preset:       stateSyncPreset,
		name:         name,
		namespace:    c.String("namespace"),
		target:       node,
		requirePhase: c.String("require-phase"),
		configPatch:  c.String("config-patch"),
		rpcServers:   c.StringSlice("rpc-servers"),
		sets:         c.StringSlice("set"),
	}
	dryRun := c.Bool("dry-run")
	noWatch := c.Bool("no-watch")
	timeout := c.Duration("timeout")

	kc := cliutil.LoadKubeconfig(c.String("kubeconfig"), args.namespace)
	cfg, err := kc.RESTConfig()
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	resolvedNS, err := kc.Namespace()
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	args.namespace = resolvedNS

	obj, err := render(args)
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	mode := "applying"
	if dryRun {
		mode = "applying (dry-run)"
	}
	fmt.Fprintf(os.Stderr, "seictl: %s SeiNodeTaskWorkflow %s/%s (state-sync target=%s) to %s\n",
		mode, obj.GetNamespace(), obj.GetName(), node, cfg.Host)

	kcli, err := cliutil.NewClient(cfg)
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	if err := kind.Apply(ctx, kcli, obj, dryRun); err != nil {
		cliutil.EmitStatus(os.Stderr, fmt.Errorf("apply SeiNodeTaskWorkflow %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		return cli.Exit("", 1)
	}

	// dry-run and --no-watch stop after apply, emitting the CR for inspection or
	// GitOps capture; the watch path streams plan progress instead.
	if dryRun || noWatch {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(obj.Object); err != nil {
			return fmt.Errorf("encode result: %w", err)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "seictl: watching %s/%s until %s (timeout %s)\n", resolvedNS, name, phaseComplete, timeout)
	if err := cliutil.RunWatch(ctx, cfg, kind.GVR, resolvedNS, name, phaseComplete, timeout, os.Stdout); err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

var stateSyncCmd = cli.Command{
	Name:                      "state-sync",
	DisableSliceFlagSeparator: true,
	Usage:                     "Re-bootstrap a node through CometBFT state sync and watch it to completion",
	Description: "Renders the StateSync recipe against <node> from the embedded " +
		"state-sync preset, server-side-applies the resulting " +
		"SeiNodeTaskWorkflow, then streams its plan progress as NDJSON on " +
		"stdout until the workflow reaches a terminal phase. " +
		"\n\n" +
		"Exit codes are kubectl-wait-compatible: 0 when .status.phase reaches " +
		"Complete, nonzero on Failed or --timeout. " +
		"\n\n" +
		"The workflow is named <node>-state-sync unless --name is given. " +
		"--config-patch merges seid config before the resync (e.g. app.toml " +
		"[state-store] evm-ss-split=true for the giga migration); " +
		"--rpc-servers overrides witness resolution (>=2 or the controller " +
		"fails the plan closed). " +
		"\n\n" +
		"Cluster + namespace resolve from --kubeconfig and -n exactly as " +
		"`node apply`; the resolved target prints on stderr before applying.",
	ArgsUsage: "<node>",
	Arguments: []cli.Argument{
		&cli.StringArg{Name: "node", UsageText: "metadata.name of the target SeiNode"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:  "name",
			Usage: "metadata.name of the workflow CR (default: <node>-state-sync)",
		},
		&cli.StringFlag{
			Name:  "config-patch",
			Usage: "Path to a YAML/JSON config-patch file merged before the resync (file -> section -> value; e.g. app.toml: {state-store: {evm-ss-split: true}})",
		},
		&cli.StringSliceFlag{
			Name:  "rpc-servers",
			Usage: "State-sync witness RPC endpoints (host:port). Overrides the node's resolved syncers; must carry >=2 or the plan refuses to compile. Repeatable.",
		},
		&cli.StringFlag{
			Name:  "require-phase",
			Usage: "SeiNode phase the target must reach before the workflow dispatches (default Running, per the CRD)",
		},
		&cli.StringSliceFlag{
			Name:  "set",
			Usage: "Strategic-merge override, dotted path with optional list-index suffix. Repeatable.",
		},
		&cli.BoolFlag{
			Name:  "no-watch",
			Usage: "Apply and exit without watching (print the applied CR)",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Validate via server-side-apply dry-run and emit the would-be-applied CR without persisting or watching",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Value: 60 * time.Minute,
			Usage: "Watch timeout; exits with metav1.Status reason=Timeout when exceeded. A full state sync can be slow — size for the target's dataset.",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (honors KUBECONFIG colon-merge); defaults to $HOME/.kube/config or in-cluster",
		},
	},
	Action: stateSyncAction,
}
