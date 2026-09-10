package cliutil

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ValidateQuantity rejects a resource-flag value the apiserver would
// reject at admission. Without it a `--memory 32GB` renders a CR that
// only fails once it has been committed, PR-merged, and picked up by
// Flux — the operator learns about the typo minutes later, from someone
// else's reconcile loop. Empty means "flag unset"; the preset default
// stands.
func ValidateQuantity(flag, value string) error {
	if value == "" {
		return nil
	}
	if _, err := resource.ParseQuantity(value); err != nil {
		return UsageError("--%s %q is not a valid Kubernetes quantity: %s", flag, value, err.Error())
	}
	return nil
}

// RejectCPULimit fails a render carrying spec.resources.limits.cpu. No
// discrete flag writes that path, but --set can reach it, and the CRD's
// CEL rejects it at admission (seinode_types.go:152 — "resources.limits
// accepts only memory: seid deliberately carries no CPU limit").
//
// Narrow by design: limits.memory is left alone because the CRD permits
// it when it equals requests.memory. Rejecting the whole limits block
// here would forbid a spelling the schema allows.
func RejectCPULimit(root map[string]interface{}) error {
	_, found, err := unstructured.NestedFieldNoCopy(root, "spec", "resources", "limits", "cpu")
	if err != nil {
		return UsageError("read spec.resources.limits.cpu: %s", err.Error())
	}
	if found {
		return UsageError("spec.resources.limits.cpu is not allowed: seid deliberately carries no CPU limit and the CRD rejects one at admission (spec.resources.limits accepts only memory)")
	}
	return nil
}
