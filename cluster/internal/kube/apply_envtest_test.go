//go:build envtest

package kube_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/sei-protocol/seictl/cluster/internal/kube"
	"github.com/sei-protocol/seictl/internal/testenv"
)

const fieldOwner = "seictl-bench"

var (
	testEnv     *testenv.Env
	fixtureBody []byte
)

func TestMain(m *testing.M) {
	env, err := testenv.Start()
	if err != nil {
		log.Printf("envtest unavailable, skipping: %v", err)
		os.Exit(0)
	}
	testEnv = env
	body, err := os.ReadFile("testdata/fixture.yaml")
	if err != nil {
		log.Fatalf("read fixture: %v", err)
	}
	fixtureBody = body

	code := m.Run()
	if err := env.Stop(); err != nil {
		log.Printf("envtest stop: %v", err)
	}
	os.Exit(code)
}

// renderFixture returns a 3-doc bundle with NAMESPACE substituted, split
// into individual byte slices in YAML stream order.
func renderFixture(namespace string) [][]byte {
	full := strings.ReplaceAll(string(fixtureBody), "${NAMESPACE}", namespace)
	docs := splitDocs(full)
	out := make([][]byte, len(docs))
	for i, d := range docs {
		out[i] = []byte(d)
	}
	return out
}

// splitDocs is a minimal multi-doc YAML splitter sufficient for the
// fixture above (no `---` inside string values).
func splitDocs(s string) []string {
	parts := strings.Split(s, "\n---\n")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "---\n")
		if p == "" || allCommentLines(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func allCommentLines(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "#") {
			return false
		}
	}
	return true
}

func newTestClient(t *testing.T) *kube.Client {
	t.Helper()
	path := testEnv.WriteKubeconfig(t)
	kc, kerr := kube.New(kube.Options{Kubeconfig: path})
	if kerr != nil {
		t.Fatalf("kube.New: %v", kerr)
	}
	return kc
}

func TestApply_FirstApplyMarksAllCreate(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "create")
	kc := newTestClient(t)

	results, err := kc.Apply(context.Background(), fieldOwner, ns, renderFixture(ns))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results: got %d, want 3", len(results))
	}
	for _, r := range results {
		if r.Action != "create" {
			t.Errorf("%s/%s action: got %q, want create", r.Kind, r.Name, r.Action)
		}
	}
}

func TestApply_IdempotentReapplyIsUnchanged(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "unchanged")
	kc := newTestClient(t)
	docs := renderFixture(ns)

	if _, err := kc.Apply(context.Background(), fieldOwner, ns, docs); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	results, err := kc.Apply(context.Background(), fieldOwner, ns, docs)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	for _, r := range results {
		if r.Action != "unchanged" {
			t.Errorf("%s/%s action: got %q, want unchanged", r.Kind, r.Name, r.Action)
		}
	}
}

func TestApply_SpecChangeMarksOnlyChangedDocUpdate(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "update")
	kc := newTestClient(t)

	if _, err := kc.Apply(context.Background(), fieldOwner, ns, renderFixture(ns)); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Mutate dep-alpha's replicas (the only doc-specific occurrence of
	// "name: dep-alpha\n[...]\n  replicas: 1"). Leave the other two
	// Deployments at replicas: 1.
	mutated := strings.ReplaceAll(string(fixtureBody), "${NAMESPACE}", ns)
	mutated = strings.Replace(mutated,
		"name: dep-alpha\n  namespace: "+ns+"\nspec:\n  replicas: 1",
		"name: dep-alpha\n  namespace: "+ns+"\nspec:\n  replicas: 2", 1)
	mutatedDocs := make([][]byte, 0)
	for _, d := range splitDocs(mutated) {
		mutatedDocs = append(mutatedDocs, []byte(d))
	}

	results, err := kc.Apply(context.Background(), fieldOwner, ns, mutatedDocs)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	updates := 0
	unchanged := 0
	for _, r := range results {
		switch r.Action {
		case "update":
			updates++
			if r.Name != "dep-alpha" {
				t.Errorf("only dep-alpha should be updated; got update on %s", r.Name)
			}
		case "unchanged":
			unchanged++
		default:
			t.Errorf("unexpected action %q on %s", r.Action, r.Name)
		}
	}
	if updates != 1 || unchanged != 2 {
		t.Errorf("action counts: %d update, %d unchanged; want 1/2", updates, unchanged)
	}
}

func TestApply_MissingNamespaceIsActionable(t *testing.T) {
	// Don't pre-create the namespace.
	ns := "ghost-" + strings.ToLower(t.Name())
	kc := newTestClient(t)

	_, err := kc.Apply(context.Background(), fieldOwner, ns, renderFixture(ns))
	if err == nil {
		t.Fatalf("expected error for missing namespace")
	}
	if !strings.Contains(err.Error(), "run `seictl onboard --apply`") {
		t.Errorf("error should be actionable; got %v", err)
	}
}

func TestApply_CRDNotInstalledIsActionable(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "missing-crd")
	kc := newTestClient(t)

	doc := []byte(fmt.Sprintf(`apiVersion: tide.example.io/v1
kind: NotInstalled
metadata:
  name: phantom
  namespace: %s
spec:
  data: irrelevant
`, ns))

	_, err := kc.Apply(context.Background(), fieldOwner, ns, [][]byte{doc})
	if err == nil {
		t.Fatalf("expected error for unregistered GVK")
	}
	if !strings.Contains(err.Error(), "kind not installed") {
		t.Errorf("error should mention CRD missing; got %v", err)
	}
}

func TestApply_FieldConflictSurfacesConflictingManager(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "conflict")
	kc := newTestClient(t)

	// Pre-seed dep-alpha with a different field manager owning .spec.replicas.
	cs, err := kubernetes.NewForConfig(testEnv.RESTConfig())
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	preseedBody := fmt.Sprintf(`{
		"apiVersion": "apps/v1",
		"kind": "Deployment",
		"metadata": {"name": "dep-alpha", "namespace": %q},
		"spec": {
			"replicas": 5,
			"selector": {"matchLabels": {"app": "dep-alpha"}},
			"template": {
				"metadata": {"labels": {"app": "dep-alpha"}},
				"spec": {"containers": [{"name": "c", "image": "busybox"}]}
			}
		}
	}`, ns)
	_, err = cs.AppsV1().Deployments(ns).Patch(
		context.Background(), "dep-alpha", types.ApplyPatchType,
		[]byte(preseedBody),
		metav1.PatchOptions{FieldManager: "kubectl-client-side-apply"},
	)
	if err != nil {
		t.Fatalf("preseed: %v", err)
	}

	// Now apply our bundle with seictl-bench claiming the same field.
	_, err = kc.Apply(context.Background(), fieldOwner, ns, renderFixture(ns))
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	var statusErr *apierrors.StatusError
	if !errors.As(err, &statusErr) || !apierrors.IsConflict(err) {
		t.Errorf("expected apierrors.IsConflict, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "kubectl-client-side-apply") {
		t.Errorf("error should name conflicting manager; got %v", err)
	}
}
