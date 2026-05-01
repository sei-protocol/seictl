package cluster

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func sndReady(generation int64, replicas int) *unstructured.Unstructured {
	entries := make([]any, replicas)
	for i := 0; i < replicas; i++ {
		entries[i] = entry("chain-rpc-"+strconv.Itoa(i), "ns", 8545, 8546)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"generation": generation,
		},
		"status": map[string]any{
			"observedGeneration": generation,
			"perPodServices":     entries,
		},
	}}
}

func sndStaleGeneration(metaGen, observedGen int64, replicas int) *unstructured.Unstructured {
	u := sndReady(metaGen, replicas)
	_ = unstructured.SetNestedField(u.Object, observedGen, "status", "observedGeneration")
	return u
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

	t.Run("preserves controller order with .svc.cluster.local URLs", func(t *testing.T) {
		u := sndWithPerPodServices([]map[string]any{
			entry("pacific-1-rpc-primary-0", "nightly", 8545, 8546),
			entry("pacific-1-rpc-primary-1", "nightly", 8545, 8546),
			entry("pacific-1-rpc-primary-2", "nightly", 8545, 8546),
		})
		got := parsePerPodServices(u)
		want := []PerPodEndpoint{
			{Name: "pacific-1-rpc-primary-0", EvmJsonRpc: "http://pacific-1-rpc-primary-0.nightly.svc.cluster.local:8545", EvmWs: "ws://pacific-1-rpc-primary-0.nightly.svc.cluster.local:8546"},
			{Name: "pacific-1-rpc-primary-1", EvmJsonRpc: "http://pacific-1-rpc-primary-1.nightly.svc.cluster.local:8545", EvmWs: "ws://pacific-1-rpc-primary-1.nightly.svc.cluster.local:8546"},
			{Name: "pacific-1-rpc-primary-2", EvmJsonRpc: "http://pacific-1-rpc-primary-2.nightly.svc.cluster.local:8545", EvmWs: "ws://pacific-1-rpc-primary-2.nightly.svc.cluster.local:8546"},
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
}

func TestIsPerPodReady(t *testing.T) {
	t.Run("ready when len==replicas and observedGen==metaGen", func(t *testing.T) {
		if !isPerPodReady(sndReady(7, 3), 3) {
			t.Errorf("ready SND should be ready")
		}
	})

	t.Run("stale generation: observedGen < metaGen → not ready", func(t *testing.T) {
		// Scale-down race: stale entries from the prior generation would falsely match without the observedGen guard.
		if isPerPodReady(sndStaleGeneration(8, 7, 3), 3) {
			t.Errorf("stale generation SND must not be ready")
		}
	})

	t.Run("len mismatch: too few entries", func(t *testing.T) {
		if isPerPodReady(sndReady(1, 2), 3) {
			t.Errorf("len(perPodServices)=2 vs replicas=3 must not be ready")
		}
	})

	t.Run("len mismatch: too many entries", func(t *testing.T) {
		if isPerPodReady(sndReady(1, 5), 3) {
			t.Errorf("len(perPodServices)=5 vs replicas=3 must not be ready")
		}
	})

	t.Run("no observedGeneration field → not ready", func(t *testing.T) {
		u := sndWithPerPodServices([]map[string]any{entry("a-0", "ns", 8545, 8546)})
		if isPerPodReady(u, 1) {
			t.Errorf("missing observedGeneration must not be ready")
		}
	})
}

func TestIsTerminalError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		terminal bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("connection reset"), false},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: "sei.io", Resource: "seinodedeployments"}, "x", errors.New("rbac")), true},
		{"unauthorized", apierrors.NewUnauthorized("token expired"), true},
		{"not-found (transient)", apierrors.NewNotFound(schema.GroupResource{Group: "sei.io", Resource: "seinodedeployments"}, "x"), false},
		{"no-match (CRD missing)", &apimeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "sei.io", Kind: "SeiNodeDeployment"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTerminalError(tc.err); got != tc.terminal {
				t.Errorf("isTerminalError(%v) = %v, want %v", tc.err, got, tc.terminal)
			}
		})
	}
}

func withFastPolling(t *testing.T, timeout, interval time.Duration) {
	t.Helper()
	oldTimeout := perPodPollTimeout
	oldInterval := perPodPollInterval
	perPodPollTimeout = timeout
	perPodPollInterval = interval
	t.Cleanup(func() {
		perPodPollTimeout = oldTimeout
		perPodPollInterval = oldInterval
	})
}

func TestPollPerPod_TerminalErrorShortCircuits(t *testing.T) {
	// 30s timeout but test must complete in ms; proves short-circuit fires.
	withFastPolling(t, 30*time.Second, 10*time.Millisecond)
	gr := schema.GroupResource{Group: "sei.io", Resource: "seinodedeployments"}
	getSND := func(context.Context, string, string) (*unstructured.Unstructured, error) {
		return nil, apierrors.NewForbidden(gr, "x", errors.New("rbac"))
	}
	var warn bytes.Buffer
	start := time.Now()
	got := pollPerPod(context.Background(), getSND, "ns", "snd", 3, &warn)
	elapsed := time.Since(start)
	if got != nil {
		t.Errorf("terminal error should return nil, got %+v", got)
	}
	if elapsed > time.Second {
		t.Errorf("terminal error should short-circuit; took %s", elapsed)
	}
	if !strings.Contains(warn.String(), "forbidden") && !strings.Contains(warn.String(), "Forbidden") {
		t.Errorf("warn should mention forbidden: %q", warn.String())
	}
}

func TestPollPerPod_RetriesUntilReady(t *testing.T) {
	// Three reconcile ticks: nothing → partial → ready.
	withFastPolling(t, 5*time.Second, 5*time.Millisecond)
	var calls atomic.Int32
	getSND := func(context.Context, string, string) (*unstructured.Unstructured, error) {
		switch calls.Add(1) {
		case 1:
			return &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{"generation": int64(1)},
				"status":   map[string]any{},
			}}, nil
		case 2:
			return sndReady(1, 1), nil
		default:
			return sndReady(1, 3), nil
		}
	}
	var warn bytes.Buffer
	got := pollPerPod(context.Background(), getSND, "ns", "snd", 3, &warn)
	if len(got) != 3 {
		t.Errorf("expected 3 entries after retry, got %d (%+v); warn=%q", len(got), got, warn.String())
	}
	if calls.Load() < 3 {
		t.Errorf("expected at least 3 GetSND calls, got %d", calls.Load())
	}
}

func TestPollPerPod_StaleGenerationKeepsPolling(t *testing.T) {
	withFastPolling(t, 100*time.Millisecond, 5*time.Millisecond)
	var calls atomic.Int32
	getSND := func(context.Context, string, string) (*unstructured.Unstructured, error) {
		calls.Add(1)
		return sndStaleGeneration(8, 7, 3), nil
	}
	var warn bytes.Buffer
	got := pollPerPod(context.Background(), getSND, "ns", "snd", 3, &warn)
	if got != nil {
		t.Errorf("stale-gen SND must time out, got %+v", got)
	}
	if calls.Load() < 5 {
		t.Errorf("poller should have retried; calls=%d", calls.Load())
	}
	if !strings.Contains(warn.String(), "timed out") {
		t.Errorf("warn should mention timeout: %q", warn.String())
	}
}

func TestPollPerPod_NotFoundIsTransient(t *testing.T) {
	withFastPolling(t, 5*time.Second, 5*time.Millisecond)
	gr := schema.GroupResource{Group: "sei.io", Resource: "seinodedeployments"}
	var calls atomic.Int32
	getSND := func(context.Context, string, string) (*unstructured.Unstructured, error) {
		switch calls.Add(1) {
		case 1, 2:
			return nil, apierrors.NewNotFound(gr, "snd")
		default:
			return sndReady(1, 2), nil
		}
	}
	var warn bytes.Buffer
	got := pollPerPod(context.Background(), getSND, "ns", "snd", 2, &warn)
	if len(got) != 2 {
		t.Errorf("expected 2 entries after NotFound retry, got %d; warn=%q", len(got), warn.String())
	}
}

func TestPollPerPod_ContextCancelExitsCleanly(t *testing.T) {
	withFastPolling(t, 30*time.Second, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	getSND := func(context.Context, string, string) (*unstructured.Unstructured, error) {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"generation": int64(1)},
		}}, nil
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	var warn bytes.Buffer
	start := time.Now()
	got := pollPerPod(ctx, getSND, "ns", "snd", 3, &warn)
	elapsed := time.Since(start)
	if got != nil {
		t.Errorf("cancelled ctx should return nil, got %+v", got)
	}
	if elapsed > time.Second {
		t.Errorf("cancellation should exit promptly; took %s", elapsed)
	}
}

var _ perPodPoller = pollPerPodFromCluster
