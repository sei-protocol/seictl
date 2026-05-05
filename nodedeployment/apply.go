package nodedeployment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/snd"
)

func applyAction(ctx context.Context, c *cli.Command) error {
	args := renderArgs{
		preset:    c.String("preset"),
		name:      c.String("name"),
		namespace: c.String("namespace"),
		chainID:   c.String("chain-id"),
		image:     c.String("image"),
		sets:      c.StringSlice("set"),
	}
	if c.IsSet("replicas") {
		args.replicas = int(c.Int("replicas"))
		args.hasReps = true
	}
	dryRun := c.Bool("dry-run")
	kubeconfig := c.String("kubeconfig")

	if args.namespace == "" {
		args.namespace = resolveNamespace(kubeconfig, "")
	}

	obj, err := render(args)
	if err != nil {
		emitStatus(os.Stderr, usageError("%s", err.Error()))
		return cli.Exit("", 1)
	}

	cfg, err := loadConfig(kubeconfig)
	if err != nil {
		emitStatus(os.Stderr, fmt.Errorf("load kubeconfig: %w", err))
		return cli.Exit("", 1)
	}
	kc, err := newClient(cfg)
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	if err := snd.Apply(ctx, kc, obj, dryRun); err != nil {
		emitStatus(os.Stderr, fmt.Errorf("apply SeiNodeDeployment %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		return cli.Exit("", 1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj.Object); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}

var applyCmd = cli.Command{
	Name:  "apply",
	Usage: "Render a preset and server-side-apply the resulting SeiNodeDeployment",
	Description: "Loads the named preset, applies discrete-flag and --set " +
		"overrides, and server-side-applies the result. With --dry-run, " +
		"the apiserver validates and returns the would-be-applied CR " +
		"without persisting. The post-apply CR is emitted to stdout as " +
		"JSON; errors land on stderr as a metav1.Status object.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "preset",
			Usage:    "Preset name (genesis-chain | rpc)",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "name",
			Usage:    "metadata.name of the SeiNodeDeployment",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context's namespace)",
		},
		&cli.StringFlag{
			Name:  "chain-id",
			Usage: "Chain ID (sets spec.genesis.chainId; required by genesis-chain preset)",
		},
		&cli.StringFlag{
			Name:  "image",
			Usage: "seid container image (sets spec.template.spec.image)",
		},
		&cli.IntFlag{
			Name:  "replicas",
			Usage: "Replica count (overrides preset default)",
		},
		&cli.StringSliceFlag{
			Name:  "set",
			Usage: "Strategic-merge override, dotted path (e.g. --set spec.template.spec.image=foo). Repeatable.",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Validate via server-side-apply dry-run and emit the would-be-applied CR without persisting",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (defaults to $KUBECONFIG, then $HOME/.kube/config)",
		},
	},
	Action: applyAction,
}
