package nodedeployment

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/sei-protocol/seictl/internal/snd"
)

type renderArgs struct {
	preset    string
	name      string
	namespace string
	chainID   string
	image     string
	replicas  int
	hasReps   bool
	sets      []string
}

// presetRequiredFields surfaces missing required fields as a friendly
// usageError before SSA, so a typo gets "preset rpc requires
// .spec.template.spec.chainId" instead of an apiserver Invalid wall.
var presetRequiredFields = map[string][][]string{
	"genesis-chain": {
		{"spec", "genesis", "chainId"},
		{"spec", "template", "spec", "chainId"},
		{"spec", "template", "spec", "image"},
	},
	"rpc": {
		{"spec", "template", "spec", "chainId"},
		{"spec", "template", "spec", "image"},
	},
}

// presetRoles is the sei.io/role value stamped onto each preset's
// template.metadata.labels. Convention from prod manifests:
// validators carry "validator", general fullnodes carry "node".
var presetRoles = map[string]string{
	"genesis-chain": "validator",
	"rpc":           "node",
}

func render(args renderArgs) (*unstructured.Unstructured, error) {
	if args.preset == "" {
		return nil, usageError("--preset is required (known: %s)", strings.Join(presetNames(), ", "))
	}
	if args.name == "" {
		return nil, usageError("name is required: seictl nd apply <name> --preset ...")
	}

	data, err := loadPreset(args.preset)
	if err != nil {
		return nil, usageError("%s", err.Error())
	}

	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse preset %q: %w", args.preset, err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(jsonBytes); err != nil {
		return nil, fmt.Errorf("decode preset %q: %w", args.preset, err)
	}
	u.SetGroupVersionKind(snd.GVK)

	if args.image != "" {
		if err := unstructured.SetNestedField(u.Object, args.image, "spec", "template", "spec", "image"); err != nil {
			return nil, fmt.Errorf("apply --image: %w", err)
		}
	}
	if args.hasReps {
		if err := unstructured.SetNestedField(u.Object, int64(args.replicas), "spec", "replicas"); err != nil {
			return nil, fmt.Errorf("apply --replicas: %w", err)
		}
	}
	if args.chainID != "" {
		if err := unstructured.SetNestedField(u.Object, args.chainID, "spec", "template", "spec", "chainId"); err != nil {
			return nil, fmt.Errorf("apply --chain-id (template): %w", err)
		}
		if err := unstructured.SetNestedField(u.Object, args.chainID, "spec", "template", "metadata", "labels", "sei.io/chain"); err != nil {
			return nil, fmt.Errorf("apply --chain-id (template label): %w", err)
		}
		if role, ok := presetRoles[args.preset]; ok {
			if err := unstructured.SetNestedField(u.Object, role, "spec", "template", "metadata", "labels", "sei.io/role"); err != nil {
				return nil, fmt.Errorf("apply preset role label: %w", err)
			}
		}
		if args.preset == "genesis-chain" {
			if err := unstructured.SetNestedField(u.Object, args.chainID, "spec", "genesis", "chainId"); err != nil {
				return nil, fmt.Errorf("apply --chain-id (genesis): %w", err)
			}
		}
		if args.preset == "rpc" {
			peers := []interface{}{
				map[string]interface{}{
					"label": map[string]interface{}{
						"selector": map[string]interface{}{
							"sei.io/chain": args.chainID,
						},
					},
				},
			}
			if err := unstructured.SetNestedSlice(u.Object, peers, "spec", "template", "spec", "peers"); err != nil {
				return nil, fmt.Errorf("apply rpc peer source: %w", err)
			}
		}
	}

	for _, expr := range args.sets {
		if err := applySet(u.Object, expr); err != nil {
			return nil, usageError("apply --set %q: %s", expr, err.Error())
		}
	}

	// Reassert identity after --set so --set metadata.namespace=kube-system
	// can't silently retarget.
	u.SetName(args.name)
	if args.namespace != "" {
		u.SetNamespace(args.namespace)
	}

	// NOT A TRUST BOUNDARY — anyone with `kubectl edit snd` can forge
	// these. Downstream consumers must not gate behavior on them.
	annotations := u.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["seictl.sei.io/preset"] = args.preset
	annotations["seictl.sei.io/version"] = version
	u.SetAnnotations(annotations)

	for _, fields := range presetRequiredFields[args.preset] {
		val, found, _ := unstructured.NestedString(u.Object, fields...)
		if !found || val == "" {
			return nil, usageError("preset %q requires .%s — set via flag or --set %s=<value>",
				args.preset, strings.Join(fields, "."), strings.Join(fields, "."))
		}
	}

	return u, nil
}

// applySet writes a single dotted-path --set expression. List-index
// syntax is intentionally unsupported; add when a real consumer asks.
func applySet(root map[string]interface{}, expr string) error {
	eq := strings.Index(expr, "=")
	if eq < 0 {
		return fmt.Errorf("missing '=' in --set expression")
	}
	rawPath := expr[:eq]
	rawVal := expr[eq+1:]
	if rawPath == "" {
		return fmt.Errorf("empty path before '='")
	}
	if strings.ContainsAny(rawPath, "[]") {
		return fmt.Errorf("list-index syntax in --set paths is not supported (path: %q); rewrite as a top-level field or file an issue with the case", rawPath)
	}
	fields := strings.Split(rawPath, ".")
	for _, f := range fields {
		if f == "" {
			return fmt.Errorf("empty segment in path %q", rawPath)
		}
	}
	return unstructured.SetNestedField(root, parseValue(rawVal), fields...)
}

func parseValue(raw string) interface{} {
	switch raw {
	case "":
		return ""
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	return raw
}
