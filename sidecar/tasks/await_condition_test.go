package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sei-protocol/seictl/sidecar/rpc"
)

// heightServer returns an httptest.Server that serves a height sequence.
// After the sequence is exhausted it keeps returning the last value.
func heightServer(heights ...int64) *httptest.Server {
	var mu sync.Mutex
	idx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := idx
		if i < len(heights)-1 {
			idx++
		}
		mu.Unlock()
		fmt.Fprintf(w, `{"sync_info":{"latest_block_height":"%d","catching_up":false}}`, heights[i])
	}))
}

func rpcClient(url string) *rpc.StatusClient {
	return rpc.NewStatusClient(url, nil)
}

func TestAwaitHeight_ReachesTarget(t *testing.T) {
	srv := heightServer(100, 200, 500)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(500),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := handler(ctx, params); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestAwaitHeight_AlreadyPastTarget(t *testing.T) {
	srv := heightServer(1000)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(500),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := handler(ctx, params); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestAwaitHeight_MissingTargetHeight(t *testing.T) {
	srv := heightServer(100)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{"condition": "height"}
	if err := handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing targetHeight")
	}
}

func TestAwaitHeight_ZeroTargetHeight(t *testing.T) {
	srv := heightServer(100)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(0),
	}
	if err := handler(context.Background(), params); err == nil {
		t.Fatal("expected error for zero targetHeight")
	}
}

func TestAwaitHeight_NegativeTargetHeight(t *testing.T) {
	srv := heightServer(100)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(-5),
	}
	if err := handler(context.Background(), params); err == nil {
		t.Fatal("expected error for negative targetHeight")
	}
}

func TestAwaitHeight_TransientRPCErrors(t *testing.T) {
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	goodSrv := heightServer(500)
	defer goodSrv.Close()

	var mu sync.Mutex
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		var target string
		if n <= 2 {
			target = errSrv.URL
		} else {
			target = goodSrv.URL
		}
		resp, err := http.Get(target + r.URL.Path)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
	}))
	defer proxy.Close()

	handler := NewConditionWaiter(rpcClient(proxy.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(500),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := handler(ctx, params); err != nil {
		t.Fatalf("expected success after transient errors, got %v", err)
	}
}

func TestAwaitHeight_ContextCancellation(t *testing.T) {
	srv := heightServer(100)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(99999),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := handler(ctx, params)
	if err == nil {
		t.Fatal("expected context error")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestAwaitHeight_MissingCondition(t *testing.T) {
	handler := NewConditionWaiter(rpcClient("http://unused")).Handler()
	params := map[string]any{}
	if err := handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing condition")
	}
}

func TestAwaitHeight_UnknownCondition(t *testing.T) {
	handler := NewConditionWaiter(rpcClient("http://unused")).Handler()
	params := map[string]any{"condition": "unknown"}
	if err := handler(context.Background(), params); err == nil {
		t.Fatal("expected error for unknown condition")
	}
}

func TestAwaitHeight_UnknownAction(t *testing.T) {
	srv := heightServer(500)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": float64(500),
		"action":       "UNKNOWN",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := handler(ctx, params); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestAwaitHeight_Int64TargetHeight(t *testing.T) {
	srv := heightServer(1000)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": int64(500),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := handler(ctx, params); err != nil {
		t.Fatalf("expected success with int64 targetHeight, got %v", err)
	}
}

func TestAwaitHeight_JSONNumberTargetHeight(t *testing.T) {
	srv := heightServer(5000)
	defer srv.Close()

	handler := NewConditionWaiter(rpcClient(srv.URL)).Handler()
	params := map[string]any{
		"condition":    "height",
		"targetHeight": json.Number("5000"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := handler(ctx, params); err != nil {
		t.Fatalf("expected success with json.Number targetHeight, got %v", err)
	}
}
