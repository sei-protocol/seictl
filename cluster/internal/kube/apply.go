package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"
)

// ApplyResult is what Apply emits per input document. Action distinguishes
// create / update / unchanged, sourced from .metadata.generation (which
// only ticks on spec changes, unlike resourceVersion which the controller
// bumps for status writes).
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
	if c.restConfig == nil {
		return nil, errors.New("kube.Client has no REST config; constructed without cluster access")
	}
	dyn, err := dynamic.NewForConfig(c.restConfig)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(c.restConfig)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	groups, err := restmapper.GetAPIGroupResources(disc)
	if err != nil {
		return nil, fmt.Errorf("discover API groups: %w", err)
	}
	return ApplyWith(ctx, dyn, restmapper.NewDiscoveryRESTMapper(groups), fieldOwner, namespace, docs)
}

// ApplyWith is the seam tests inject through. Production callers go
// through Client.Apply.
func ApplyWith(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, fieldOwner, namespace string, docs [][]byte) ([]ApplyResult, error) {
	if err := requireNamespace(ctx, dyn, namespace); err != nil {
		return nil, err
	}

	results := make([]ApplyResult, 0, len(docs))
	for _, raw := range docs {
		obj := &unstructured.Unstructured{}
		if err := yamlutil.Unmarshal(raw, &obj.Object); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			if meta.IsNoMatchError(err) {
				return nil, fmt.Errorf("kind %s is not installed in this cluster (CRD missing); contact the platform team", gvk)
			}
			return nil, fmt.Errorf("rest mapping for %s: %w", gvk, err)
		}
		ri := resourceClient(dyn, mapping, obj.GetNamespace())

		oldGen, existed, err := preGetGeneration(ctx, ri, obj.GetName())
		if err != nil {
			return nil, fmt.Errorf("pre-get %s/%s in %s: %w", gvk.Kind, obj.GetName(), obj.GetNamespace(), err)
		}

		applied, raced, err := createOrApply(ctx, ri, obj, fieldOwner, existed)
		if err != nil {
			return nil, fmt.Errorf("apply %s/%s in %s: %w", gvk.Kind, obj.GetName(), obj.GetNamespace(), err)
		}
		// Lost-race fallthrough: object existed at Patch time, so re-stamp
		// the existed flag and re-fetch the pre-state generation so action
		// attribution doesn't lie.
		if raced {
			pre, _, getErr := preGetGeneration(ctx, ri, obj.GetName())
			if getErr == nil {
				oldGen, existed = pre, true
			}
		}

		results = append(results, ApplyResult{
			Kind:      gvk.Kind,
			Name:      applied.GetName(),
			Namespace: applied.GetNamespace(),
			Action:    actionFor(existed, oldGen, applied.GetGeneration()),
		})
	}
	return results, nil
}

func resourceClient(dyn dynamic.Interface, mapping *meta.RESTMapping, namespace string) dynamic.ResourceInterface {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return dyn.Resource(mapping.Resource).Namespace(namespace)
	}
	return dyn.Resource(mapping.Resource)
}

// requireNamespace fails closed with an actionable message if the
// engineer hasn't run `seictl onboard` yet — Apply does not auto-create
// namespaces, and the bare K8s 404 is not self-serve.
func requireNamespace(ctx context.Context, dyn dynamic.Interface, namespace string) error {
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	_, err := dyn.Resource(nsGVR).Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("namespace %q not found; run `seictl onboard --apply` to create it", namespace)
	}
	return fmt.Errorf("verify namespace %q: %w", namespace, err)
}

func preGetGeneration(ctx context.Context, ri dynamic.ResourceInterface, name string) (gen int64, existed bool, err error) {
	got, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return got.GetGeneration(), true, nil
}

// createOrApply does Create when the object is new and SSA Patch when
// it already exists. AlreadyExists from Create (lost race) falls
// through to Patch and signals raced=true so the caller can re-fetch
// the pre-state generation for accurate action attribution.
func createOrApply(ctx context.Context, ri dynamic.ResourceInterface, obj *unstructured.Unstructured, fieldOwner string, existed bool) (applied *unstructured.Unstructured, raced bool, err error) {
	if !existed {
		applied, err = ri.Create(ctx, obj, metav1.CreateOptions{FieldManager: fieldOwner})
		if err == nil {
			return applied, false, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, false, err
		}
		raced = true
	}
	body, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, raced, err
	}
	// Force ownership per LLD §Apply strategy. With field-manager
	// segregation (controller owns status, seictl-bench owns spec),
	// Force should never trigger in practice — if it does, an unexpected
	// spec writer exists and we've silently stolen ownership. Revisit
	// when that conflict surfaces.
	applied, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{
		FieldManager: fieldOwner,
		Force:        ptr.To(true),
	})
	return applied, raced, err
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
