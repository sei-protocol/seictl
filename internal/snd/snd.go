// Package snd is the seictl-side plumbing for SeiNodeDeployment custom
// resources. It pins the GVK, wraps server-side-apply with seictl's
// field manager, and provides constructor helpers for unstructured
// objects.
//
// The unstructured choice is deliberate: sei-k8s-controller imports
// github.com/sei-protocol/seictl/sidecar/client, so a typed import of
// sei-k8s-controller/api/v1alpha1 from seictl would create a Go module
// cycle. sei-protocol/sei-k8s-controller#175 tracks splitting the api
// surface into a leaf module; once that lands, this package is the
// natural place to introduce typed accessors.
package snd

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	Group      = "sei.io"
	Version    = "v1alpha1"
	Kind       = "SeiNodeDeployment"
	ListKind   = "SeiNodeDeploymentList"
	FieldOwner = client.FieldOwner("seictl")
)

var GVK = schema.GroupVersionKind{Group: Group, Version: Version, Kind: Kind}

// New returns an unstructured SeiNodeDeployment with the GVK pinned.
// Caller populates name, namespace, and spec.
func New() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(GVK)
	return obj
}

// Apply server-side-applies obj as the seictl FieldManager. With dryRun,
// the apiserver validates and returns the would-be-applied object
// without persisting.
//
// On success the obj passed in is mutated in place to match the
// apiserver's response (resourceVersion, defaulted fields, status).
func Apply(ctx context.Context, c client.Client, obj *unstructured.Unstructured, dryRun bool) error {
	obj.SetGroupVersionKind(GVK)
	opts := []client.PatchOption{client.ForceOwnership, FieldOwner}
	if dryRun {
		opts = append(opts, client.DryRunAll)
	}
	return c.Patch(ctx, obj, client.Apply, opts...)
}
