// SSA orchestration over k8s.io/cli-runtime/pkg/resource.Builder. The
// Builder handles the generic kubectl-style pipeline (parse YAML stream,
// resolve REST mappings, build per-object REST clients) using our
// Client's ConfigFlags as the RESTClientGetter; this file adds the
// seictl-specific orchestration on top: namespace pre-flight,
// generation-based action attribution, and an actionable wrapper for
// "CRD missing" errors.
package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// ApplyResult is what Apply emits per input document. Action distinguishes
// create / update / unchanged, sourced from .metadata.generation (which
// only ticks on spec changes, unlike resourceVersion which controllers
// bump for status writes).
type ApplyResult struct {
	Kind      string
	Name      string
	Namespace string
	Action    string // "create" | "update" | "unchanged"
}

// Apply does server-side apply on each input doc with the given field
// owner, writing into namespace. The contract is all-or-nothing: any
// failure aborts and returns no partial results, matching the LLD's
// "no partial-state tracking" stance — the engineer re-runs.
func (c *Client) Apply(ctx context.Context, fieldOwner, namespace string, docs [][]byte) ([]ApplyResult, error) {
	if c.flags == nil {
		return nil, errors.New("kube.Client has no ConfigFlags; constructed without cluster access")
	}
	if err := c.requireNamespace(ctx, namespace); err != nil {
		return nil, err
	}

	stream := bytes.Join(docs, []byte("\n---\n"))
	result := resource.NewBuilder(c.flags).
		Unstructured().
		ContinueOnError().
		NamespaceParam(namespace).DefaultNamespace().
		Stream(bytes.NewReader(stream), "seictl-bench").
		Flatten().
		Do()
	if err := result.Err(); err != nil {
		return nil, wrapMappingErr(err)
	}

	var results []ApplyResult
	visitErr := result.Visit(func(info *resource.Info, vErr error) error {
		if vErr != nil {
			return wrapMappingErr(vErr)
		}
		ar, err := applyOne(info, fieldOwner)
		if err != nil {
			return err
		}
		results = append(results, ar)
		return nil
	})
	if visitErr != nil {
		return nil, visitErr
	}
	return results, nil
}

func applyOne(info *resource.Info, fieldOwner string) (ApplyResult, error) {
	helper := resource.NewHelper(info.Client, info.Mapping).WithFieldManager(fieldOwner)
	kind := info.Mapping.GroupVersionKind.Kind

	oldGen, existed, err := preGetGeneration(helper, info.Namespace, info.Name)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("pre-get %s/%s in %s: %w", kind, info.Name, info.Namespace, err)
	}

	// Force is intentionally off. With proper field-manager segregation
	// (controller owns .status, seictl-bench owns .spec), no SSA conflict
	// should arise in steady state. A 409 here means an unexpected writer
	// holds a field we're claiming — surface it loudly rather than
	// silently steal. A future --force-ownership flag is the ops-rescue
	// path if one's ever needed.
	body, err := runtime.Encode(unstructured.UnstructuredJSONScheme, info.Object)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("marshal %s/%s: %w", kind, info.Name, err)
	}
	applied, err := helper.Patch(info.Namespace, info.Name, types.ApplyPatchType, body, &metav1.PatchOptions{
		Force: ptr.To(false),
	})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply %s/%s in %s: %w", kind, info.Name, info.Namespace, err)
	}

	newGen, _ := generationOf(applied)
	return ApplyResult{
		Kind:      kind,
		Name:      info.Name,
		Namespace: info.Namespace,
		Action:    actionFor(existed, oldGen, newGen),
	}, nil
}

func preGetGeneration(helper *resource.Helper, namespace, name string) (gen int64, existed bool, err error) {
	got, err := helper.Get(namespace, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	g, _ := generationOf(got)
	return g, true, nil
}

// requireNamespace fails closed with an actionable message if the
// engineer hasn't run `seictl onboard` yet — Apply does not auto-create
// namespaces, and the bare K8s 404 is not self-serve.
func (c *Client) requireNamespace(ctx context.Context, namespace string) error {
	cfg, err := c.flags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("build REST config for namespace check: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build clientset for namespace check: %w", err)
	}
	_, err = clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("namespace %q not found; run `seictl onboard --apply` to create it", namespace)
	}
	return fmt.Errorf("verify namespace %q: %w", namespace, err)
}

func wrapMappingErr(err error) error {
	if meta.IsNoMatchError(err) {
		return fmt.Errorf("kind not installed in this cluster (CRD missing); contact the platform team: %w", err)
	}
	return err
}

func generationOf(obj runtime.Object) (int64, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return 0, err
	}
	return accessor.GetGeneration(), nil
}

func actionFor(existed bool, oldGen, newGen int64) string {
	switch {
	case !existed:
		return "create"
	case oldGen == newGen:
		return "unchanged"
	default:
		return "update"
	}
}
