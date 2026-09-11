package seinetwork

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

func applyAction(ctx context.Context, c *cli.Command) error {
	name := c.StringArg("name")
	if name == "" {
		cliutil.EmitStatus(os.Stderr, cliutil.UsageError("name argument required: seictl network apply <name> --preset ..."))
		return cli.Exit("", 1)
	}

	args := renderArgs{
		preset:           c.String("preset"),
		name:             name,
		namespace:        c.String("namespace"),
		chainID:          c.String("chain-id"),
		image:            c.String("image"),
		cpu:              c.String("cpu"),
		memory:           c.String("memory"),
		storage:          c.String("storage"),
		iops:             c.String("iops"),
		throughput:       c.String("throughput"),
		nodeIsolation:    c.String("node-isolation"),
		sets:             c.StringSlice("set"),
		configValues:     c.StringSlice("config-value"),
		genesisAccounts:  c.StringSlice("genesis-account"),
		genesisOverrides: c.StringSlice("genesis-override"),
	}
	if c.IsSet("replicas") {
		args.replicas = int(c.Int("replicas"))
		args.hasReps = true
	}
	dryRun := c.Bool("dry-run")

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
	fmt.Fprintf(os.Stderr, "seictl: %s SeiNetwork %s/%s to %s\n",
		mode, obj.GetNamespace(), obj.GetName(), cfg.Host)

	kcli, err := cliutil.NewClient(cfg)
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	if err := kind.Apply(ctx, kcli, obj, dryRun); err != nil {
		cliutil.EmitStatus(os.Stderr, fmt.Errorf("apply SeiNetwork %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
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
	Name: "apply",
	// urfave/cli's StringSliceFlag splits values on `,` by default,
	// mangling multi-denom coins and TOML list values.
	DisableSliceFlagSeparator: true,
	Usage:                     "Render a preset and server-side-apply the resulting SeiNetwork",
	Description: "Loads the named preset, applies discrete-flag and --set " +
		"overrides, and server-side-applies the result. With --dry-run, " +
		"the apiserver validates and returns the would-be-applied CR " +
		"without persisting. " +
		"\n\n" +
		"IMMUTABILITY: spec.genesis (incl. --chain-id, --genesis-account, " +
		"--genesis-override) and spec.replicas are admission-immutable. " +
		"Re-applying with a changed --chain-id or --replicas is REJECTED " +
		"(metav1.Status Invalid, exit 1), not a silent no-op — genesis and " +
		"validator-set size are network identity. To change them, delete " +
		"and re-create. " +
		"\n\n" +
		"Layering, lowest precedence first: preset YAML, discrete flags " +
		"(--chain-id, --image, --replicas, --cpu, --memory, --storage, " +
		"--iops, --throughput, --node-isolation), --set, then --config-value (merged into " +
		"spec.configValues by (fileName, key), so it never duplicates an " +
		"entry --set or the preset placed there). " +
		"\n\n" +
		"--config-value sets a typed TOML key on EVERY validator (spec 002/003). " +
		"Editing configValues on a live SeiNetwork restarts the whole " +
		"validator pool at once; block production stops until more than 2/3 " +
		"are back. Prefer editing a follower SeiNode for a running chain. " +
		"Values are unvalidated by the controller beyond shape: a bad key " +
		"surfaces as ConfigValuesValid=False on the CR, not at apply. " +
		"The genesis-chain preset also carries spec.configOverrides (raw " +
		"TOML merge-patch, the legacy surface); setting the same key in both " +
		"is not detected here and the controller decides precedence, so " +
		"keep a key in one place. " +
		"\n\n" +
		"--cpu/--memory/--storage each override one dimension of the " +
		"preset's resource footprint; unspecified dimensions keep the " +
		"preset default (4 CPU / 32Gi / 500Gi). Values must be valid " +
		"Kubernetes quantities (32Gi, not 32GB) — seictl rejects a bad " +
		"one locally rather than letting Flux discover it. Neither the " +
		"presets nor these flags emit limits — the controller derives " +
		"the memory limit from the request. A CPU limit is rejected " +
		"outright (the CRD forbids one); a memory limit is reachable " +
		"only via --set, and the CRD requires it to equal the memory " +
		"request. " +
		"\n\n" +
		"--iops/--throughput select storage PERFORMANCE as a pair: you " +
		"supply the parameters, seictl resolves them to the " +
		"VolumeAttributesClass that encodes them, and that name is what " +
		"lands on the CR. Omit both for the standard tier — the PVC then " +
		"carries no volumeAttributesClassName and the gp3 StorageClass " +
		"defaults apply. An unsupported pair is refused locally, naming " +
		"the supported set. The 10000-IOPS offering also needs a data " +
		"volume that provisions at least 20 GiB: EBS gp3 caps IOPS at " +
		"500 x GiB. EBS rounds a request up to whole GiB, so 19.5Gi " +
		"qualifies and 19Gi does not. The apiserver cannot see that " +
		"rule, and a violation fails at provision time with the pod " +
		"Pending. " +
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
		&cli.StringArg{Name: "name", UsageText: "metadata.name of the SeiNetwork"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "preset",
			Usage:    "Preset name (genesis-chain)",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:  "chain-id",
			Usage: "Chain ID — sets spec.genesis.chainId. Required. Admission-immutable after create.",
		},
		&cli.StringFlag{
			Name:  "image",
			Usage: "seid container image (sets spec.image; required by all v1 presets)",
		},
		&cli.IntFlag{
			Name:  "replicas",
			Usage: "Genesis validator count (overrides preset default 4). Admission-immutable after create — minted into genesis state.",
		},
		&cli.StringFlag{
			Name:  "cpu",
			Usage: "CPU request for seid container (overrides preset default; e.g. 4, 8, 16). Create-only on the CRD.",
		},
		&cli.StringFlag{
			Name:  "memory",
			Usage: "Memory request for seid container (overrides preset default; e.g. 32Gi, 128Gi). Create-only on the CRD.",
		},
		&cli.StringFlag{
			Name:  "storage",
			Usage: "Data volume storage size (overrides preset default; e.g. 500Gi, 2000Gi). Create-only on the CRD.",
		},
		&cli.StringFlag{
			Name:  "iops",
			Usage: "Provisioned IOPS of the data volume, as a count. Pass together with --throughput: the pair selects a supported storage performance offering, which seictl resolves to the VolumeAttributesClass that encodes it. Omit both for the standard tier (the gp3 StorageClass defaults). An unsupported pair is refused locally, naming the supported set. Create-only on the CRD.",
		},
		&cli.StringFlag{
			Name:  "throughput",
			Usage: "Throughput of the data volume in MiB/s. Pass together with --iops (see --iops). Create-only on the CRD.",
		},
		&cli.StringFlag{
			Name:  "node-isolation",
			Usage: "Worker-node placement for every validator: Shared (validators may co-locate with other pods) or Dedicated (one validator per single-tenant worker node, for benchmarks that must not share CPU/disk with a neighbour). Sets spec.scheduling.nodeIsolation; when omitted the field is left unset and the controller resolves it (legacy isolation annotation first, else its default). Repeat the flag on EVERY re-apply: seictl server-side-applies with force ownership, so an apply that omits it (e.g. one that only bumps --image) removes the field and a Dedicated network falls back to the controller default. Dedicated needs free single-tenant capacity: a child that finds none sits at status.nodes[].placement=Pending until a node is provisioned, and `network get` shows the pod Pending. Confirm placement with `seictl network get <name> -o json | jq '.status.nodes[] | {name, placement, workerNode}'`.",
		},
		&cli.StringSliceFlag{
			Name:  "set",
			Usage: "Strategic-merge override, dotted path with optional list-index suffix (e.g. --set spec.image=foo, --set spec.configOverrides.evm.http_port=8545). Wins on collision with discrete flags. Repeatable.",
		},
		&cli.StringSliceFlag{
			Name:  "config-value",
			Usage: `Append a typed entry to spec.configValues: --config-value <file>.toml:<dotted.key>=<value> (e.g. --config-value config.toml:evm-only=true, --config-value app.toml:giga_executor.enabled=true). The file must be a TOML file name (^[A-Za-z0-9_-]+\.toml$); the key a dotted TOML path. Values parse as JSON when possible (bool, number, array, table); otherwise as strings — wrap in JSON quotes to force a string ("400ms"). null is refused. Same (file, key) as an existing entry replaces it. At most 100 entries. Repeatable. Prefer this over --set spec.configValues=[...], which replaces the whole list.`,
		},
		&cli.StringSliceFlag{
			Name:  "genesis-account",
			Usage: "Append a GenesisAccount to spec.genesis.accounts: --genesis-account <address>:<balance> (e.g. --genesis-account sei1abc...:1000000000usei). Balance accepts the standard cosmos coin format (comma-separated denominations). Repeatable. --set spec.genesis.accounts[N]... overrides on collision.",
		},
		&cli.StringSliceFlag{
			Name:  "genesis-override",
			Usage: `Set a key in spec.genesis.overrides: --genesis-override <module.field[.field...]>=<value> (e.g. --genesis-override staking.params.unbonding_time=600s). Keys must be dotted cosmos-module paths — the first segment is a module in app_state (staking, bank, gov, ...). Values parse as JSON when possible (numbers, bools, objects); otherwise as strings. To force a numeric-looking value to render as string, wrap in JSON quotes (e.g. --genesis-override foo.bar='"42"'). Repeatable. --set cannot reach this map because its parser splits on every dot.`,
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
