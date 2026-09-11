package cliutil

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// nodeIsolationValues mirrors the CRD enum on spec.scheduling.nodeIsolation.
var nodeIsolationValues = []string{"Shared", "Dedicated"}

// ApplyNodeIsolation sets spec.scheduling.nodeIsolation from --node-isolation.
// The match is case-insensitive so `dedicated` works from a shell, but the
// value written is the CRD's exact enum spelling. Empty leaves the field
// unset, which the controller resolves to Shared after its legacy fallback.
func ApplyNodeIsolation(root map[string]interface{}, value string) error {
	if value == "" {
		return nil
	}
	for _, v := range nodeIsolationValues {
		if strings.EqualFold(v, value) {
			if err := unstructured.SetNestedField(root, v, "spec", "scheduling", "nodeIsolation"); err != nil {
				return fmt.Errorf("apply --node-isolation: %w", err)
			}
			return nil
		}
	}
	return UsageError("--node-isolation %q is not one of %s", value, strings.Join(nodeIsolationValues, ", "))
}
