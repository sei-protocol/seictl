package task

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sei-protocol/seictl/internal/cliutil"
	sidecar "github.com/sei-protocol/seictl/sidecar/client"
)

// sidecarProxyPort is the in-pod kube-rbac-proxy port the task verbs address,
// not the sidecar's own loopback :7777. The proxy runs TokenReview + SAR and
// forwards passed requests to 127.0.0.1:7777.
const sidecarProxyPort int32 = 8443

// requestTimeout bounds a single sidecar HTTP call. Every /v0/tasks call is
// sidecar-local and cheap (submit returns immediately; the task runs async), so
// a hung connection fails fast rather than stalling a poll iteration.
const requestTimeout = 10 * time.Second

// Discovery labels for snapshot-upload target selection. The publish label
// ships in a separate controller change; until it lands, discovery selects
// nothing and --node is the working path.
const (
	labelSnapshotPublish = "sei.io/snapshot-publish"
	labelChain           = "sei.io/chain"
)

// resolveKube resolves the cluster connection and effective namespace from
// --kubeconfig / -n exactly as the workflow and node trees do.
func resolveKube(c *cli.Command) (*rest.Config, string, error) {
	kc := cliutil.LoadKubeconfig(c.String("kubeconfig"), c.String("namespace"))
	cfg, err := kc.RESTConfig()
	if err != nil {
		return nil, "", err
	}
	ns, err := kc.Namespace()
	if err != nil {
		return nil, "", err
	}
	return cfg, ns, nil
}

// newSidecarClient builds a SidecarClient targeting one pod's proxy, carrying
// the resolved kube identity as an Authorization: Bearer header so the proxy's
// TokenReview passes. It reuses the established NewSidecarClientFromPodDNS
// addressing (http scheme, pod headless-service DNS) rather than inventing a
// new client path.
func newSidecarClient(cfg *rest.Config, ns, node string, port int32) (*sidecar.SidecarClient, error) {
	token, err := bearerToken(cfg)
	if err != nil {
		return nil, err
	}
	doer := &bearerDoer{
		inner: &http.Client{Timeout: requestTimeout},
		token: token,
	}
	return sidecar.NewSidecarClientFromPodDNS(node, ns, port, sidecar.WithHTTPDoer(doer))
}

// bearerDoer injects the resolved kube bearer token on every request so the
// in-pod kube-rbac-proxy can authenticate the caller. An empty token (a
// cert-based kubeconfig, or an unauthenticated-mode sidecar) sends no header.
type bearerDoer struct {
	inner sidecar.HttpRequestDoer
	token string
}

func (d *bearerDoer) Do(req *http.Request) (*http.Response, error) {
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	return d.inner.Do(req)
}

// bearerToken extracts the bearer token from the resolved rest.Config: the
// inline token first, then the token file an in-cluster SA config points at.
func bearerToken(cfg *rest.Config) (string, error) {
	if cfg.BearerToken != "" {
		return cfg.BearerToken, nil
	}
	if cfg.BearerTokenFile != "" {
		b, err := os.ReadFile(cfg.BearerTokenFile)
		if err != nil {
			return "", fmt.Errorf("read bearer token file %s: %w", cfg.BearerTokenFile, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", nil
}

// discoverPublishNode label-selects snapshot-publish pods for the chain and
// returns a random one's node name. It is the discovery half of
// snapshot-upload's target selection; --node bypasses it.
func discoverPublishNode(ctx context.Context, cfg *rest.Config, ns, chain string) (string, error) {
	kcli, err := cliutil.NewClient(cfg)
	if err != nil {
		return "", err
	}
	pods := &unstructured.UnstructuredList{}
	pods.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "PodList"})
	err = kcli.List(ctx, pods,
		client.InNamespace(ns),
		client.MatchingLabels{labelSnapshotPublish: "true", labelChain: chain},
	)
	if err != nil {
		return "", fmt.Errorf("list snapshot-publish pods for chain %s in %s: %w", chain, ns, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods match %s=true,%s=%s in %s (discovery needs the controller's publish-label change; use --node until it lands)",
			labelSnapshotPublish, labelChain, chain, ns)
	}
	pod := pods.Items[rand.IntN(len(pods.Items))].GetName()
	return nodeFromPod(pod), nil
}

// nodeFromPod derives a SeiNode / headless-service name from a StatefulSet pod
// name by stripping the trailing ordinal. Publish nodes are single-replica, so
// the pod is always <node>-0 and the sidecar resolves at <node>-0.<node>.<ns>.
func nodeFromPod(pod string) string {
	i := strings.LastIndex(pod, "-")
	if i < 0 {
		return pod
	}
	if _, err := strconv.Atoi(pod[i+1:]); err != nil {
		return pod
	}
	return pod[:i]
}
