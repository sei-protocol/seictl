package nodedeployment

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/sei-protocol/seictl/internal/snd"
)

// renderArgs feeds a preset render. Discrete fields beat preset
// defaults; --set entries beat discrete fields.
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

// render loads the named preset, applies discrete-flag overrides, then
// applies --set overrides, and returns the resulting unstructured SND
// with metadata.name / metadata.namespace and provenance annotations
// populated.
func render(args renderArgs) (*unstructured.Unstructured, error) {
	if args.preset == "" {
		return nil, fmt.Errorf("--preset is required (known: %s)", strings.Join(presetNames(), ", "))
	}
	if args.name == "" {
		return nil, fmt.Errorf("--name is required")
	}

	data, err := loadPreset(args.preset)
	if err != nil {
		return nil, err
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
	u.SetName(args.name)
	if args.namespace != "" {
		u.SetNamespace(args.namespace)
	}

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
		if err := unstructured.SetNestedField(u.Object, args.chainID, "spec", "genesis", "chainId"); err != nil {
			return nil, fmt.Errorf("apply --chain-id: %w", err)
		}
	}

	for _, expr := range args.sets {
		if err := applySet(u.Object, expr); err != nil {
			return nil, fmt.Errorf("apply --set %q: %w", expr, err)
		}
	}

	annotations := u.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["seictl.sei.io/preset"] = args.preset
	annotations["seictl.sei.io/version"] = version
	u.SetAnnotations(annotations)

	return u, nil
}

// applySet parses a single --set expression and writes it into obj.
//
// Supported forms:
//
//	a.b.c=value           -> obj.a.b.c = value
//	a.b=true|false        -> typed bool
//	a.b=42                -> typed int64
//	a.b=                  -> empty string
//
// List-index syntax (a.b[0].c=value) is intentionally not supported in
// v1 — the embedded presets don't need it, and unstructured.SetNested*
// helpers cover map paths cleanly. Add when a real consumer asks.
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
		return fmt.Errorf("list-index syntax in --set paths is not supported (path: %q)", rawPath)
	}
	fields := strings.Split(rawPath, ".")
	for _, f := range fields {
		if f == "" {
			return fmt.Errorf("empty segment in path %q", rawPath)
		}
	}
	return unstructured.SetNestedField(root, parseValue(rawVal), fields...)
}

// parseValue converts a raw RHS string into a Go value the unstructured
// layer accepts (DeepCopyJSONValue: bool, int64, float64, string,
// []interface{}, map[string]interface{}).
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
