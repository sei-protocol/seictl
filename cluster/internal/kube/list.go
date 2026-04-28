package kube

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/resource"
)

// ListOptions selects what to list. Resources is a slice of resource
// names (plural) like "seinodedeployments", "jobs", "configmaps".
// LabelSelector follows the standard `key=value,key2=value2` form.
type ListOptions struct {
	Namespace     string
	Resources     []string
	LabelSelector string
	AllNamespaces bool
}

// List returns Unstructured objects across the given resource types
// filtered by labels. Errors short-circuit; partial results are not
// returned (mirrors Apply's all-or-nothing contract).
func (c *Client) List(ctx context.Context, opts ListOptions) ([]unstructured.Unstructured, error) {
	if c.flags == nil {
		return nil, errors.New("kube.Client has no ConfigFlags; constructed without cluster access")
	}
	if len(opts.Resources) == 0 {
		return nil, errors.New("ListOptions.Resources required")
	}

	b := resource.NewBuilder(c.flags).
		Unstructured().
		ContinueOnError().
		LabelSelectorParam(opts.LabelSelector).
		ResourceTypes(opts.Resources...).
		Flatten()

	if opts.AllNamespaces {
		b = b.AllNamespaces(true)
	} else {
		b = b.NamespaceParam(opts.Namespace).DefaultNamespace()
	}

	result := b.Do()
	if err := result.Err(); err != nil {
		return nil, wrapMappingErr(err)
	}

	var out []unstructured.Unstructured
	visitErr := result.Visit(func(info *resource.Info, vErr error) error {
		if vErr != nil {
			return wrapMappingErr(vErr)
		}
		u, ok := info.Object.(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected object type %T for %s/%s", info.Object, info.Namespace, info.Name)
		}
		out = append(out, *u)
		return nil
	})
	if visitErr != nil {
		return nil, wrapMappingErr(visitErr)
	}
	return out, nil
}
