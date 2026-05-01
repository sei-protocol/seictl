package cluster

import (
	"context"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

// PerPodEndpoint addresses one child SeiNode via its headless Service.
// TendermintRpc is intentionally absent — the controller's
// PerPodServicePorts exposes only EVM HTTP/WS today
// (sei-k8s-controller#156). Slice order is by pod ordinal ascending.
type PerPodEndpoint struct {
	Name       string `json:"name"`
	EvmJsonRpc string `json:"evmJsonRpc"`
	EvmWs      string `json:"evmWs"`
}

// var (not const) so tests can shrink the deadline.
var (
	perPodPollTimeout  = 30 * time.Second
	perPodPollInterval = 1 * time.Second
)

type perPodPoller func(ctx context.Context, kc *kube.Client, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint

type sndGetter func(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)

func pollPerPodFromCluster(ctx context.Context, kc *kube.Client, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint {
	return pollPerPod(ctx, func(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
		return kc.GetSND(ctx, ns, name)
	}, namespace, sndName, replicas, warn)
}

// pollPerPod is best-effort: timeout returns nil + warning, never fails
// the apply. Terminal errors (Forbidden, Unauthorized, no CRD match)
// short-circuit; NotFound is transient since apply just landed.
func pollPerPod(ctx context.Context, getSND sndGetter, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint {
	deadline := time.Now().Add(perPodPollTimeout)
	var lastErr error
	for {
		u, err := getSND(ctx, namespace, sndName)
		switch {
		case err != nil:
			if isTerminalError(err) {
				fmt.Fprintf(warn, "warning: per-pod URL resolution failed: %v; 'perPod' omitted from envelope\n", err)
				return nil
			}
			lastErr = err
		case u != nil:
			if isPerPodReady(u, replicas) {
				return parsePerPodServices(u)
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				fmt.Fprintf(warn, "warning: per-pod URL resolution timed out after %s (last error: %v); 'perPod' omitted from envelope\n", perPodPollTimeout, lastErr)
			} else {
				fmt.Fprintf(warn, "warning: per-pod URL resolution timed out after %s waiting for SND %q status.perPodServices to report %d entries at the latest generation; 'perPod' omitted from envelope\n", perPodPollTimeout, sndName, replicas)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(perPodPollInterval):
		}
	}
}

// isPerPodReady requires both len==replicas AND observedGeneration==
// metadata.generation. The observedGen guard closes a scale-down race
// where stale entries from the prior generation are still published.
func isPerPodReady(u *unstructured.Unstructured, replicas int) bool {
	observed, found, err := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if err != nil || !found {
		return false
	}
	if observed != u.GetGeneration() {
		return false
	}
	raw, found, err := unstructured.NestedSlice(u.Object, "status", "perPodServices")
	if err != nil || !found {
		return false
	}
	return len(raw) == replicas
}

// NotFound on the SND is *not* terminal — apply just landed, client
// cache may trail.
func isTerminalError(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return true
	}
	if apimeta.IsNoMatchError(err) {
		return true
	}
	return false
}

func parsePerPodServices(u *unstructured.Unstructured) []PerPodEndpoint {
	if u == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(u.Object, "status", "perPodServices")
	if !found || err != nil {
		return nil
	}
	out := make([]PerPodEndpoint, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "name")
		ns, _, _ := unstructured.NestedString(m, "namespace")
		evmHTTP, _, _ := unstructured.NestedInt64(m, "ports", "evmHttp")
		evmWS, _, _ := unstructured.NestedInt64(m, "ports", "evmWs")
		if name == "" || ns == "" || evmHTTP == 0 || evmWS == 0 {
			continue
		}
		out = append(out, PerPodEndpoint{
			Name:       name,
			EvmJsonRpc: fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, ns, evmHTTP),
			EvmWs:      fmt.Sprintf("ws://%s.%s.svc.cluster.local:%d", name, ns, evmWS),
		})
	}
	return out
}
