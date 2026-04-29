package aggregator

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateEngineers(t *testing.T) {
	t.Run("appends alias and sorts", func(t *testing.T) {
		repo := writeAggregator(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - charlie
  - bob
`)
		got, err := UpdateEngineers(repo, "alice")
		if err != nil {
			t.Fatalf("UpdateEngineers: %v", err)
		}
		if !got.Added {
			t.Errorf("Added should be true on insert")
		}
		want := []string{"  - alice", "  - bob", "  - charlie"}
		for _, line := range want {
			if !bytes.Contains(got.Content, []byte(line)) {
				t.Errorf("expected line %q in output:\n%s", line, got.Content)
			}
		}
	})

	t.Run("idempotent when alias already present", func(t *testing.T) {
		body := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - alice
  - bob
`
		repo := writeAggregator(t, body)
		got, err := UpdateEngineers(repo, "alice")
		if err != nil {
			t.Fatalf("UpdateEngineers: %v", err)
		}
		if got.Added {
			t.Errorf("Added should be false on idempotent path")
		}
		if !bytes.Equal(got.Content, []byte(body)) {
			t.Errorf("idempotent path should return original bytes unchanged; got:\n%s", got.Content)
		}
	})

	t.Run("missing aggregator returns sentinel", func(t *testing.T) {
		repo := t.TempDir()
		_, err := UpdateEngineers(repo, "alice")
		if !errors.Is(err, ErrAggregatorMissing) {
			t.Errorf("expected ErrAggregatorMissing; got %v", err)
		}
	})

	t.Run("malformed yaml errors but is not the sentinel", func(t *testing.T) {
		repo := writeAggregator(t, "not: [valid: yaml: content")
		_, err := UpdateEngineers(repo, "alice")
		if err == nil {
			t.Fatal("expected error on malformed YAML")
		}
		if errors.Is(err, ErrAggregatorMissing) {
			t.Errorf("malformed YAML should not surface as ErrAggregatorMissing; got %v", err)
		}
	})

	t.Run("returns repo-relative aggregator path", func(t *testing.T) {
		repo := writeAggregator(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
		got, err := UpdateEngineers(repo, "alice")
		if err != nil {
			t.Fatalf("UpdateEngineers: %v", err)
		}
		if got.Path != "clusters/harbor/engineers/kustomization.yaml" {
			t.Errorf("Path: got %q want repo-relative aggregator path", got.Path)
		}
	})
}

func writeAggregator(t *testing.T, body string) string {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "harbor", "engineers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return repo
}
