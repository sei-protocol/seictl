package onboardmanifests

import (
	"strings"
	"testing"
)

func TestGenerate_ReturnsAllCellFilesAtExpectedPaths(t *testing.T) {
	files, err := Generate(Cell{Alias: "bdc", Namespace: "eng-bdc"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 6 {
		t.Fatalf("files: got %d, want 6", len(files))
	}
	want := map[string]bool{
		"clusters/harbor/engineers/bdc/namespace.yaml":                false,
		"clusters/harbor/engineers/bdc/workload-service-account.yaml": false,
		"clusters/harbor/engineers/bdc/flux-gitrepository.yaml":       false,
		"clusters/harbor/engineers/bdc/flux-kustomization.yaml":       false,
		"clusters/harbor/engineers/bdc/flux-rbac.yaml":                false,
		"clusters/harbor/engineers/bdc/kustomization.yaml":            false,
	}
	for _, f := range files {
		if _, ok := want[f.Path]; !ok {
			t.Errorf("unexpected path %q", f.Path)
		}
		want[f.Path] = true
	}
	for path, seen := range want {
		if !seen {
			t.Errorf("missing expected file: %s", path)
		}
	}
}

func TestGenerate_NamespaceContent(t *testing.T) {
	files, _ := Generate(Cell{Alias: "bdc", Namespace: "eng-bdc"})
	ns := contentFor(t, files, "namespace.yaml")
	if !strings.Contains(ns, "name: eng-bdc") {
		t.Errorf("namespace name: %s", ns)
	}
	if !strings.Contains(ns, "tide.sei.io/owner: bdc") {
		t.Errorf("owner label: %s", ns)
	}
	if !strings.Contains(ns, "tide.sei.io/cell-type: personal") {
		t.Errorf("cell-type label: %s", ns)
	}
}

func TestGenerate_ServiceAccountHasNoIRSAAnnotation(t *testing.T) {
	files, _ := Generate(Cell{Alias: "bdc", Namespace: "eng-bdc"})
	sa := contentFor(t, files, "workload-service-account.yaml")
	if !strings.Contains(sa, "name: workload-service-account") {
		t.Errorf("SA name: %s", sa)
	}
	// The IRSA/Pod-Identity confusion: ensure we never accidentally
	// add the IRSA-style role-arn annotation. Pod Identity binds
	// server-side, not via SA annotation.
	if strings.Contains(sa, "eks.amazonaws.com/role-arn") {
		t.Errorf("SA must not carry IRSA annotation; got %s", sa)
	}
}

func TestGenerate_KustomizationReferencesAllResources(t *testing.T) {
	files, _ := Generate(Cell{Alias: "bdc", Namespace: "eng-bdc"})
	k := contentFor(t, files, "/kustomization.yaml")
	for _, want := range []string{
		"namespace.yaml",
		"workload-service-account.yaml",
		"flux-gitrepository.yaml",
		"flux-kustomization.yaml",
		"flux-rbac.yaml",
	} {
		if !strings.Contains(k, want) {
			t.Errorf("cell kustomization missing resource %q in:\n%s", want, k)
		}
	}
}

func contentFor(t *testing.T, files []File, suffix string) string {
	t.Helper()
	for _, f := range files {
		if strings.HasSuffix(f.Path, suffix) {
			return string(f.Content)
		}
	}
	t.Fatalf("file with suffix %q not found", suffix)
	return ""
}
