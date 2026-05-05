// Package onboardmanifests generates the engineer cell's Kustomize
// resources for `seictl onboard`.
//
// v1 ships namespace, bare workload ServiceAccount (the K8s anchor for
// Pod Identity), the per-engineer Flux GitRepository + Kustomization
// that watches the workspace branch, the in-namespace flux-reconciler
// SA + admin RoleBinding (security boundary; see sei-protocol/seictl#130),
// and the kustomization that wires them together. No engineer Role /
// RoleBinding for human identity — engineers operate as cluster-admin
// via SSO today; per-engineer scoped K8s identity is tracked at
// sei-protocol/seictl#80.
//
// The workload ServiceAccount carries no eks.amazonaws.com/role-arn
// annotation — that's IRSA's pattern, not Pod Identity. EKS Pod
// Identity binds server-side via (cluster, namespace, serviceAccount);
// annotating the SA is at best a no-op and at worst misleading to
// readers.
package onboardmanifests

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/*.yaml
var templatesFS embed.FS

type Cell struct {
	Alias     string
	Namespace string
}

type File struct {
	Path    string
	Content []byte
}

var cellTemplates = []string{
	"namespace.yaml",
	"workload-service-account.yaml",
	"flux-gitrepository.yaml",
	"flux-kustomization.yaml",
	"flux-rbac.yaml",
	"kustomization.yaml",
}

// Generate returns the engineer-cell files at their target platform-repo
// paths (`clusters/harbor/engineers/<alias>/...`).
func Generate(cell Cell) ([]File, error) {
	dir := fmt.Sprintf("clusters/harbor/engineers/%s/", cell.Alias)
	out := make([]File, 0, len(cellTemplates))
	for _, name := range cellTemplates {
		body, err := render(name, cell)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", name, err)
		}
		out = append(out, File{Path: dir + name, Content: body})
	}
	return out, nil
}

func render(name string, cell Cell) ([]byte, error) {
	body, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(name).Parse(string(body))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cell); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
