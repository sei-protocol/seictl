package tasks

import (
	"context"
	"strings"
	"testing"
	"time"

	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seictl/sidecar/shadow"
)

// newTestComparisonLoop wires a comparison loop against two RPC servers whose
// blocks diverge at Layer 0 (different app_hash, matching results, no per-tx
// detail) so every compared block is a divergence, with a recording uploader.
// continueOnDivergence selects survey mode vs the default halt-on-first.
func newTestComparisonLoop(t *testing.T, continueOnDivergence bool) (*comparisonLoop, *recordingUploader) {
	t.Helper()
	shadowSrv := fakeRPCAndBlockServer(1<<30, "SHADOW", "RES", nil)
	canonicalSrv := fakeRPCAndBlockServer(1<<30, "CANONICAL", "RES", nil)
	t.Cleanup(shadowSrv.Close)
	t.Cleanup(canonicalSrv.Close)

	rec := &recordingUploader{}
	exporter := NewResultExporter(t.TempDir(), "test-chain", "pod-0", func(_ context.Context, _ string) (seis3.Uploader, error) {
		return rec, nil
	})
	return &comparisonLoop{
		exporter:     exporter,
		comparator:   shadow.NewComparator(shadowSrv.URL, canonicalSrv.URL),
		uploader:     rec,
		cfg:          ResultExportRequest{Bucket: "bkt", Region: "us-east-1", Prefix: "p/", ContinueOnDivergence: continueOnDivergence},
		prefix:       "p/",
		height:       1,
		pollInterval: time.Millisecond,
	}, rec
}

func countKeys(rec *recordingUploader, substr string) int {
	n := 0
	for _, k := range rec.keys {
		if strings.Contains(k, substr) {
			n++
		}
	}
	return n
}

// TestCompareDefaultMode_HaltsOnDivergence: with ContinueOnDivergence unset the
// first divergent block trips the loop — it reports diverged, uploads a
// per-block divergence report, and does not advance past the divergent height.
func TestCompareDefaultMode_HaltsOnDivergence(t *testing.T) {
	loop, rec := newTestComparisonLoop(t, false)

	diverged, err := loop.compareBlocksUpTo(context.Background(), 5)
	if err != nil {
		t.Fatalf("compareBlocksUpTo: %v", err)
	}
	if !diverged {
		t.Fatal("default mode must halt on the first divergence")
	}
	if loop.height != 1 {
		t.Errorf("height advanced to %d; default-mode halt must not step past the divergent block", loop.height)
	}
	if n := countKeys(rec, "divergence-1.report"); n != 1 {
		t.Errorf("expected exactly one per-block divergence report, got %d", n)
	}
}

// TestCompareSurveyMode_ContinuesPastDivergence: with ContinueOnDivergence set
// the loop surveys every divergent block to the end of the range, advances the
// height, and uploads NO per-block divergence report (the page is the record).
func TestCompareSurveyMode_ContinuesPastDivergence(t *testing.T) {
	loop, rec := newTestComparisonLoop(t, true)

	diverged, err := loop.compareBlocksUpTo(context.Background(), 5)
	if err != nil {
		t.Fatalf("compareBlocksUpTo: %v", err)
	}
	if diverged {
		t.Fatal("survey mode must not halt on divergence")
	}
	if loop.height != 6 {
		t.Errorf("height = %d, want 6 (surveyed every block past the divergences)", loop.height)
	}
	if n := countKeys(rec, ".report"); n != 0 {
		t.Errorf("survey mode must upload no per-block divergence report, got %d", n)
	}
}

// TestCompareSurveyMode_BoundsMemory: surveying past comparePageSize divergent
// blocks must flush-and-truncate the in-memory page rather than accumulate
// every result — the bound that keeps a multi-million-block sweep from
// exhausting the sidecar. Two full pages flush; the buffer holds only the
// trailing remainder.
func TestCompareSurveyMode_BoundsMemory(t *testing.T) {
	loop, rec := newTestComparisonLoop(t, true)

	const blocks = 2*comparePageSize + 50
	diverged, err := loop.compareBlocksUpTo(context.Background(), blocks)
	if err != nil {
		t.Fatalf("compareBlocksUpTo: %v", err)
	}
	if diverged {
		t.Fatal("survey mode must not halt on divergence")
	}
	if len(loop.pageBuf) >= comparePageSize {
		t.Errorf("pageBuf holds %d results; survey mode must flush+truncate at comparePageSize (%d) to bound memory", len(loop.pageBuf), comparePageSize)
	}
	if len(loop.pageBuf) != 50 {
		t.Errorf("pageBuf = %d, want 50 (the remainder after two full-page flushes)", len(loop.pageBuf))
	}
	if n := countKeys(rec, ".compare.ndjson.gz"); n != 2 {
		t.Errorf("expected 2 flushed compare pages, got %d", n)
	}
	if n := countKeys(rec, ".report"); n != 0 {
		t.Errorf("survey mode must upload no per-block divergence report, got %d", n)
	}
}
