package kube

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/utils/ptr"
)

// DeleteResult is what Delete emits per matched object.
type DeleteResult struct {
	Kind      string
	Name      string
	Namespace string
	Action    string // "deleted" | "not-found" | "still-terminating"
}

// DeleteOptions mirrors ListOptions — same selector + resource types,
// but every match is deleted with foreground propagation.
type DeleteOptions struct {
	Namespace     string
	Resources     []string
	LabelSelector string
}

// Delete tears down all objects matching the label selector across the
// requested resource types. DeletePropagationForeground ensures children
// (e.g., StatefulSet pods owned by a SeiNodeDeployment) cascade in order.
//
// Returns one DeleteResult per matched object. NotFound is treated as
// success ("already gone"). The caller is responsible for the
// finalizer-stuck timeout policy (LLD §Landmine).
func (c *Client) Delete(ctx context.Context, opts DeleteOptions) ([]DeleteResult, error) {
	if c.flags == nil {
		return nil, errors.New("kube.Client has no ConfigFlags; constructed without cluster access")
	}
	if len(opts.Resources) == 0 {
		return nil, errors.New("DeleteOptions.Resources required")
	}

	result := resource.NewBuilder(c.flags).
		Unstructured().
		ContinueOnError().
		NamespaceParam(opts.Namespace).DefaultNamespace().
		LabelSelectorParam(opts.LabelSelector).
		ResourceTypes(opts.Resources...).
		Flatten().
		Do()
	if err := result.Err(); err != nil {
		return nil, wrapMappingErr(err)
	}

	var results []DeleteResult
	visitErr := result.Visit(func(info *resource.Info, vErr error) error {
		if vErr != nil {
			return wrapMappingErr(vErr)
		}
		helper := resource.NewHelper(info.Client, info.Mapping)
		_, err := helper.DeleteWithOptions(info.Namespace, info.Name, &metav1.DeleteOptions{
			PropagationPolicy: ptr.To(metav1.DeletePropagationForeground),
		})

		action := "deleted"
		if err != nil {
			if apierrors.IsNotFound(err) {
				action = "not-found"
			} else {
				return fmt.Errorf("delete %s/%s in %s: %w", info.Mapping.GroupVersionKind.Kind, info.Name, info.Namespace, err)
			}
		}
		results = append(results, DeleteResult{
			Kind:      info.Mapping.GroupVersionKind.Kind,
			Name:      info.Name,
			Namespace: info.Namespace,
			Action:    action,
		})
		return nil
	})
	if visitErr != nil {
		return nil, wrapMappingErr(visitErr)
	}
	return results, nil
}
