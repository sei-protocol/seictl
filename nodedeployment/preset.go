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
	preset          string
	name            string
	namespace       string
	chainID         string
	image           string
	replicas        int
	hasReps         bool
	sets            []string
	genesisAccounts []string
	overrides       []string
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

	if len(args.genesisAccounts) > 0 {
		if args.preset != "genesis-chain" {
			return nil, usageError("--genesis-account is only valid with --preset genesis-chain")
		}
		accounts := make([]interface{}, 0, len(args.genesisAccounts))
		for _, entry := range args.genesisAccounts {
			addr, balance, parseErr := parseGenesisAccount(entry)
			if parseErr != nil {
				return nil, usageError("apply --genesis-account %q: %s", entry, parseErr.Error())
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
		if err := applySet(u.Object, expr); err != nil {
			return nil, usageError("apply --set %q: %s", expr, err.Error())
		}
	}

	for _, expr := range args.overrides {
		if err := applyOverride(u.Object, expr); err != nil {
			return nil, usageError("apply --override %q: %s", expr, err.Error())
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

// applyOverride writes a single key=value pair into
// spec.template.spec.overrides. Keys may contain dots because the
// overrides map's keys are themselves dotted TOML paths.
func applyOverride(root map[string]interface{}, expr string) error {
	eq := strings.Index(expr, "=")
	if eq < 0 {
		return fmt.Errorf("missing '=' in --override expression")
	}
	key := expr[:eq]
	val := expr[eq+1:]
	if key == "" {
		return fmt.Errorf("empty key before '='")
	}
	existing, _, err := unstructured.NestedStringMap(root, "spec", "template", "spec", "overrides")
	if err != nil {
		return fmt.Errorf("read existing overrides: %w", err)
	}
	if existing == nil {
		existing = map[string]string{}
	}
	existing[key] = val
	return unstructured.SetNestedStringMap(root, existing, "spec", "template", "spec", "overrides")
}

// applySet writes a single dotted-path --set expression. Each segment is a
// map key, optionally suffixed with `[N]` to step into a list at index N
// after the key. Empty intermediate maps and lists are created on demand.
//
// List-index rules:
//   - idx == len(list) appends a new element (extends the list by one).
//   - idx <  len(list) sets in place on the existing element.
//   - idx >  len(list) errors; sparse indices are not supported.
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
	// `--set foo=null` would silently set the literal string "null"; reject
	// loudly so the user sees the gap. Field clearing is not in scope.
	if rawVal == "null" {
		return fmt.Errorf("value 'null' is not supported")
	}
	segments, err := parsePathSegments(rawPath)
	if err != nil {
		return err
	}
	return writeMap(root, segments, parseValue(rawVal))
}

// pathSegment names one step in a --set path. Every segment has a `key`
// (the map field). If isList is set, the step also indexes into a list
// at the value of that key (so `accounts[0]` is one segment with
// key="accounts", isList=true, listIdx=0).
type pathSegment struct {
	key     string
	isList  bool
	listIdx int
}

// parsePathSegments splits a dotted path into segments, recognizing an
// optional `[N]` suffix on each segment as a list index.
func parsePathSegments(path string) ([]pathSegment, error) {
	fields := strings.Split(path, ".")
	out := make([]pathSegment, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			return nil, fmt.Errorf("empty segment in path %q", path)
		}
		bracket := strings.Index(f, "[")
		if bracket < 0 {
			out = append(out, pathSegment{key: f})
			continue
		}
		if !strings.HasSuffix(f, "]") {
			return nil, fmt.Errorf("malformed segment %q in path %q: '[' without closing ']'", f, path)
		}
		if bracket == 0 {
			return nil, fmt.Errorf("malformed segment %q in path %q: '[N]' without preceding key", f, path)
		}
		key := f[:bracket]
		idxStr := f[bracket+1 : len(f)-1]
		if idxStr == "" {
			return nil, fmt.Errorf("empty list index in segment %q", f)
		}
		if strings.ContainsAny(idxStr, "[]") {
			return nil, fmt.Errorf("nested list-index syntax not supported in segment %q", f)
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			return nil, fmt.Errorf("malformed list index in segment %q: %w", f, err)
		}
		if idx < 0 {
			return nil, fmt.Errorf("negative list index in segment %q", f)
		}
		out = append(out, pathSegment{key: key, isList: true, listIdx: idx})
	}
	return out, nil
}

// writeMap descends one map step (segments[0].key), recursing or setting.
func writeMap(m map[string]interface{}, segments []pathSegment, value interface{}) error {
	if len(segments) == 0 {
		return fmt.Errorf("internal: writeMap called with empty segments")
	}
	seg := segments[0]
	rest := segments[1:]

	if seg.isList {
		var list []interface{}
		if existing, ok := m[seg.key]; ok && existing != nil {
			l, isList := existing.([]interface{})
			if !isList {
				return fmt.Errorf("path expects list at key %q but found %T", seg.key, existing)
			}
			list = l
		}
		newList, err := writeList(list, seg.listIdx, rest, value)
		if err != nil {
			return fmt.Errorf("at .%s[%d]: %w", seg.key, seg.listIdx, err)
		}
		m[seg.key] = newList
		return nil
	}

	if len(rest) == 0 {
		m[seg.key] = value
		return nil
	}

	existing, ok := m[seg.key]
	if !ok || existing == nil {
		inner := map[string]interface{}{}
		m[seg.key] = inner
		return writeMap(inner, rest, value)
	}
	inner, isMap := existing.(map[string]interface{})
	if !isMap {
		return fmt.Errorf("path expects map at key %q but found %T", seg.key, existing)
	}
	return writeMap(inner, rest, value)
}

// writeList sets or appends at index, recursing into the element if needed.
func writeList(list []interface{}, idx int, rest []pathSegment, value interface{}) ([]interface{}, error) {
	if idx > len(list) {
		return nil, fmt.Errorf("list index %d out of range (length %d); sparse indices are not supported", idx, len(list))
	}
	if idx == len(list) {
		if len(rest) == 0 {
			return append(list, value), nil
		}
		inner := map[string]interface{}{}
		list = append(list, inner)
		if err := writeMap(inner, rest, value); err != nil {
			return nil, err
		}
		return list, nil
	}
	if len(rest) == 0 {
		list[idx] = value
		return list, nil
	}
	inner, isMap := list[idx].(map[string]interface{})
	if !isMap {
		return nil, fmt.Errorf("path expects map at index [%d] but found %T", idx, list[idx])
	}
	return list, writeMap(inner, rest, value)
}

// parseGenesisAccount parses a `<address>:<balance>` --genesis-account entry.
// The balance side accepts the standard cosmos coin format (one or more
// `<int><denom>` separated by commas — e.g. `1000usei,500uatom`).
func parseGenesisAccount(entry string) (string, string, error) {
	idx := strings.Index(entry, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("missing ':' between address and balance")
	}
	addr := strings.TrimSpace(entry[:idx])
	balance := strings.TrimSpace(entry[idx+1:])
	if addr == "" {
		return "", "", fmt.Errorf("empty address before ':'")
	}
	if balance == "" {
		return "", "", fmt.Errorf("empty balance after ':'")
	}
	return addr, balance, nil
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
