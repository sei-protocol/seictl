// Package kube wraps kubeconfig loading for cluster-facing seictl
// commands. Client holds a *genericclioptions.ConfigFlags so it
// satisfies cli-runtime's RESTClientGetter natively — Builder, Helper,
// and discovery cache all consume it directly without adapter shims.
package kube

import (
	"os"
	"sort"
	"strings"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

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

// inClusterContextName is the synthetic context label used when a
// Client is constructed from rest.InClusterConfig() rather than a
// kubeconfig file. Echoed in the JSON envelope alongside ClusterServer.
const inClusterContextName = "in-cluster"

// Vars (not consts) so tests can stub the in-cluster surface.
var (
	inClusterConfigFn      = rest.InClusterConfig
	inClusterNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// New resolves kubeconfig context + namespace into a Client. Errors
// in this layer are kubeconfig-shape problems and map to ExitIdentity /
// CatKubeconfigParse, not cluster reachability.
//
// When no kubeconfig file is present and no --context override is given,
// New falls back to rest.InClusterConfig() so seictl can run from
// inside a pod with a projected ServiceAccount token. The synthetic
// Client uses ContextName="in-cluster"; ClusterServer is the apiserver
// URL from the in-cluster config.
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
		// No kubeconfig context and no override — try in-cluster auth.
		// ConfigFlags.ToRESTConfig() falls back to in-cluster on its own
		// for resource.Builder, so all we need to synthesize here are
		// the descriptive fields echoed into the JSON envelope.
		return newInClusterClient(cf, opts)
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

func newInClusterClient(cf *genericclioptions.ConfigFlags, opts Options) (*Client, *clioutput.Error) {
	cfg, err := inClusterConfigFn()
	if err != nil {
		return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"no current-context in kubeconfig and not running in a pod with a ServiceAccount")
	}

	ns := opts.Namespace
	if ns == "" {
		if data, ferr := os.ReadFile(inClusterNamespaceFile); ferr == nil {
			ns = strings.TrimSpace(string(data))
		}
	}
	if ns == "" {
		ns = "default"
	}

	return &Client{
		ContextName:   inClusterContextName,
		ClusterName:   inClusterContextName,
		ClusterServer: cfg.Host,
		Namespace:     ns,
		flags:         cf,
	}, nil
}

// RESTClientGetter exposes the underlying ConfigFlags for callers that
// need to feed cli-runtime APIs (resource.Builder, etc.) directly.
func (c *Client) RESTClientGetter() genericclioptions.RESTClientGetter {
	return c.flags
}
