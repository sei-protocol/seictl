package cluster

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func sndWithPerPodServices(entries []map[string]any) *unstructured.Unstructured {
	raw := make([]any, len(entries))
	for i, e := range entries {
		raw[i] = e
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"perPodServices": raw,
		},
	}}
}

func entry(name, ns string, evmHTTP, evmWS int64) map[string]any {
	return map[string]any{
		"name":      name,
		"namespace": ns,
		"ports": map[string]any{
			"evmHttp": evmHTTP,
			"evmWs":   evmWS,
		},
	}
}

func TestParsePerPodServices(t *testing.T) {
	t.Run("nil unstructured", func(t *testing.T) {
		if got := parsePerPodServices(nil); got != nil {
			t.Errorf("nil input → got %v, want nil", got)
		}
	})

	t.Run("missing status field", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		if got := parsePerPodServices(u); got != nil {
			t.Errorf("no status → got %v, want nil", got)
		}
	})

	t.Run("missing perPodServices", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{"phase": "Ready"},
		}}
		if got := parsePerPodServices(u); got != nil {
			t.Errorf("no perPodServices → got %v, want nil", got)
		}
	})

	t.Run("preserves controller order", func(t *testing.T) {
		u := sndWithPerPodServices([]map[string]any{
			entry("pacific-1-rpc-primary-0", "nightly", 8545, 8546),
			entry("pacific-1-rpc-primary-1", "nightly", 8545, 8546),
			entry("pacific-1-rpc-primary-2", "nightly", 8545, 8546),
		})
		got := parsePerPodServices(u)
		want := []PerPodEndpoint{
			{Name: "pacific-1-rpc-primary-0", EvmJsonRpc: "http://pacific-1-rpc-primary-0.nightly.svc:8545", EvmWs: "ws://pacific-1-rpc-primary-0.nightly.svc:8546"},
			{Name: "pacific-1-rpc-primary-1", EvmJsonRpc: "http://pacific-1-rpc-primary-1.nightly.svc:8545", EvmWs: "ws://pacific-1-rpc-primary-1.nightly.svc:8546"},
			{Name: "pacific-1-rpc-primary-2", EvmJsonRpc: "http://pacific-1-rpc-primary-2.nightly.svc:8545", EvmWs: "ws://pacific-1-rpc-primary-2.nightly.svc:8546"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parsed entries:\ngot  %+v\nwant %+v", got, want)
		}
	})

	t.Run("drops malformed entries", func(t *testing.T) {
		u := sndWithPerPodServices([]map[string]any{
			entry("ok-0", "ns", 8545, 8546),
			{"name": "missing-ns", "ports": map[string]any{"evmHttp": int64(8545), "evmWs": int64(8546)}},
			{"name": "no-ports", "namespace": "ns"},
			entry("ok-1", "ns", 8545, 8546),
		})
		got := parsePerPodServices(u)
		if len(got) != 2 {
			t.Fatalf("expected 2 valid entries, got %d: %+v", len(got), got)
		}
		if got[0].Name != "ok-0" || got[1].Name != "ok-1" {
			t.Errorf("unexpected entries: %+v", got)
		}
	})

	t.Run("partial — caller treats len mismatch as 'keep polling'", func(t *testing.T) {
		// Controller may publish only some children mid-rollout. Parser
		// returns what's there; the poller decides whether to continue.
		u := sndWithPerPodServices([]map[string]any{
			entry("a-0", "ns", 8545, 8546),
		})
		got := parsePerPodServices(u)
		if len(got) != 1 {
			t.Errorf("partial: got %d entries, want 1", len(got))
		}
	})
}

