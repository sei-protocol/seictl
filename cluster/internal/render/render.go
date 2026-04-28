// Package render handles ${VAR}-style substitution and YAML
// document splitting for templates rendered by `seictl bench up`.
//
// Substitution uses os.Expand and is fail-closed: any unresolved ${VAR}
// in a template returns CatTemplateRender so the engineer sees the gap
// rather than an empty-string-laundered manifest.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

// Render substitutes ${VAR} occurrences in tmpl with values from vars.
// Errors with the deduplicated, sorted list of missing keys.
func Render(tmpl []byte, vars map[string]string) ([]byte, *clioutput.Error) {
	missing := map[string]struct{}{}
	out := os.Expand(string(tmpl), func(key string) string {
		v, ok := vars[key]
		if !ok {
			missing[key] = struct{}{}
		}
		return v
	})
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatTemplateRender,
			"template references undefined vars: %v", keys)
	}
	return []byte(out), nil
}

// Indent left-pads every line of body with prefix. Trailing whitespace
// and a single trailing newline are preserved as the input had them.
func Indent(body, prefix string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// ManifestRef is the minimal manifest metadata bench up emits per
// rendered document.
type ManifestRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Action    string `json:"action"`
}

// SplitYAML returns each non-empty document from a multi-doc YAML
// stream. The split happens on YAML document boundaries (a `---` line
// at column zero), preserving each document's original formatting.
// Comment-only documents (e.g., a provenance preamble at the top of a
// file before the first `---`) are filtered out — they carry no
// manifest data and would fail downstream Kind/Name extraction.
func SplitYAML(data []byte) [][]byte {
	var (
		docs    [][]byte
		current []byte
	)
	flush := func() {
		buf := bytes.TrimSpace(current)
		if len(buf) > 0 && !isCommentOnly(buf) {
			docs = append(docs, append([]byte(nil), buf...))
		}
		current = current[:0]
	}
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if bytes.Equal(bytes.TrimRight(line, "\r\n"), []byte("---")) {
			flush()
			continue
		}
		current = append(current, line...)
	}
	flush()
	return docs
}

func isCommentOnly(doc []byte) bool {
	for _, line := range bytes.Split(doc, []byte("\n")) {
		t := bytes.TrimSpace(line)
		if len(t) == 0 {
			continue
		}
		if t[0] != '#' {
			return false
		}
	}
	return true
}

// ExtractRef pulls Kind / metadata.name / metadata.namespace from a
// rendered Kubernetes manifest. Action is left to the caller (dry-run
// reports "create"; --apply distinguishes create/update/unchanged).
func ExtractRef(doc []byte) (ManifestRef, error) {
	var m struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return ManifestRef{}, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Kind == "" {
		return ManifestRef{}, errors.New("manifest missing kind")
	}
	if m.Metadata.Name == "" {
		return ManifestRef{}, errors.New("manifest missing metadata.name")
	}
	return ManifestRef{Kind: m.Kind, Name: m.Metadata.Name, Namespace: m.Metadata.Namespace}, nil
}
