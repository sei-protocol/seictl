// Package onboardmanifests generates the engineer cell's Kustomize
// resources for `seictl onboard`. v1 ships three files: namespace,
// bare ServiceAccount (the K8s anchor for Pod Identity), and the
// kustomization that wires them together. No Role / RoleBinding —
// engineers operate as cluster-admin via SSO today; per-engineer
// scoped K8s identity is tracked at sei-protocol/seictl#80.
package onboardmanifests

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Cell is the input to Generate.
type Cell struct {
	Alias string // "bdc"
}

// Namespace returns "eng-<alias>".
func (c Cell) Namespace() string { return "eng-" + c.Alias }

// File represents one generated manifest with its target path under
// the platform repo (`clusters/harbor/engineers/<alias>/<filename>`).
type File struct {
	Path    string // platform-repo-relative path
	Content []byte
}

// Generate returns the three files the engineer cell needs. The path
// prefix `clusters/harbor/engineers/<alias>/` is per LLD §`onboard`.
func Generate(cell Cell) ([]File, error) {
	dir := fmt.Sprintf("clusters/harbor/engineers/%s/", cell.Alias)
	out := make([]File, 0, 3)
	for _, spec := range []struct {
		name string
		tmpl string
	}{
		{"namespace.yaml", namespaceTemplate},
		{"bench-seiload-sa.yaml", serviceAccountTemplate},
		{"kustomization.yaml", kustomizationTemplate},
	} {
		body, err := render(spec.tmpl, cell)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", spec.name, err)
		}
		out = append(out, File{Path: dir + spec.name, Content: body})
	}
	return out, nil
}

func render(tmpl string, cell Cell) ([]byte, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, cell); err != nil {
		return nil, err
	}
	// Defensive: trim any leading whitespace that Go templates add.
	out := strings.TrimLeft(buf.String(), "\n")
	return []byte(out), nil
}

const namespaceTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
  labels:
    app.kubernetes.io/name: {{.Namespace}}
    app.kubernetes.io/managed-by: flux
    tide.sei.io/cell-type: personal
    tide.sei.io/owner: {{.Alias}}
`

// The SA carries no eks.amazonaws.com/role-arn annotation — that's the
// IRSA pattern, not Pod Identity. EKS Pod Identity binds via the
// (cluster, namespace, serviceAccount) tuple stored server-side.
const serviceAccountTemplate = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: bench-seiload
  namespace: {{.Namespace}}
  labels:
    app.kubernetes.io/managed-by: flux
    tide.sei.io/cell-type: personal
    tide.sei.io/owner: {{.Alias}}
`

const kustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - bench-seiload-sa.yaml
commonLabels:
  tide.sei.io/cell-type: personal
  tide.sei.io/owner: {{.Alias}}
`
