package cliutil

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// MaxConfigValues mirrors the CRD's MaxItems on spec.configValues.
const MaxConfigValues = 100

var (
	configValueFileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.toml$`)
	configValueKeyPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
)

// ParseConfigValue parses a `<file>.toml:<dotted.key>=<value>` expression
// into a spec.configValues entry. The value parses as JSON when it parses
// (number, bool, array, table, JSON-quoted string); otherwise it is stored
// as a string. The CRD constraints on fileName and key are checked here so
// a typo fails at render time rather than at Flux apply.
func ParseConfigValue(expr string) (map[string]interface{}, error) {
	colon := strings.Index(expr, ":")
	if colon < 0 {
		return nil, fmt.Errorf("missing ':' — expected <file>.toml:<dotted.key>=<value>")
	}
	fileName := expr[:colon]
	rest := expr[colon+1:]
	eq := strings.Index(rest, "=")
	if eq < 0 {
		return nil, fmt.Errorf("missing '=' — expected <file>.toml:<dotted.key>=<value>")
	}
	key := rest[:eq]
	val := rest[eq+1:]

	if !configValueFileNamePattern.MatchString(fileName) || len(fileName) > 64 {
		return nil, fmt.Errorf("fileName %q must match %s (at most 64 chars); only TOML files are accepted", fileName, configValueFileNamePattern.String())
	}
	if !configValueKeyPattern.MatchString(key) || len(key) > 256 {
		return nil, fmt.Errorf("key %q must be a dotted TOML path matching %s (at most 256 chars)", key, configValueKeyPattern.String())
	}
	if val == "" {
		return nil, fmt.Errorf("empty value for %s:%s — the CRD requires a value; to set an empty string pass '\"\"'", fileName, key)
	}

	var parsed interface{}
	if jsonErr := json.Unmarshal([]byte(val), &parsed); jsonErr != nil {
		parsed = val
	}
	if parsed == nil {
		return nil, fmt.Errorf("value null for %s:%s is rejected by the CRD; remove the entry instead of nulling it", fileName, key)
	}
	if containsNull(parsed) {
		return nil, fmt.Errorf("value for %s:%s contains a nested null, which has no TOML representation and fails at plan-build", fileName, key)
	}
	return map[string]interface{}{
		"fileName": fileName,
		"key":      key,
		"value":    parsed,
	}, nil
}

func containsNull(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []interface{}:
		for _, e := range t {
			if containsNull(e) {
				return true
			}
		}
	case map[string]interface{}:
		for _, e := range t {
			if containsNull(e) {
				return true
			}
		}
	}
	return false
}

// ApplyConfigValues merges parsed --config-value entries into the list at
// fieldPath (spec.configValues). An entry whose (fileName, key) already
// exists — from the preset or from --set — is replaced in place so the flag
// never duplicates a key the controller would then reject; new entries
// append in flag order.
func ApplyConfigValues(root map[string]interface{}, exprs []string, fieldPath ...string) error {
	if len(exprs) == 0 {
		return nil
	}
	existing, _, err := unstructured.NestedSlice(root, fieldPath...)
	if err != nil {
		return fmt.Errorf("read existing %s: %w", strings.Join(fieldPath, "."), err)
	}
	for _, expr := range exprs {
		entry, parseErr := ParseConfigValue(expr)
		if parseErr != nil {
			return UsageError("apply --config-value %q: %s", expr, parseErr.Error())
		}
		replaced := false
		for i, raw := range existing {
			m, ok := raw.(map[string]interface{})
			if ok && m["fileName"] == entry["fileName"] && m["key"] == entry["key"] {
				existing[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			existing = append(existing, entry)
		}
	}
	if len(existing) > MaxConfigValues {
		return UsageError("%s has %d entries; the CRD accepts at most %d", strings.Join(fieldPath, "."), len(existing), MaxConfigValues)
	}
	return unstructured.SetNestedSlice(root, existing, fieldPath...)
}
