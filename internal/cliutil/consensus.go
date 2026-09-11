package cliutil

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// consensusEngineValues mirrors the CRD enum on spec.consensus.engine.
var consensusEngineValues = []string{"Tendermint", "Autobahn"}

// ApplyConsensus sets spec.consensus.engine from --consensus-engine and
// spec.consensus.evmOnly from --evm-only. Both unset leaves spec.consensus
// absent, which the controller resolves to Tendermint. Cross-field rules are
// checked by ValidateConsensus after --set has run.
func ApplyConsensus(root map[string]interface{}, engine string, evmOnly bool) error {
	if engine != "" {
		canonical := ""
		for _, v := range consensusEngineValues {
			if strings.EqualFold(v, engine) {
				canonical = v
				break
			}
		}
		if canonical == "" {
			return UsageError("--consensus-engine %q is not one of %s", engine, strings.Join(consensusEngineValues, ", "))
		}
		if err := unstructured.SetNestedField(root, canonical, "spec", "consensus", "engine"); err != nil {
			return fmt.Errorf("apply --consensus-engine: %w", err)
		}
	}
	if evmOnly {
		if err := unstructured.SetNestedField(root, true, "spec", "consensus", "evmOnly"); err != nil {
			return fmt.Errorf("apply --evm-only: %w", err)
		}
	}
	return nil
}

// ValidateConsensus re-reads spec.consensus from the merged object so the
// evmOnly-requires-Autobahn rule holds however the fields got there (flags,
// --set, a preset), and names the flags instead of the CRD's field message.
func ValidateConsensus(root map[string]interface{}) error {
	evmOnly, _, err := unstructured.NestedBool(root, "spec", "consensus", "evmOnly")
	if err != nil {
		return UsageError("read spec.consensus.evmOnly: %s", err.Error())
	}
	if !evmOnly {
		return nil
	}
	engine, _, err := unstructured.NestedString(root, "spec", "consensus", "engine")
	if err != nil {
		return UsageError("read spec.consensus.engine: %s", err.Error())
	}
	if engine != "Autobahn" {
		return UsageError("--evm-only requires --consensus-engine Autobahn (spec.consensus.engine is %q): the EVM-only executor runs only under Autobahn", engine)
	}
	return nil
}
