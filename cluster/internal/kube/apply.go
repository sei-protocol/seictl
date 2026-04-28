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
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

// ApplyResult is what Apply emits per input document.
type ApplyResult struct {
	Kind      string
	Name      string
	Namespace string
	Action    string // "create" | "update" | "unchanged"
}

// Applier abstracts the apply pipeline so callers (tests, prod) can
// swap the dynamic-client + REST-mapper construction.
type Applier interface {
	Apply(ctx context.Context, fieldOwner string, docs [][]byte) ([]ApplyResult, error)
}

// Apply does server-side apply on each input doc with the given field
// owner. Pre-Get + post-Patch resourceVersion comparison distinguishes
// create / update / unchanged. Failures abort and return the partial
// result list so callers can surface what landed before the break.
func (c *Client) Apply(ctx context.Context, fieldOwner string, docs [][]byte) ([]ApplyResult, error) {
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
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))
	return ApplyWith(ctx, dyn, mapper, fieldOwner, docs)
}

// ApplyWith is the seam tests inject through. Production callers go
// through Client.Apply.
func ApplyWith(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, fieldOwner string, docs [][]byte) ([]ApplyResult, error) {
	results := make([]ApplyResult, 0, len(docs))
	for _, raw := range docs {
		obj := &unstructured.Unstructured{}
		if err := yamlutil.Unmarshal(raw, &obj.Object); err != nil {
			return results, fmt.Errorf("parse manifest: %w", err)
		}
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return results, fmt.Errorf("rest mapping for %s: %w", gvk, err)
		}
		ri := resourceClient(dyn, mapping, obj.GetNamespace())

		oldRV, existed, err := preGetResourceVersion(ctx, ri, obj.GetName())
		if err != nil {
			return results, fmt.Errorf("pre-get %s/%s: %w", gvk.Kind, obj.GetName(), err)
		}

		applied, err := createOrApply(ctx, ri, obj, fieldOwner, existed)
		if err != nil {
			return results, fmt.Errorf("apply %s/%s: %w", gvk.Kind, obj.GetName(), err)
		}

		results = append(results, ApplyResult{
			Kind:      gvk.Kind,
			Name:      applied.GetName(),
			Namespace: applied.GetNamespace(),
			Action:    actionFor(existed, oldRV, applied.GetResourceVersion()),
		})
	}
	return results, nil
}

// createOrApply does Create when the object is new and SSA Patch when
// it already exists. AlreadyExists from Create (lost race) falls through
// to Patch. Field manager is set on both paths so managed-fields
// ownership is consistent across the lifetime of the object.
func createOrApply(ctx context.Context, ri dynamic.ResourceInterface, obj *unstructured.Unstructured, fieldOwner string, existed bool) (*unstructured.Unstructured, error) {
	if !existed {
		created, err := ri.Create(ctx, obj, metav1.CreateOptions{FieldManager: fieldOwner})
		if err == nil {
			return created, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
	}
	body, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, err
	}
	return ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{
		FieldManager: fieldOwner,
		Force:        ptrBool(true),
	})
}

func resourceClient(dyn dynamic.Interface, mapping *meta.RESTMapping, namespace string) dynamic.ResourceInterface {
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return dyn.Resource(mapping.Resource).Namespace(namespace)
	}
	return dyn.Resource(mapping.Resource)
}

func preGetResourceVersion(ctx context.Context, ri dynamic.ResourceInterface, name string) (rv string, existed bool, err error) {
	got, err := ri.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return got.GetResourceVersion(), true, nil
}

func actionFor(existed bool, oldRV, newRV string) string {
	switch {
	case !existed:
		return "create"
	case oldRV == newRV:
		return "unchanged"
	default:
		return "update"
	}
}

func ptrBool(b bool) *bool { return &b }
