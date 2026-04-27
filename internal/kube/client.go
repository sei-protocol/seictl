// Package kube wraps kubeconfig loading + REST config construction
// behind a single Client used by every cluster-facing seictl command.
//
// Slice 2 of docs/design/cluster-cli.md: only the read paths needed for
// `seictl context`. The controller-runtime client is added in the bench
// slice when SSA / Unstructured CRUD is actually exercised — keeping
// this package minimal until that need lands.
package kube

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/sei-protocol/seictl/internal/clioutput"
)

type Options struct {
	// Kubeconfig is an explicit kubeconfig path. Empty falls back to the
	// KUBECONFIG env var, then ~/.kube/config.
	Kubeconfig string
	// Context is the kubeconfig context name to use. Empty falls back to
	// the kubeconfig's current-context.
	Context string
	// Namespace overrides the context's namespace. Empty falls back to
	// the context's namespace, then "default".
	Namespace string
}

type Client struct {
	RESTConfig    *rest.Config
	RawConfig     clientcmdapi.Config
	ContextName   string
	ClusterName   string
	ClusterServer string
	Namespace     string
}

// New resolves kubeconfig + context + namespace into a Client. Errors
// in this layer are kubeconfig-shape problems — they map to
// ExitIdentity / CatKubeconfigParse, not cluster reachability.
func New(opts Options) (*Client, *clioutput.Error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loadingRules.ExplicitPath = opts.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if opts.Context != "" {
		overrides.CurrentContext = opts.Context
	}
	if opts.Namespace != "" {
		overrides.Context.Namespace = opts.Namespace
	}

	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	raw, err := cfg.RawConfig()
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"load kubeconfig: %v", err)
	}

	contextName := overrides.CurrentContext
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName == "" {
		return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"no current-context in kubeconfig and no --context provided")
	}
	kctx, ok := raw.Contexts[contextName]
	if !ok {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"context %q not found in kubeconfig", contextName)
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

	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatKubeconfigParse,
			"build REST config: %v", err)
	}

	return &Client{
		RESTConfig:    restCfg,
		RawConfig:     raw,
		ContextName:   contextName,
		ClusterName:   kctx.Cluster,
		ClusterServer: cluster.Server,
		Namespace:     ns,
	}, nil
}
