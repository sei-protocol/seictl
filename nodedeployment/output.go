package nodedeployment

import (
	"fmt"
	"strings"

	"k8s.io/cli-runtime/pkg/printers"
)

// makePrinter returns a printer for -o values: yaml (default), json,
// name (kind/name), jsonpath=<template>. Anything else is a usage error
// the caller surfaces via emitStatus.
func makePrinter(format string) (printers.ResourcePrinter, error) {
	switch {
	case format == "" || format == "yaml":
		return &printers.YAMLPrinter{}, nil
	case format == "json":
		return &printers.JSONPrinter{}, nil
	case format == "name":
		return &printers.NamePrinter{}, nil
	case strings.HasPrefix(format, "jsonpath="):
		tmpl := strings.TrimPrefix(format, "jsonpath=")
		if tmpl == "" {
			return nil, fmt.Errorf("empty jsonpath template")
		}
		p, err := printers.NewJSONPathPrinter(tmpl)
		if err != nil {
			return nil, fmt.Errorf("invalid jsonpath: %w", err)
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unknown -o format %q (use yaml|json|name|jsonpath=...)", format)
	}
}
