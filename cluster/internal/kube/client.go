// Package kube wraps kubeconfig loading for cluster-facing seictl
// commands. Client holds a *genericclioptions.ConfigFlags so it
// satisfies cli-runtime's RESTClientGetter natively — Builder, Helper,
// and discovery cache all consume it directly without adapter shims.
package kube

import (
	"sort"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

type Options struct {
	// Kubeconfig is an explicit kubeconfig path. Empty falls back to
	// the KUBECONFIG env var, then ~/.kube/config.
	Kubeconfig string
	// Context is the kubeconfig context name to use. Empty falls back
	// to the kubeconfig's current-context.
	Context string
	// Namespace overrides the context's namespace. Empty falls back to
	// the context's namespace, then "default".
	Namespace string
}

type Client struct {
	ContextName   string
	ClusterName   string
	ClusterServer string
	Namespace     string

	// flags is the cli-runtime config source — also a RESTClientGetter
	// for resource.Builder. Unexported because callers go through
	// methods on Client, not raw cli-runtime.
	flags *genericclioptions.ConfigFlags
}

// New resolves kubeconfig context + namespace into a Client. Errors
// in this layer are kubeconfig-shape problems and map to ExitIdentity /
// CatKubeconfigParse, not cluster reachability.
func New(opts Options) (*Client, *clioutput.Error) {
	cf := genericclioptions.NewConfigFlags(true)
	// Leave the pointers nil when the caller didn't override, so
	// ConfigFlags' default loader honors KUBECONFIG / current-context /
	// the context's namespace as expected.
	if opts.Kubeconfig != "" {
		cf.KubeConfig = &opts.Kubeconfig
	} else {
		cf.KubeConfig = nil
	}
	if opts.Context != "" {
		cf.Context = &opts.Context
	} else {
		cf.Context = nil
	}
	if opts.Namespace != "" {
		cf.Namespace = &opts.Namespace
	} else {
		cf.Namespace = nil
	}

	loader := cf.ToRawKubeConfigLoader()
	raw, err := loader.RawConfig()
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"load kubeconfig: %v", err)
	}

	contextName := raw.CurrentContext
	if opts.Context != "" {
		contextName = opts.Context
	}
	if contextName == "" {
		return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"no current-context in kubeconfig and no --context provided")
	}
	kctx, ok := raw.Contexts[contextName]
	if !ok {
		available := make([]string, 0, len(raw.Contexts))
		for k := range raw.Contexts {
			available = append(available, k)
		}
		sort.Strings(available)
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"context %q not found in kubeconfig (available: %v)", contextName, available)
	}
	cluster, ok := raw.Clusters[kctx.Cluster]
	if !ok {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"cluster %q (referenced by context %q) not found in kubeconfig", kctx.Cluster, contextName)
	}

	ns := opts.Namespace
	if ns == "" {
		ns = kctx.Namespace
	}
	if ns == "" {
		ns = "default"
	}

	return &Client{
		ContextName:   contextName,
		ClusterName:   kctx.Cluster,
		ClusterServer: cluster.Server,
		Namespace:     ns,
		flags:         cf,
	}, nil
}

// RESTClientGetter exposes the underlying ConfigFlags for callers that
// need to feed cli-runtime APIs (resource.Builder, etc.) directly.
func (c *Client) RESTClientGetter() genericclioptions.RESTClientGetter {
	return c.flags
}
