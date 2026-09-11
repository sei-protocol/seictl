package seinode

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
		cliutil.EmitStatus(os.Stderr, cliutil.UsageError("name argument required: seictl node apply <name> --preset ..."))
		return cli.Exit("", 1)
	}

	args := renderArgs{
		preset:          c.String("preset"),
		name:            name,
		namespace:       c.String("namespace"),
		chainID:         c.String("chain-id"),
		image:           c.String("image"),
		network:         c.String("network"),
		externalAddress: c.String("external-address"),
		cpu:             c.String("cpu"),
		memory:          c.String("memory"),
		storage:         c.String("storage"),
		iops:            c.String("iops"),
		throughput:      c.String("throughput"),
		nodeIsolation:   c.String("node-isolation"),
		consensusEngine: c.String("consensus-engine"),
		evmOnly:         c.Bool("evm-only"),
		sets:            c.StringSlice("set"),
		configValues:    c.StringSlice("config-value"),
		overrides:       c.StringSlice("override"),
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
	fmt.Fprintf(os.Stderr, "seictl: %s SeiNode %s/%s to %s\n",
		mode, obj.GetNamespace(), obj.GetName(), cfg.Host)

	kcli, err := cliutil.NewClient(cfg)
	if err != nil {
		cliutil.EmitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	if err := kind.Apply(ctx, kcli, obj, dryRun); err != nil {
		cliutil.EmitStatus(os.Stderr, fmt.Errorf("apply SeiNode %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
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
	Usage:                     "Render a preset and server-side-apply the resulting SeiNode",
	Description: "Loads the named preset, applies discrete-flag and --set " +
		"overrides, and server-side-applies the result. With --dry-run, " +
		"the apiserver validates and returns the would-be-applied CR " +
		"without persisting. " +
		"\n\n" +
		"A SeiNode is a SINGLE node — there is no spec.replicas. An RPC " +
		"fleet of N is N distinct `node apply` invocations. " +
		"\n\n" +
		"--network <X> auto-wires spec.peers[].label.selector to " +
		"sei.io/seinetwork=<X> (peer with that network's validators) and " +
		"stamps the metadata.labels (sei.io/seinetwork=<X>, sei.io/role=node) " +
		"that `node list -l` selects on. " +
		"\n\n" +
		"Layering, lowest precedence first: preset YAML, discrete flags " +
		"(--chain-id, --image, --network, --external-address, --cpu, " +
		"--memory, --storage, --iops, --throughput, --node-isolation, --consensus-engine, --evm-only), --override, --set, " +
		"then --config-value (merged into spec.configValues by (fileName, key)). " +
		"\n\n" +
		"--override writes the allow-listed spec.overrides map (validated " +
		"keys, string values). --config-value writes spec.configValues " +
		"(spec 002): any TOML file, typed JSON values, no allow-list — the " +
		"controller validates shape only and reports a bad key as " +
		"ConfigValuesValid=False. Changing configValues on a Running node " +
		"restarts seid. " +
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
		&cli.StringArg{Name: "name", UsageText: "metadata.name of the SeiNode"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "preset",
			Usage:    "Preset name (rpc)",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:  "chain-id",
			Usage: "Chain ID — sets spec.chainId. Required by all v1 presets.",
		},
		&cli.StringFlag{
			Name:  "image",
			Usage: "seid container image (sets spec.image; required by all v1 presets)",
		},
		&cli.StringFlag{
			Name:  "network",
			Usage: "SeiNetwork to peer with — derives spec.peers[].label.selector (sei.io/seinetwork=<X>) and stamps the metadata.labels `node list -l` matches on. Required for a peering full node unless --set spec.peers... is given.",
		},
		&cli.StringFlag{
			Name:  "external-address",
			Usage: "Routable P2P host:port written to spec.externalAddress. Leave unset for in-cluster nodes (headless DNS is self-configuring); set for cross-cluster/sentry peers.",
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
			Usage: "Worker-node placement: Shared (may co-locate with other pods) or Dedicated (single-tenant worker node, for a benchmark follower that must not share CPU/disk with a neighbour). Sets spec.scheduling.nodeIsolation; when omitted the field is left unset and the controller resolves it (legacy isolation annotation first, else its default). Repeat the flag on EVERY re-apply: seictl server-side-applies with force ownership, so an apply that omits it (e.g. one that only bumps --image) removes the field and a Dedicated node falls back to the controller default. Dedicated needs free single-tenant capacity: with none the pod sits Pending until a node is provisioned. Confirm with `seictl node get <name> -o json | jq .status.currentNodeIsolation`.",
		},
		&cli.StringFlag{
			Name:  "consensus-engine",
			Usage: "Consensus engine this node runs: Tendermint (default when omitted) or Autobahn. Sets spec.consensus.engine. Create-only on its effective value: the engine is baked into the ceremony's autobahn.json and this node's home directory, so changing it means replacing the node. Repeat the flag on EVERY re-apply: seictl server-side-applies with force ownership, so an apply that omits it (e.g. one that only bumps --image) drops spec.consensus from the applied configuration — on an Autobahn node the apiserver then rejects the apply as a create-only change (re-run with the flag), never silently reverting the engine. A follower of an Autobahn chain must set this so configure-genesis fetches the chain's autobahn.json beside genesis.json and the controller sets config.toml autobahn-config-file itself — do not set that key via --config-value (refused at plan build). Pair with --config-value for the rest of the Autobahn/Giga tuning (giga_executor, state-store, 400ms block interval).",
		},
		&cli.BoolFlag{
			Name:  "evm-only",
			Usage: "Run the EVM-only executor (requires --consensus-engine Autobahn). Sets spec.consensus.evmOnly=true. Create-only. Repeat it on EVERY re-apply alongside --consensus-engine, for the same force-ownership reason. The controller closes the CometBFT RPC (26657), REST and gRPC listeners, probes readiness on GET / :8545 instead of /lag_status, and attaches no cosmos-exporter — do not set evm-only, rpc.laddr, api.enable, grpc.enable or grpc-web.enable via --config-value. Refused on a seed. Empty blocks are off by default under Autobahn, so height stays 0 until load arrives.",
		},
		&cli.StringSliceFlag{
			Name:  "set",
			Usage: "Strategic-merge override, dotted path with optional list-index suffix (e.g. --set spec.image=foo, --set spec.peers[0].label.namespace=other-ns). Wins on collision with discrete flags. Repeatable.",
		},
		&cli.StringSliceFlag{
			Name:  "config-value",
			Usage: `Append a typed entry to spec.configValues: --config-value <file>.toml:<dotted.key>=<value> (e.g. --config-value config.toml:evm-only=true, --config-value app.toml:giga_executor.enabled=true). The file must be a TOML file name (^[A-Za-z0-9_-]+\.toml$); the key a dotted TOML path. Values parse as JSON when possible (bool, number, array, table); otherwise as strings — wrap in JSON quotes to force a string ("400ms"). null is refused. Same (file, key) as an existing entry replaces it. At most 100 entries. Repeatable. Prefer this over --set spec.configValues=[...], which replaces the whole list.`,
		},
		&cli.StringSliceFlag{
			Name:  "override",
			Usage: "Set a key in spec.overrides: --override <toml-path>=<value> (e.g. --override evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber). Keys are dotted TOML paths consumed by the controller's config-apply pipeline; --set cannot reach this map because its parser splits on every dot. Repeatable.",
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
