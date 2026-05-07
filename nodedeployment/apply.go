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
	name := c.StringArg("name")
	if name == "" {
		emitStatus(os.Stderr, usageError("name argument required: seictl nd apply <name> --preset ..."))
		return cli.Exit("", 1)
	}

	args := renderArgs{
		preset:          c.String("preset"),
		name:            name,
		namespace:       c.String("namespace"),
		chainID:         c.String("chain-id"),
		image:           c.String("image"),
		sets:            c.StringSlice("set"),
		genesisAccounts: c.StringSlice("genesis-account"),
		overrides:       c.StringSlice("override"),
	}
	if c.IsSet("replicas") {
		args.replicas = int(c.Int("replicas"))
		args.hasReps = true
	}
	dryRun := c.Bool("dry-run")

	kc := loadKubeconfig(c.String("kubeconfig"), args.namespace)
	cfg, err := kc.RESTConfig()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	resolvedNS, err := kc.Namespace()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	args.namespace = resolvedNS

	obj, err := render(args)
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	mode := "applying"
	if dryRun {
		mode = "applying (dry-run)"
	}
	fmt.Fprintf(os.Stderr, "seictl: %s SeiNodeDeployment %s/%s to %s\n",
		mode, obj.GetNamespace(), obj.GetName(), cfg.Host)

	kcli, err := newClient(cfg)
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	if err := snd.Apply(ctx, kcli, obj, dryRun); err != nil {
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
		"without persisting. " +
		"\n\n" +
		"Layering, lowest precedence first: preset YAML, discrete flags " +
		"(--chain-id, --image, --replicas), --set. " +
		"\n\n" +
		"Cluster + namespace come from --kubeconfig (or $KUBECONFIG, " +
		"or $HOME/.kube/config, or in-cluster) and -n (or the kubeconfig " +
		"context's default-namespace, or the in-cluster ServiceAccount's " +
		"namespace). seictl prints the resolved cluster + namespace on " +
		"stderr before applying. " +
		"\n\n" +
		"Output: post-apply CR on stdout as JSON. Errors on stderr as a " +
		"metav1.Status object (parse with `jq -r .reason`).",
	ArgsUsage: "<name>",
	Arguments: []cli.Argument{
		&cli.StringArg{Name: "name", UsageText: "metadata.name of the SeiNodeDeployment"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "preset",
			Usage:    "Preset name (genesis-chain | rpc)",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:  "chain-id",
			Usage: "Chain ID — sets spec.template.spec.chainId (and spec.genesis.chainId for genesis-chain). Required by all v1 presets.",
		},
		&cli.StringFlag{
			Name:  "image",
			Usage: "seid container image (sets spec.template.spec.image; required by all v1 presets)",
		},
		&cli.IntFlag{
			Name:  "replicas",
			Usage: "Replica count (overrides preset default)",
		},
		&cli.StringSliceFlag{
			Name:  "set",
			Usage: "Strategic-merge override, dotted path with optional list-index suffix (e.g. --set spec.template.spec.image=foo, --set spec.genesis.accounts[0].address=sei1abc). Wins on collision with discrete flags. Repeatable.",
		},
		&cli.StringSliceFlag{
			Name:  "genesis-account",
			Usage: "Append a GenesisAccount to spec.genesis.accounts: --genesis-account <address>:<balance> (e.g. --genesis-account sei1abc...:1000000000usei). Balance accepts the standard cosmos coin format (comma-separated denominations). Repeatable. Requires --preset genesis-chain. --set spec.genesis.accounts[N]... overrides on collision.",
		},
		&cli.StringSliceFlag{
			Name:  "override",
			Usage: "Set a key in spec.template.spec.overrides: --override <toml-path>=<value> (e.g. --override evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber). Keys are dotted TOML paths consumed by the controller's config-apply pipeline; --set cannot reach this map because its parser splits on every dot. Repeatable.",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Validate via server-side-apply dry-run and emit the would-be-applied CR without persisting",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (honors KUBECONFIG colon-merge); defaults to $HOME/.kube/config or in-cluster",
		},
	},
	Action: applyAction,
}
