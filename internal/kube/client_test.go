package kube

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sei-protocol/seictl/internal/clioutput"
)

const harborKubeconfig = `apiVersion: v1
kind: Config
current-context: harbor
contexts:
- name: harbor
  context:
    cluster: harbor-eks
    user: harbor-sso
    namespace: eng-bdc
- name: scratch
  context:
    cluster: scratch-eks
    user: harbor-sso
clusters:
- name: harbor-eks
  cluster:
    server: https://harbor.example.com
- name: scratch-eks
  cluster:
    server: https://scratch.example.com
users:
- name: harbor-sso
  user:
    token: fake
`

func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed kubeconfig: %v", err)
	}
	return path
}

func TestNew_HappyPath(t *testing.T) {
	path := writeFixture(t, harborKubeconfig)
	c, err := New(Options{Kubeconfig: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.ContextName != "harbor" {
		t.Errorf("context: got %q", c.ContextName)
	}
	if c.ClusterName != "harbor-eks" {
		t.Errorf("cluster name: got %q", c.ClusterName)
	}
	if c.ClusterServer != "https://harbor.example.com" {
		t.Errorf("server: got %q", c.ClusterServer)
	}
	if c.Namespace != "eng-bdc" {
		t.Errorf("namespace: got %q", c.Namespace)
	}
	if c.RESTConfig == nil {
		t.Errorf("REST config nil")
	}
}

func TestNew_ContextOverride(t *testing.T) {
	path := writeFixture(t, harborKubeconfig)
	c, err := New(Options{Kubeconfig: path, Context: "scratch"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.ContextName != "scratch" {
		t.Errorf("context: got %q, want scratch", c.ContextName)
	}
	if c.Namespace != "default" {
		t.Errorf("namespace fallback: got %q, want default", c.Namespace)
	}
}

func TestNew_NamespaceOverride(t *testing.T) {
	path := writeFixture(t, harborKubeconfig)
	c, err := New(Options{Kubeconfig: path, Namespace: "eng-other"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Namespace != "eng-other" {
		t.Errorf("namespace: got %q", c.Namespace)
	}
}

func TestNew_MissingContext(t *testing.T) {
	path := writeFixture(t, harborKubeconfig)
	_, err := New(Options{Kubeconfig: path, Context: "ghost"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Category != clioutput.CatKubeconfigParse {
		t.Errorf("category: got %q", err.Category)
	}
}

func TestNew_NoCurrentContext(t *testing.T) {
	const minimal = `apiVersion: v1
kind: Config
contexts: []
clusters: []
users: []
`
	path := writeFixture(t, minimal)
	_, err := New(Options{Kubeconfig: path})
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Category != clioutput.CatKubeconfigParse {
		t.Errorf("category: got %q", err.Category)
	}
	if err.Code != clioutput.ExitIdentity {
		t.Errorf("code: got %d", err.Code)
	}
}

func TestNew_MalformedKubeconfig(t *testing.T) {
	path := writeFixture(t, "not: [valid yaml")
	_, err := New(Options{Kubeconfig: path})
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if err.Category != clioutput.CatKubeconfigParse {
		t.Errorf("category: got %q", err.Category)
	}
}
