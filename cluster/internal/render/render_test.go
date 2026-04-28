package render

import (
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

func TestRender(t *testing.T) {
	t.Run("substitutes simple vars", func(t *testing.T) {
		out, err := Render([]byte("hello ${NAME}, chain=${CHAIN_ID}"),
			map[string]string{"NAME": "bdc", "CHAIN_ID": "bench-bdc-demo"})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if string(out) != "hello bdc, chain=bench-bdc-demo" {
			t.Errorf("got %q", string(out))
		}
	})

	t.Run("fails closed on missing vars", func(t *testing.T) {
		_, err := Render([]byte("${A} and ${B} and ${A}"), map[string]string{"A": "x"})
		if err == nil {
			t.Fatalf("expected error")
		}
		if err.Category != clioutput.CatTemplateRender {
			t.Errorf("category: %q", err.Category)
		}
		// Missing var list should be deduped + sorted.
		if !strings.Contains(err.Message, "[B]") {
			t.Errorf("expected [B] in message, got %q", err.Message)
		}
	})

	t.Run("multiple missing vars are deduped and sorted", func(t *testing.T) {
		_, err := Render([]byte("${C}${A}${B}${A}"), map[string]string{})
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(err.Message, "[A B C]") {
			t.Errorf("expected sorted dedup list, got %q", err.Message)
		}
	})
}

func TestSplitYAML(t *testing.T) {
	t.Run("two docs", func(t *testing.T) {
		input := []byte(`---
kind: A
metadata:
  name: a1
---
kind: B
metadata:
  name: b1
`)
		docs := SplitYAML(input)
		if len(docs) != 2 {
			t.Fatalf("docs: got %d, want 2", len(docs))
		}
		if !strings.Contains(string(docs[0]), "a1") || !strings.Contains(string(docs[1]), "b1") {
			t.Errorf("doc contents wrong: %s | %s", docs[0], docs[1])
		}
	})

	t.Run("single doc no leading separator", func(t *testing.T) {
		docs := SplitYAML([]byte("kind: X\nmetadata:\n  name: x1\n"))
		if len(docs) != 1 {
			t.Fatalf("docs: got %d, want 1", len(docs))
		}
	})

	t.Run("empty docs are skipped", func(t *testing.T) {
		docs := SplitYAML([]byte("---\n---\nkind: X\nmetadata:\n  name: x1\n---\n"))
		if len(docs) != 1 {
			t.Fatalf("docs: got %d, want 1", len(docs))
		}
	})

	t.Run("comment-only preamble is dropped", func(t *testing.T) {
		input := []byte("# Provenance line 1\n# Provenance line 2\n---\nkind: X\nmetadata:\n  name: x1\n")
		docs := SplitYAML(input)
		if len(docs) != 1 {
			t.Fatalf("docs: got %d, want 1", len(docs))
		}
		if !strings.Contains(string(docs[0]), "kind: X") {
			t.Errorf("expected kind doc, got %s", docs[0])
		}
	})
}

func TestExtractRef(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		ref, err := ExtractRef([]byte("kind: SeiNodeDeployment\nmetadata:\n  name: bench-bdc-demo\n  namespace: eng-bdc\n"))
		if err != nil {
			t.Fatalf("ExtractRef: %v", err)
		}
		if ref.Kind != "SeiNodeDeployment" || ref.Name != "bench-bdc-demo" || ref.Namespace != "eng-bdc" {
			t.Errorf("ref: %+v", ref)
		}
	})

	t.Run("missing kind", func(t *testing.T) {
		_, err := ExtractRef([]byte("metadata:\n  name: nope\n"))
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		_, err := ExtractRef([]byte("kind: ConfigMap\n"))
		if err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestIndent(t *testing.T) {
	out := Indent("a\nb\n\nc", "  ")
	if out != "  a\n  b\n\n  c" {
		t.Errorf("got %q", out)
	}
}
