package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// blockJSON builds a minimal /block response with the given header fields.
func blockJSON(appHash, lastResultsHash string) []byte {
	resp := map[string]any{
		"result": map[string]any{
			"block": map[string]any{
				"header": map[string]any{
					"app_hash":          appHash,
					"last_results_hash": lastResultsHash,
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// blockResultsJSON builds a minimal /block_results response.
func blockResultsJSON(txs []txResult) []byte {
	resp := map[string]any{
		"result": map[string]any{
			"txs_results": txs,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// rpcServer creates an httptest server that responds to /block and /block_results.
func rpcServer(appHash, lastResultsHash string, txs []txResult) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/block":
			w.Write(blockJSON(appHash, lastResultsHash))
		case r.URL.Path == "/block_results":
			w.Write(blockResultsJSON(txs))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCompareBlock_Match(t *testing.T) {
	srv := rpcServer("AABB", "CCDD", nil)
	defer srv.Close()

	comp := NewComparator(srv.URL, srv.URL)
	result, err := comp.CompareBlock(context.Background(), 100)
	if err != nil {
		t.Fatalf("CompareBlock: %v", err)
	}

	if !result.Match {
		t.Error("expected match when both endpoints return identical data")
	}
	if result.DivergenceLayer != nil {
		t.Errorf("expected nil divergence layer, got %d", *result.DivergenceLayer)
	}
	if !result.Layer0.AppHashMatch {
		t.Error("expected AppHash match")
	}
	if result.Layer1 != nil {
		t.Error("expected nil Layer1 when Layer0 matches")
	}
}

func TestCompareBlock_Layer0Divergence(t *testing.T) {
	shadowSrv := rpcServer("SHADOW_HASH", "RESULTS_HASH", nil)
	defer shadowSrv.Close()
	canonicalSrv := rpcServer("CANONICAL_HASH", "RESULTS_HASH", nil)
	defer canonicalSrv.Close()

	comp := NewComparator(shadowSrv.URL, canonicalSrv.URL)
	result, err := comp.CompareBlock(context.Background(), 100)
	if err != nil {
		t.Fatalf("CompareBlock: %v", err)
	}

	if result.Match {
		t.Error("expected divergence")
	}
	if result.DivergenceLayer == nil || *result.DivergenceLayer != 0 {
		t.Errorf("expected divergence layer 0, got %v", result.DivergenceLayer)
	}
	if result.Layer0.AppHashMatch {
		t.Error("expected AppHash mismatch")
	}
	if result.Layer0.ShadowAppHash != "SHADOW_HASH" {
		t.Errorf("shadow app hash = %q, want SHADOW_HASH", result.Layer0.ShadowAppHash)
	}
	if result.Layer0.CanonicalAppHash != "CANONICAL_HASH" {
		t.Errorf("canonical app hash = %q, want CANONICAL_HASH", result.Layer0.CanonicalAppHash)
	}
}

func TestCompareBlock_Layer1TxDivergence(t *testing.T) {
	shadowTxs := []txResult{
		{Code: 0, GasUsed: "100", GasWanted: "200", Log: "ok", Events: json.RawMessage(`[]`)},
	}
	canonicalTxs := []txResult{
		{Code: 1, GasUsed: "150", GasWanted: "200", Log: "reverted", Events: json.RawMessage(`[]`)},
	}

	shadowSrv := rpcServer("AAA", "BBB", shadowTxs)
	defer shadowSrv.Close()
	canonicalSrv := rpcServer("CCC", "BBB", canonicalTxs)
	defer canonicalSrv.Close()

	comp := NewComparator(shadowSrv.URL, canonicalSrv.URL)
	result, err := comp.CompareBlock(context.Background(), 50)
	if err != nil {
		t.Fatalf("CompareBlock: %v", err)
	}

	if result.Match {
		t.Error("expected divergence")
	}
	if result.Layer1 == nil {
		t.Fatal("expected Layer1 to be populated")
	}
	if len(result.Layer1.Divergences) != 1 {
		t.Fatalf("expected 1 tx divergence, got %d", len(result.Layer1.Divergences))
	}

	div := result.Layer1.Divergences[0]
	if div.TxIndex != 0 {
		t.Errorf("tx index = %d, want 0", div.TxIndex)
	}

	// Should have divergences for code, gasUsed, and log.
	fieldNames := make(map[string]bool)
	for _, f := range div.Fields {
		fieldNames[f.Field] = true
	}
	for _, expected := range []string{"code", "gasUsed", "log"} {
		if !fieldNames[expected] {
			t.Errorf("expected field %q in divergences", expected)
		}
	}
}

func TestCompareBlock_Layer1TxCountMismatch(t *testing.T) {
	shadowTxs := []txResult{
		{Code: 0, GasUsed: "100", GasWanted: "200"},
		{Code: 0, GasUsed: "100", GasWanted: "200"},
	}
	canonicalTxs := []txResult{
		{Code: 0, GasUsed: "100", GasWanted: "200"},
	}

	shadowSrv := rpcServer("AAA", "BBB", shadowTxs)
	defer shadowSrv.Close()
	canonicalSrv := rpcServer("CCC", "BBB", canonicalTxs)
	defer canonicalSrv.Close()

	comp := NewComparator(shadowSrv.URL, canonicalSrv.URL)
	result, err := comp.CompareBlock(context.Background(), 50)
	if err != nil {
		t.Fatalf("CompareBlock: %v", err)
	}

	if result.Layer1 == nil {
		t.Fatal("expected Layer1 result")
	}
	if result.Layer1.TxCountMatch {
		t.Error("expected tx count mismatch")
	}

	// Extra tx on shadow side should be reported as "presence" divergence.
	found := false
	for _, d := range result.Layer1.Divergences {
		for _, f := range d.Fields {
			if f.Field == "presence" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected a presence divergence for the extra tx")
	}
}

func TestCompareBlock_RPCError(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error")
	}))
	defer badSrv.Close()

	goodSrv := rpcServer("AA", "BB", nil)
	defer goodSrv.Close()

	comp := NewComparator(badSrv.URL, goodSrv.URL)
	_, err := comp.CompareBlock(context.Background(), 1)
	if err == nil {
		t.Error("expected error when shadow RPC fails")
	}
}
