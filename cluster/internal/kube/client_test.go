package kube

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
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

func stubInCluster(t *testing.T, cfg *rest.Config, err error) {
	t.Helper()
	old := inClusterConfigFn
	inClusterConfigFn = func() (*rest.Config, error) { return cfg, err }
	t.Cleanup(func() { inClusterConfigFn = old })
}

func stubInClusterNamespace(t *testing.T, ns string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "namespace")
	if err := os.WriteFile(path, []byte(ns+"\n"), 0o600); err != nil {
		t.Fatalf("seed namespace file: %v", err)
	}
	old := inClusterNamespaceFile
	inClusterNamespaceFile = path
	t.Cleanup(func() { inClusterNamespaceFile = old })
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

	t.Run("no current-context, no in-cluster", func(t *testing.T) {
		const minimal = `apiVersion: v1
kind: Config
contexts: []
clusters: []
users: []
`
		path := writeFixture(t, minimal)
		stubInCluster(t, nil, errors.New("not in a cluster"))
		_, err := New(Options{Kubeconfig: path})
		if err == nil {
			t.Fatalf("expected error")
		}
		if err.Category != clioutput.CatKubeconfigParse || err.Code != clioutput.ExitIdentity {
			t.Errorf("got %+v", err)
		}
		if !strings.Contains(err.Message, "ServiceAccount") {
			t.Errorf("error should mention ServiceAccount fallback; got %q", err.Message)
		}
	})

	t.Run("in-cluster fallback when no current-context", func(t *testing.T) {
		const minimal = `apiVersion: v1
kind: Config
contexts: []
clusters: []
users: []
`
		path := writeFixture(t, minimal)
		stubInCluster(t, &rest.Config{Host: "https://kubernetes.default.svc"}, nil)
		stubInClusterNamespace(t, "release-test")

		c, err := New(Options{Kubeconfig: path})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.ContextName != "in-cluster" || c.ClusterName != "in-cluster" {
			t.Errorf("synthetic context fields: %+v", c)
		}
		if c.ClusterServer != "https://kubernetes.default.svc" {
			t.Errorf("server: %q", c.ClusterServer)
		}
		if c.Namespace != "release-test" {
			t.Errorf("namespace from SA file: %q", c.Namespace)
		}
	})

	t.Run("in-cluster fallback honors namespace override", func(t *testing.T) {
		const minimal = `apiVersion: v1
kind: Config
contexts: []
clusters: []
users: []
`
		path := writeFixture(t, minimal)
		stubInCluster(t, &rest.Config{Host: "https://kubernetes.default.svc"}, nil)
		stubInClusterNamespace(t, "release-test")

		c, err := New(Options{Kubeconfig: path, Namespace: "override-ns"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.Namespace != "override-ns" {
			t.Errorf("namespace override: got %q want override-ns", c.Namespace)
		}
	})

	t.Run("in-cluster fallback when SA namespace file missing falls back to default", func(t *testing.T) {
		const minimal = `apiVersion: v1
kind: Config
contexts: []
clusters: []
users: []
`
		path := writeFixture(t, minimal)
		stubInCluster(t, &rest.Config{Host: "https://kubernetes.default.svc"}, nil)
		// Point the SA namespace file at a path that does not exist.
		oldFile := inClusterNamespaceFile
		inClusterNamespaceFile = filepath.Join(t.TempDir(), "missing")
		t.Cleanup(func() { inClusterNamespaceFile = oldFile })

		c, err := New(Options{Kubeconfig: path})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.Namespace != "default" {
			t.Errorf("expected default namespace; got %q", c.Namespace)
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
