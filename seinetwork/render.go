package seinetwork

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

type renderArgs struct {
	preset           string
	name             string
	namespace        string
	chainID          string
	image            string
	replicas         int
	hasReps          bool
	cpu              string
	memory           string
	storage          string
	iops             string
	throughput       string
	nodeIsolation    string
	consensusEngine  string
	evmOnly          bool
	sets             []string
	configValues     []string
	genesisAccounts  []string
	genesisOverrides []string
}

// presetRequiredFields surfaces missing required fields as a friendly
// UsageError before SSA, so a typo gets "preset genesis-chain requires
// .spec.genesis.chainId" instead of an apiserver Invalid wall.
var presetRequiredFields = map[string][][]string{
	"genesis-chain": {
		{"spec", "genesis", "chainId"},
		{"spec", "image"},
	},
}

func render(args renderArgs) (*unstructured.Unstructured, error) {
	if args.preset == "" {
		return nil, cliutil.UsageError("--preset is required (known: %s)", strings.Join(presetNames(), ", "))
	}
	if args.name == "" {
		return nil, cliutil.UsageError("name is required: seictl network apply <name> --preset ...")
	}
	for _, q := range []struct{ flag, value string }{
		{"cpu", args.cpu},
		{"memory", args.memory},
		{"storage", args.storage},
	} {
		if err := cliutil.ValidateQuantity(q.flag, q.value); err != nil {
			return nil, err
		}
	}

	// The operator supplies the (IOPS, throughput) pair; the harness
	// resolves it to the class name that encodes it (Spec 001 Req 3.2).
	// Empty means the standard tier, which is the absence of the field.
	vacName, err := cliutil.ResolveStoragePerformance(args.iops, args.throughput)
	if err != nil {
		return nil, err
	}

	data, err := loadPreset(args.preset)
	if err != nil {
		return nil, cliutil.UsageError("%s", err.Error())
	}

	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse preset %q: %w", args.preset, err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(jsonBytes); err != nil {
		return nil, fmt.Errorf("decode preset %q: %w", args.preset, err)
	}
	u.SetGroupVersionKind(kind.GVK)

	if args.image != "" {
		if err := unstructured.SetNestedField(u.Object, args.image, "spec", "image"); err != nil {
			return nil, fmt.Errorf("apply --image: %w", err)
		}
	}
	if args.hasReps {
		if err := unstructured.SetNestedField(u.Object, int64(args.replicas), "spec", "replicas"); err != nil {
			return nil, fmt.Errorf("apply --replicas: %w", err)
		}
	}
	if args.cpu != "" {
		if err := unstructured.SetNestedField(u.Object, args.cpu, "spec", "resources", "requests", "cpu"); err != nil {
			return nil, fmt.Errorf("apply --cpu: %w", err)
		}
	}
	if args.memory != "" {
		if err := unstructured.SetNestedField(u.Object, args.memory, "spec", "resources", "requests", "memory"); err != nil {
			return nil, fmt.Errorf("apply --memory: %w", err)
		}
	}
	if args.storage != "" {
		if err := unstructured.SetNestedField(u.Object, args.storage, "spec", "dataVolume", "storage", "resources", "requests", "storage"); err != nil {
			return nil, fmt.Errorf("apply --storage: %w", err)
		}
	}
	if vacName != "" {
		if err := unstructured.SetNestedField(u.Object, vacName, "spec", "dataVolume", "storage", "volumeAttributesClassName"); err != nil {
			return nil, fmt.Errorf("apply --iops/--throughput: %w", err)
		}
	}
	if err := cliutil.ApplyNodeIsolation(u.Object, args.nodeIsolation); err != nil {
		return nil, err
	}
	if err := cliutil.ApplyConsensus(u.Object, args.consensusEngine, args.evmOnly); err != nil {
		return nil, err
	}
	if args.chainID != "" {
		// SeiNetwork has no top-level chainId — genesis.chainId is the sole
		// chain identity (seinetwork_types.go:41,111). The old SND
		// triple-write (template + label + genesis) collapses to one path.
		if err := unstructured.SetNestedField(u.Object, args.chainID, "spec", "genesis", "chainId"); err != nil {
			return nil, fmt.Errorf("apply --chain-id (genesis): %w", err)
		}
	}

	if len(args.genesisAccounts) > 0 {
		accounts := make([]interface{}, 0, len(args.genesisAccounts))
		for _, entry := range args.genesisAccounts {
			addr, balance, parseErr := cliutil.ParseGenesisAccount(entry)
			if parseErr != nil {
				return nil, cliutil.UsageError("apply --genesis-account %q: %s", entry, parseErr.Error())
			}
			accounts = append(accounts, map[string]interface{}{
				"address": addr,
				"balance": balance,
			})
		}
		if err := unstructured.SetNestedSlice(u.Object, accounts, "spec", "genesis", "accounts"); err != nil {
			return nil, fmt.Errorf("apply --genesis-account: %w", err)
		}
	}

	for _, expr := range args.sets {
		if err := cliutil.ApplySet(u.Object, expr); err != nil {
			return nil, cliutil.UsageError("apply --set %q: %s", expr, err.Error())
		}
	}

	for _, expr := range args.genesisOverrides {
		if err := cliutil.ApplyGenesisOverride(u.Object, expr, "spec", "genesis", "overrides"); err != nil {
			return nil, cliutil.UsageError("apply --genesis-override %q: %s", expr, err.Error())
		}
	}

	// After --set so an entry --set placed in the list is merged by
	// (fileName, key) rather than duplicated.
	if err := cliutil.ApplyConfigValues(u.Object, args.configValues, "spec", "configValues"); err != nil {
		return nil, err
	}

	// Final resource guard — nothing below writes spec.resources, so this
	// sees whatever --set left behind.
	if err := cliutil.RejectCPULimit(u.Object); err != nil {
		return nil, err
	}

	// Same shape, for storage: re-read what landed so --set can neither
	// name a class no supported pair resolves to, nor shrink the volume
	// below the one the selected IOPS is legal on.
	if err := cliutil.ValidateStoragePerformanceSelection(u.Object); err != nil {
		return nil, err
	}

	// Re-read spec.consensus after --set so --evm-only cannot be paired with
	// --set spec.consensus.engine=Tendermint, and a preset-supplied engine
	// satisfies --evm-only.
	if err := cliutil.ValidateConsensus(u.Object); err != nil {
		return nil, err
	}

	// Reassert identity after --set so --set metadata.namespace=kube-system
	// can't silently retarget.
	u.SetName(args.name)
	if args.namespace != "" {
		u.SetNamespace(args.namespace)
	}

	// NOT A TRUST BOUNDARY — anyone with `kubectl edit seinetwork` can forge
	// these. Downstream consumers must not gate behavior on them.
	annotations := u.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["seictl.sei.io/preset"] = args.preset
	annotations["seictl.sei.io/version"] = cliutil.Version
	u.SetAnnotations(annotations)

	for _, fields := range presetRequiredFields[args.preset] {
		val, found, _ := unstructured.NestedString(u.Object, fields...)
		if !found || val == "" {
			return nil, cliutil.UsageError("preset %q requires .%s — set via flag or --set %s=<value>",
				args.preset, strings.Join(fields, "."), strings.Join(fields, "."))
		}
	}

	return u, nil
}
