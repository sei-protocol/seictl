// Package snd is the seictl-side plumbing for SeiNodeDeployment custom
// resources. Once sei-protocol/sei-k8s-controller#175 splits the api
// surface into a leaf module, this package is the natural place to
// introduce typed accessors in place of the unstructured helpers below.
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

func New() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(GVK)
	return obj
}

// Apply server-side-applies obj as the seictl FieldManager, mutating
// obj in place to match the apiserver response.
func Apply(ctx context.Context, c client.Client, obj *unstructured.Unstructured, dryRun bool) error {
	obj.SetGroupVersionKind(GVK)
	opts := []client.PatchOption{client.ForceOwnership, FieldOwner}
	if dryRun {
		opts = append(opts, client.DryRunAll)
	}
	return c.Patch(ctx, obj, client.Apply, opts...)
}
