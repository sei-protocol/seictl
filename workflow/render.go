package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// seiNodePhases is the SeiNodePhase enum (the target node's phase). Used to
// validate --require-phase client-side, so a typo is a crisp usage error rather
// than an apiserver Invalid wall.
var seiNodePhases = []string{"Pending", "Initializing", "Running", "Failed", "Terminating"}

type renderArgs struct {
	preset       string
	name         string
	namespace    string
	target       string
	requirePhase string
	configPatch  string // path to a YAML/JSON config-patch file
	rpcServers   []string
	sets         []string
}

// render builds a SeiNodeTaskWorkflow CR from an embedded preset and the CLI
// arguments. The result is the exact JSON the apiserver receives, so it is the
// object the jsoncontract tests lock against.
func render(args renderArgs) (*unstructured.Unstructured, error) {
	if args.preset == "" {
		return nil, cliutil.UsageError("--preset is required (known: %s)", strings.Join(presetNames(), ", "))
	}
	if args.name == "" {
		return nil, cliutil.UsageError("name is required")
	}
	if args.target == "" {
		return nil, cliutil.UsageError("--target <node> is required: the SeiNode this workflow operates on")
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

	// Target node — the single SeiNode this workflow acts on. Locked field name
	// spec.target.nodeRef.name (SeiNodeTaskTarget, shared with SeiNodeTask).
	if err := unstructured.SetNestedField(u.Object, args.target, "spec", "target", "nodeRef", "name"); err != nil {
		return nil, fmt.Errorf("apply --target: %w", err)
	}
	if args.requirePhase != "" {
		if !slices.Contains(seiNodePhases, args.requirePhase) {
			return nil, cliutil.UsageError("--require-phase %q invalid; legal SeiNode phases: %s",
				args.requirePhase, strings.Join(seiNodePhases, ", "))
		}
		if err := unstructured.SetNestedField(u.Object, args.requirePhase, "spec", "target", "requirePhase"); err != nil {
			return nil, fmt.Errorf("apply --require-phase: %w", err)
		}
	}

	// StateSync recipe parameters. configPatch is the config-patch task's
	// file -> section-or-key -> value wire shape; rpcServers overrides witness
	// resolution (the controller fails the plan closed on fewer than two).
	if args.configPatch != "" {
		patch, err := loadConfigPatch(args.configPatch)
		if err != nil {
			return nil, cliutil.UsageError("%s", err.Error())
		}
		if err := unstructured.SetNestedField(u.Object, patch, "spec", "stateSync", "configPatch"); err != nil {
			return nil, fmt.Errorf("apply --config-patch: %w", err)
		}
	}
	if len(args.rpcServers) > 0 {
		servers := make([]interface{}, len(args.rpcServers))
		for i, s := range args.rpcServers {
			servers[i] = s
		}
		if err := unstructured.SetNestedSlice(u.Object, servers, "spec", "stateSync", "rpcServers"); err != nil {
			return nil, fmt.Errorf("apply --rpc-servers: %w", err)
		}
	}

	for _, expr := range args.sets {
		if err := cliutil.ApplySet(u.Object, expr); err != nil {
			return nil, cliutil.UsageError("apply --set %q: %s", expr, err.Error())
		}
	}

	// Reassert identity after --set so a --set metadata.namespace=... cannot
	// silently retarget the object.
	u.SetName(args.name)
	if args.namespace != "" {
		u.SetNamespace(args.namespace)
	}

	// NOT A TRUST BOUNDARY — anyone with `kubectl edit` can forge these.
	annotations := u.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["seictl.sei.io/preset"] = args.preset
	annotations["seictl.sei.io/version"] = cliutil.Version
	u.SetAnnotations(annotations)

	// A workflow with no recipe kind can never be adopted — fail before SSA.
	if recipe, _, _ := unstructured.NestedString(u.Object, "spec", "kind"); recipe == "" {
		return nil, cliutil.UsageError("preset %q produced no spec.kind", args.preset)
	}

	return u, nil
}

// loadConfigPatch reads a YAML/JSON config-patch file into the
// file -> section-or-key -> value shape spec.stateSync.configPatch expects
// (the config-patch task's wire form), e.g.:
//
//	app.toml:
//	  state-store:
//	    evm-ss-split: true
func loadConfigPatch(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --config-patch file %q: %w", path, err)
	}
	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse --config-patch file %q: %w", path, err)
	}
	var patch map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &patch); err != nil {
		return nil, fmt.Errorf("decode --config-patch file %q (want a file->section->value map): %w", path, err)
	}
	if len(patch) == 0 {
		return nil, fmt.Errorf("--config-patch file %q is empty", path)
	}
	// Each top-level file key must map to a section table (file -> section ->
	// value); a scalar here is a malformed patch the sidecar would reject.
	for file, section := range patch {
		if _, ok := section.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("--config-patch file %q: %q must map to a section table (file -> section -> value), got %T",
				path, file, section)
		}
	}
	return patch, nil
}
