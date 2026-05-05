package nodedeployment

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// kubeconfig honors --kubeconfig, $KUBECONFIG colon-merge, in-cluster
// fallback, and kubectl namespace precedence (override > context >
// "default") through a single deferred loader.
type kubeconfig struct {
	cfg clientcmd.ClientConfig
}

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

// newClient builds a controller-runtime client for unstructured SSA;
// no scheme registration needed since GVK is read off the object.
func newClient(cfg *rest.Config) (client.Client, error) {
	c, err := client.New(cfg, client.Options{Scheme: runtime.NewScheme()})
	if err != nil {
		return nil, fmt.Errorf("build kube client: %w", err)
	}
	return c, nil
}
