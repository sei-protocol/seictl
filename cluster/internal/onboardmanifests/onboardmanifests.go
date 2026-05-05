// Package onboardmanifests generates the engineer cell's Kustomize
// resources for `seictl onboard`.
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
