package cliutil

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// RequireDNS1123Label returns a usage error unless value is a DNS-1123 label.
// The harness verbs splice --chain-id and --run-id into resource names and
// label values, so the strictest of the two rules applies to both.
func RequireDNS1123Label(flag, value string) error {
	if value == "" {
		return UsageError("--%s is required", flag)
	}
	if errs := validation.IsDNS1123Label(value); len(errs) > 0 {
		return UsageError("--%s %q: %s", flag, value, strings.Join(errs, "; "))
	}
	return nil
}

// RequireHarnessNames validates the chain-id and run-id pair shared by the
// chaos and bench render verbs, including the <prefix>-<run-id> resource name
// they produce.
func RequireHarnessNames(chainID, runID, namePrefix string) error {
	if err := RequireDNS1123Label("chain-id", chainID); err != nil {
		return err
	}
	if err := RequireDNS1123Label("run-id", runID); err != nil {
		return err
	}
	name := namePrefix + "-" + runID
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return UsageError("resource name %q (%s-<run-id>): %s", name, namePrefix, strings.Join(errs, "; "))
	}
	return nil
}
