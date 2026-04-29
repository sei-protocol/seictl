// Package aggregator updates the cell-aggregator kustomization at
// `clusters/harbor/engineers/kustomization.yaml` so that each `seictl
// onboard --apply` PR is fully self-wired into Flux.
//
// The grandparent file (`clusters/harbor/kustomization.yaml`) is a
// one-time human-reviewed bootstrap and is intentionally not touched.
// See sei-protocol/platform#249.
package aggregator

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const aggregatorPath = "clusters/harbor/engineers/kustomization.yaml"

// ErrAggregatorMissing is the sentinel returned when the aggregator
// kustomization doesn't exist on disk. Callers map it to a typed CLI
// error pointing at the bootstrap-PR remediation path.
var ErrAggregatorMissing = errors.New("aggregator kustomization missing")

// Result is what UpdateEngineers returns: the repo-relative path of the
// aggregator, the rewritten content, and whether the alias was actually
// added (false means the alias was already present and the original
// bytes are returned unchanged so callers can skip including the file
// in the PR for a smaller, more honest diff).
type Result struct {
	Path    string
	Content []byte
	Added   bool
}

// UpdateEngineers reads the aggregator from repoPath, inserts alias
// into its `resources:` list (sorted alphabetically, idempotent), and
// returns the rewritten file. ErrAggregatorMissing fires when the
// aggregator file doesn't exist — that's a one-time human bootstrap.
func UpdateEngineers(repoPath, alias string) (Result, error) {
	full := filepath.Join(repoPath, aggregatorPath)
	raw, err := os.ReadFile(full)
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, ErrAggregatorMissing
	}
	if err != nil {
		return Result{}, fmt.Errorf("read aggregator: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Result{}, fmt.Errorf("parse aggregator: %w", err)
	}
	seq, err := findResourcesSeq(&doc)
	if err != nil {
		return Result{}, err
	}

	aliases := make([]string, 0, len(seq.Content)+1)
	for _, n := range seq.Content {
		if n.Kind != yaml.ScalarNode {
			return Result{}, fmt.Errorf("aggregator resources entry is not a bare string at line %d", n.Line)
		}
		if n.Value == alias {
			return Result{Path: aggregatorPath, Content: raw, Added: false}, nil
		}
		aliases = append(aliases, n.Value)
	}
	aliases = append(aliases, alias)
	sort.Strings(aliases)

	seq.Style = 0
	seq.Content = seq.Content[:0]
	for _, a := range aliases {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: a})
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return Result{}, fmt.Errorf("encode aggregator: %w", err)
	}
	if err := enc.Close(); err != nil {
		return Result{}, fmt.Errorf("close encoder: %w", err)
	}
	return Result{Path: aggregatorPath, Content: buf.Bytes(), Added: true}, nil
}

// findResourcesSeq locates the `resources:` sequence in the top-level
// kustomization mapping. Errors if `resources` is missing or not a
// sequence — the aggregator schema is constrained and we own it.
func findResourcesSeq(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("aggregator is not a YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("aggregator root is not a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == "resources" {
			if v.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("aggregator `resources` is not a sequence (line %d)", v.Line)
			}
			return v, nil
		}
	}
	return nil, errors.New("aggregator has no `resources` key")
}
