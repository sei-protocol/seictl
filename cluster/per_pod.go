package cluster

import (
	"context"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

// PerPodEndpoint is one child SeiNode's headless Service handle, ready
// to dial. Field shape mirrors Endpoints' JSON style (evmJsonRpc, evmWs)
// so consumers can swap from the aggregate to per-pod URLs without
// renaming keys. URLs use cluster-internal DNS — pod IPs are not
// exposed because they are ephemeral.
type PerPodEndpoint struct {
	Name       string `json:"name"`
	EvmJsonRpc string `json:"evmJsonRpc"`
	EvmWs      string `json:"evmWs"`
}

const (
	perPodPollTimeout  = 30 * time.Second
	perPodPollInterval = 1 * time.Second
)

// perPodPoller is the dependency-injection seam: production reads from
// the cluster, tests inject a fixed result. Returns nil when the
// controller hasn't published status.perPodServices in time — perPod is
// best-effort and never blocks the apply path.
type perPodPoller func(ctx context.Context, kc *kube.Client, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint

// pollPerPodFromCluster polls SND.status.perPodServices until it
// reports `replicas` entries or perPodPollTimeout fires. Best-effort:
// timeouts emit a warning to `warn` and return nil; the caller emits an
// envelope without the perPod field.
func pollPerPodFromCluster(ctx context.Context, kc *kube.Client, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint {
	deadline := time.Now().Add(perPodPollTimeout)
	var lastErr error
	for {
		u, err := kc.GetSND(ctx, namespace, sndName)
		switch {
		case err != nil:
			lastErr = err
		case u != nil:
			out := parsePerPodServices(u)
			if len(out) >= replicas {
				return out
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				fmt.Fprintf(warn, "warning: per-pod URL resolution timed out after %s (last error: %v); 'perPod' omitted from envelope\n", perPodPollTimeout, lastErr)
			} else {
				fmt.Fprintf(warn, "warning: per-pod URL resolution timed out after %s waiting for SND %q status.perPodServices to report %d entries; 'perPod' omitted from envelope\n", perPodPollTimeout, sndName, replicas)
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

// parsePerPodServices converts the controller's status.perPodServices
// slice into dial-ready URLs. Order is preserved from the controller
// (which sorts by ordinal). Malformed entries are dropped.
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
			EvmJsonRpc: fmt.Sprintf("http://%s.%s.svc:%d", name, ns, evmHTTP),
			EvmWs:      fmt.Sprintf("ws://%s.%s.svc:%d", name, ns, evmWS),
		})
	}
	return out
}
