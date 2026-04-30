//go:build envtest

package kube_test

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

func seedConfigMap(t *testing.T, ns, name string, labels map[string]string) {
	t.Helper()
	cs, err := kubernetes.NewForConfig(testEnv.RESTConfig())
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	_, err = cs.CoreV1().ConfigMaps(ns).Create(context.Background(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Data:       map[string]string{"k": "v"},
		},
		metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed cm %s/%s: %v", ns, name, err)
	}
}

func TestList_LabelSelectorMatchesScopedObjects(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "list-match")
	kc := newTestClient(t)

	seedConfigMap(t, ns, "mine-1", map[string]string{"sei.io/engineer": "bdc", "app.kubernetes.io/part-of": "seictl-bench"})
	seedConfigMap(t, ns, "mine-2", map[string]string{"sei.io/engineer": "bdc", "app.kubernetes.io/part-of": "seictl-bench"})
	seedConfigMap(t, ns, "theirs", map[string]string{"sei.io/engineer": "alice", "app.kubernetes.io/part-of": "seictl-bench"})
	seedConfigMap(t, ns, "unrelated", map[string]string{"app.kubernetes.io/managed-by": "kubectl"})

	got, err := kc.List(context.Background(), kube.ListOptions{
		Namespace:     ns,
		Resources:     []string{"configmaps"},
		LabelSelector: "sei.io/engineer=bdc,app.kubernetes.io/part-of=seictl-bench",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d objects, want 2", len(got))
	}
	for _, u := range got {
		if u.GetLabels()["sei.io/engineer"] != "bdc" {
			t.Errorf("unexpected object %s/%s with labels %v", u.GetNamespace(), u.GetName(), u.GetLabels())
		}
	}
}

func TestList_EmptyResultOnNoMatches(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "list-empty")
	kc := newTestClient(t)

	got, err := kc.List(context.Background(), kube.ListOptions{
		Namespace:     ns,
		Resources:     []string{"configmaps"},
		LabelSelector: "sei.io/engineer=ghost",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result; got %d", len(got))
	}
}

func TestList_MultiResourceTypeReturnsAcrossKinds(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "list-multikind")
	kc := newTestClient(t)

	seedConfigMap(t, ns, "cm-1", map[string]string{"sei.io/engineer": "bdc"})
	// Seed a Job too via the typed clientset.
	cs, _ := kubernetes.NewForConfig(testEnv.RESTConfig())
	_, err := cs.AppsV1().Deployments(ns).Create(context.Background(), seedDeployment("dep-1", ns, "bdc"), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	got, err := kc.List(context.Background(), kube.ListOptions{
		Namespace:     ns,
		Resources:     []string{"configmaps", "deployments.apps"},
		LabelSelector: "sei.io/engineer=bdc",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	kinds := map[string]int{}
	for _, u := range got {
		kinds[u.GetKind()]++
	}
	if kinds["ConfigMap"] != 1 || kinds["Deployment"] != 1 {
		t.Errorf("expected 1 of each kind; got %v", kinds)
	}
}

func TestDelete_LabelSelectorDeletesMatchingObjects(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "del-match")
	kc := newTestClient(t)

	seedConfigMap(t, ns, "doomed-1", map[string]string{"sei.io/bench-name": "demo"})
	seedConfigMap(t, ns, "doomed-2", map[string]string{"sei.io/bench-name": "demo"})
	seedConfigMap(t, ns, "spared", map[string]string{"sei.io/bench-name": "other"})

	results, err := kc.Delete(context.Background(), kube.DeleteOptions{
		Namespace:     ns,
		Resources:     []string{"configmaps"},
		LabelSelector: "sei.io/bench-name=demo",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("results: got %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Action != "deleted" {
			t.Errorf("%s/%s action: %q", r.Kind, r.Name, r.Action)
		}
		if !strings.HasPrefix(r.Name, "doomed-") {
			t.Errorf("unexpected match %s", r.Name)
		}
	}
	// Spared CM should still be there.
	cs, _ := kubernetes.NewForConfig(testEnv.RESTConfig())
	_, err = cs.CoreV1().ConfigMaps(ns).Get(context.Background(), "spared", metav1.GetOptions{})
	if err != nil {
		t.Errorf("spared CM should remain: %v", err)
	}
}

func TestDelete_StillTerminatingWhenFinalizerHolds(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "del-term")
	kc := newTestClient(t)

	// Seed a CM with a finalizer; envtest has no controller to release it,
	// so DeleteWithOptions sets deletionTimestamp and the object lingers.
	cs, err := kubernetes.NewForConfig(testEnv.RESTConfig())
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	_, err = cs.CoreV1().ConfigMaps(ns).Create(context.Background(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "stuck",
				Namespace:  ns,
				Labels:     map[string]string{"sei.io/bench-name": "demo"},
				Finalizers: []string{"seictl.test/hold"},
			},
			Data: map[string]string{"k": "v"},
		},
		metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed cm: %v", err)
	}

	results, err := kc.Delete(context.Background(), kube.DeleteOptions{
		Namespace:     ns,
		Resources:     []string{"configmaps"},
		LabelSelector: "sei.io/bench-name=demo",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results: got %d, want 1", len(results))
	}
	if results[0].Action != "still-terminating" {
		t.Errorf("action: got %q, want \"still-terminating\"", results[0].Action)
	}

	// Release the finalizer so the test cleans up.
	cm, gerr := cs.CoreV1().ConfigMaps(ns).Get(context.Background(), "stuck", metav1.GetOptions{})
	if gerr == nil {
		cm.Finalizers = nil
		_, _ = cs.CoreV1().ConfigMaps(ns).Update(context.Background(), cm, metav1.UpdateOptions{})
	}
}

func TestDelete_EmptyResultOnNoMatches(t *testing.T) {
	ns := testEnv.UniqueNamespace(t, "del-empty")
	kc := newTestClient(t)

	results, err := kc.Delete(context.Background(), kube.DeleteOptions{
		Namespace:     ns,
		Resources:     []string{"configmaps"},
		LabelSelector: "sei.io/bench-name=ghost",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty result; got %d", len(results))
	}
}

// seedDeployment returns a minimal Deployment used as a non-CM kind in
// the multi-resource-type list test.
func seedDeployment(name, ns, engineer string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"sei.io/engineer": engineer},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
				},
			},
		},
	}
}
