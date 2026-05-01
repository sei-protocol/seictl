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

// PerPodEndpoint is one child SeiNode's headless Service handle, ready
// to dial. URLs use cluster-internal DNS (pod IPs are ephemeral and
// excluded from the controller's status).
//
// TendermintRpc is intentionally omitted: the controller's
// PerPodServicePorts exposes only evmHttp/evmWs (sei-k8s-controller#156).
// Add when a consumer asks.
//
// Order in the parent slice is by pod ordinal ascending — preserved
// from the controller's status.perPodServices.
type PerPodEndpoint struct {
	Name       string `json:"name"`
	EvmJsonRpc string `json:"evmJsonRpc"`
	EvmWs      string `json:"evmWs"`
}

// Vars rather than consts so tests can shrink the deadline for fast
// poll-loop coverage without a real clock.
var (
	perPodPollTimeout  = 30 * time.Second
	perPodPollInterval = 1 * time.Second
)

// perPodPoller is the dependency-injection seam used by rpc/bench up.
// Returns nil on timeout or terminal apiserver error; per-pod URLs are
// best-effort and never block the apply path.
type perPodPoller func(ctx context.Context, kc *kube.Client, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint

// sndGetter is the unit-testable seam: the pure poll loop calls this
// instead of touching kube.Client directly.
type sndGetter func(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)

// pollPerPodFromCluster wires kc.GetSND into the pure pollPerPod loop.
func pollPerPodFromCluster(ctx context.Context, kc *kube.Client, namespace, sndName string, replicas int, warn io.Writer) []PerPodEndpoint {
	return pollPerPod(ctx, func(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
		return kc.GetSND(ctx, ns, name)
	}, namespace, sndName, replicas, warn)
}

// pollPerPod waits for the SND's status.perPodServices to publish
// exactly `replicas` entries at status.observedGeneration ==
// metadata.generation. The observed-gen check guards against stale
// entries during scale-down where the controller has not yet pruned
// the prior generation's entries.
//
// Terminal errors (Forbidden, Unauthorized, no CRD match) short-circuit
// with a warning. NotFound on the SND itself is treated as transient —
// the apply just landed and the engineer's client cache may not have
// caught up. Timeout returns nil + warning; the apply itself is never
// failed because per-pod URLs lagged.
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

// isPerPodReady checks both shape and freshness: status.perPodServices
// has exactly `replicas` entries AND status.observedGeneration matches
// metadata.generation. Either condition alone is insufficient — a
// scale-down race can leave a stale longer slice published against an
// older observedGeneration.
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

// isTerminalError marks apiserver errors that retrying will not fix:
// missing/wrong CRD version, RBAC denial. NotFound on the SND object
// is *not* terminal — apply just landed, client cache may trail.
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

// parsePerPodServices converts the controller's status.perPodServices
// slice into dial-ready URLs. Order is preserved from the controller
// (sorted by ordinal). Malformed entries are dropped.
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
