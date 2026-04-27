package kube

import (
	"os"
	"path/filepath"
	"strings"
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

func TestNew(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		path := writeFixture(t, harborKubeconfig)
		c, err := New(Options{Kubeconfig: path})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.ContextName != "harbor" || c.ClusterName != "harbor-eks" || c.Namespace != "eng-bdc" {
			t.Errorf("got %+v", c)
		}
		if c.ClusterServer != "https://harbor.example.com" {
			t.Errorf("server: %q", c.ClusterServer)
		}
	})

	t.Run("context override falls back to default namespace", func(t *testing.T) {
		path := writeFixture(t, harborKubeconfig)
		c, err := New(Options{Kubeconfig: path, Context: "scratch"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.ContextName != "scratch" {
			t.Errorf("context: %q", c.ContextName)
		}
		if c.Namespace != "default" {
			t.Errorf("namespace fallback: %q", c.Namespace)
		}
	})

	t.Run("namespace override", func(t *testing.T) {
		path := writeFixture(t, harborKubeconfig)
		c, err := New(Options{Kubeconfig: path, Namespace: "eng-other"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.Namespace != "eng-other" {
			t.Errorf("namespace: %q", c.Namespace)
		}
	})

	t.Run("missing context lists available", func(t *testing.T) {
		path := writeFixture(t, harborKubeconfig)
		_, err := New(Options{Kubeconfig: path, Context: "ghost"})
		if err == nil {
			t.Fatalf("expected error")
		}
		if err.Category != clioutput.CatKubeconfigParse {
			t.Errorf("category: %q", err.Category)
		}
		if !strings.Contains(err.Message, "harbor") || !strings.Contains(err.Message, "scratch") {
			t.Errorf("error message should list available contexts; got %q", err.Message)
		}
	})

	t.Run("no current-context", func(t *testing.T) {
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
		if err.Category != clioutput.CatKubeconfigParse || err.Code != clioutput.ExitIdentity {
			t.Errorf("got %+v", err)
		}
	})

	t.Run("malformed kubeconfig", func(t *testing.T) {
		path := writeFixture(t, "not: [valid yaml")
		_, err := New(Options{Kubeconfig: path})
		if err == nil {
			t.Fatalf("expected parse error")
		}
		if err.Category != clioutput.CatKubeconfigParse {
			t.Errorf("category: %q", err.Category)
		}
	})
}
