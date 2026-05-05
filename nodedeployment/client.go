package nodedeployment

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// loadConfig follows kubeconfig precedence: --kubeconfig flag,
// $KUBECONFIG, then $HOME/.kube/config. In-cluster config takes over
// when no kubeconfig is locatable AND ServiceAccount tokens are
// mounted, matching kubectl's behavior.
func loadConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv("KUBECONFIG")
	}
	if kubeconfigPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(home, ".kube", "config")
			if _, err := os.Stat(candidate); err == nil {
				kubeconfigPath = candidate
			}
		}
	}
	if kubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	return rest.InClusterConfig()
}

// resolveNamespace honors -n / --namespace if set, otherwise reads the
// kubeconfig context's default. Falls back to "default" when neither is
// set or when running in-cluster without a namespace hint.
func resolveNamespace(kubeconfigPath, override string) string {
	if override != "" {
		return override
	}
	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv("KUBECONFIG")
	}
	if kubeconfigPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(home, ".kube", "config")
			if _, err := os.Stat(candidate); err == nil {
				kubeconfigPath = candidate
			}
		}
	}
	if kubeconfigPath != "" {
		cfg, err := clientcmd.LoadFromFile(kubeconfigPath)
		if err == nil && cfg.CurrentContext != "" {
			if ctx, ok := cfg.Contexts[cfg.CurrentContext]; ok && ctx.Namespace != "" {
				return ctx.Namespace
			}
		}
	}
	return "default"
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
