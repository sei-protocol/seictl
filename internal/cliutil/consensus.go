package cliutil

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// consensusEngineValues mirrors the CRD enum on spec.consensus.engine.
var consensusEngineValues = []string{"Tendermint", "Autobahn"}

// ApplyConsensus sets spec.consensus from --consensus-engine and --evm-only.
// Both unset leaves spec.consensus absent, which the controller resolves to
// Tendermint. evmOnly without an engine is refused here rather than by the
// CRD, because the CRD's message names the field, not the flag.
func ApplyConsensus(root map[string]interface{}, engine string, evmOnly bool) error {
	if engine == "" && !evmOnly {
		return nil
	}
	if engine == "" {
		return UsageError("--evm-only requires --consensus-engine Autobahn: the EVM-only executor runs only under Autobahn")
	}
	canonical := ""
	for _, v := range consensusEngineValues {
		if strings.EqualFold(v, engine) {
			canonical = v
		}
	}
	if canonical == "" {
		return UsageError("--consensus-engine %q is not one of %s", engine, strings.Join(consensusEngineValues, ", "))
	}
	if evmOnly && canonical != "Autobahn" {
		return UsageError("--evm-only requires --consensus-engine Autobahn, got %s", canonical)
	}
	if err := unstructured.SetNestedField(root, canonical, "spec", "consensus", "engine"); err != nil {
		return fmt.Errorf("apply --consensus-engine: %w", err)
	}
	if evmOnly {
		if err := unstructured.SetNestedField(root, true, "spec", "consensus", "evmOnly"); err != nil {
			return fmt.Errorf("apply --evm-only: %w", err)
		}
	}
	return nil
}
