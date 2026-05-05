package nodedeployment

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// kubeconfig wraps clientcmd's deferred loader so a single source
// honors $KUBECONFIG colon-merge, --kubeconfig override, in-cluster
// fallback (apiserver + SA namespace from /var/run/secrets), and
// kubectl's namespace precedence (override > context > "default").
type kubeconfig struct {
	cfg clientcmd.ClientConfig
}

// loadKubeconfig returns a deferred-loading client config. It does not
// read any files until ClientConfig() / Namespace() is called, so
// constructing one is cheap.
func loadKubeconfig(explicitPath, namespaceOverride string) *kubeconfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if explicitPath != "" {
		rules.ExplicitPath = explicitPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if namespaceOverride != "" {
		overrides.Context.Namespace = namespaceOverride
	}
	return &kubeconfig{
		cfg: clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides),
	}
}

func (k *kubeconfig) RESTConfig() (*rest.Config, error) {
	cfg, err := k.cfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return cfg, nil
}

func (k *kubeconfig) Namespace() (string, error) {
	ns, _, err := k.cfg.Namespace()
	if err != nil {
		return "", fmt.Errorf("resolve namespace: %w", err)
	}
	return ns, nil
}

// newClient constructs a controller-runtime client capable of
// server-side-applying unstructured.Unstructured objects with our GVK.
// No scheme registration is required for unstructured payloads — the
// runtime resolves GVK from the object itself.
func newClient(cfg *rest.Config) (client.Client, error) {
	c, err := client.New(cfg, client.Options{Scheme: runtime.NewScheme()})
	if err != nil {
		return nil, fmt.Errorf("build kube client: %w", err)
	}
	return c, nil
}
