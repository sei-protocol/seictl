package analysis

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/sei-protocol/seictl/sidecar/shadow"
)

// mockLister implements seis3.ObjectLister for testing.
type mockLister struct {
	objects []s3types.Object
}

func (m *mockLister) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var filtered []s3types.Object
	prefix := aws.ToString(input.Prefix)
	for _, obj := range m.objects {
		key := aws.ToString(obj.Key)
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			filtered = append(filtered, obj)
		}
	}
	return &s3.ListObjectsV2Output{
		Contents:    filtered,
		IsTruncated: aws.Bool(false),
	}, nil
}

// mockDownloader implements seis3.Downloader for testing.
type mockDownloader struct {
	pages map[string][]shadow.CompareResult
}

func (m *mockDownloader) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(input.Key)
	results, ok := m.pages[key]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}

	// Build gzipped NDJSON.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	for _, r := range results {
		line, _ := json.Marshal(r)
		gw.Write(append(line, '\n'))
	}
	gw.Close()

	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}, nil
}

func makeResults(start, end int64, match bool) []shadow.CompareResult {
	var results []shadow.CompareResult
	for h := start; h <= end; h++ {
		r := shadow.CompareResult{
			Height:    h,
			Timestamp: "2026-04-02T00:00:00Z",
			Match:     match,
			Layer0: shadow.Layer0Result{
				AppHashMatch:         match,
				LastResultsHashMatch: true,
				GasUsedMatch:         true,
			},
		}
		if !match {
			layer := 0
			r.DivergenceLayer = &layer
			r.Layer0.ShadowAppHash = "aaa"
			r.Layer0.CanonicalAppHash = "bbb"
		}
		results = append(results, r)
	}
	return results
}

func TestSummarize_AllMatch(t *testing.T) {
	results := makeResults(100, 199, true)
	lister := &mockLister{
		objects: []s3types.Object{
			{Key: aws.String("p/100-199.compare.ndjson.gz"), Size: aws.Int64(1024)},
		},
	}
	dl := &mockDownloader{pages: map[string][]shadow.CompareResult{
		"p/100-199.compare.ndjson.gz": results,
	}}
	store := NewStore(lister, dl, "bucket", "p/")

	out, err := store.Summarize(context.Background(), SummarizeInput{
		Start: 100, End: 199, MaxPages: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Totals.TotalBlocks != 100 {
		t.Errorf("totalBlocks = %d, want 100", out.Totals.TotalBlocks)
	}
	if out.Totals.MatchRate != 1.0 {
		t.Errorf("matchRate = %f, want 1.0", out.Totals.MatchRate)
	}
	if out.Totals.DivergedBlocks != 0 {
		t.Errorf("diverged = %d, want 0", out.Totals.DivergedBlocks)
	}
}

func TestSummarize_Mixed(t *testing.T) {
	matching := makeResults(100, 189, true)
	diverged := makeResults(190, 199, false)

	lister := &mockLister{
		objects: []s3types.Object{
			{Key: aws.String("p/100-199.compare.ndjson.gz"), Size: aws.Int64(1024)},
		},
	}
	dl := &mockDownloader{pages: map[string][]shadow.CompareResult{
		"p/100-199.compare.ndjson.gz": append(matching, diverged...),
	}}
	store := NewStore(lister, dl, "bucket", "p/")

	out, err := store.Summarize(context.Background(), SummarizeInput{
		Start: 100, End: 199, MaxPages: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Totals.TotalBlocks != 100 {
		t.Errorf("totalBlocks = %d, want 100", out.Totals.TotalBlocks)
	}
	if out.Totals.DivergedBlocks != 10 {
		t.Errorf("diverged = %d, want 10", out.Totals.DivergedBlocks)
	}
	if out.Layer0Breakdown.AppHashMismatches != 10 {
		t.Errorf("appHashMismatches = %d, want 10", out.Layer0Breakdown.AppHashMismatches)
	}
	wantRate := 0.9
	if out.Totals.MatchRate != wantRate {
		t.Errorf("matchRate = %f, want %f", out.Totals.MatchRate, wantRate)
	}
}

func TestSummarize_Truncated(t *testing.T) {
	lister := &mockLister{
		objects: []s3types.Object{
			{Key: aws.String("p/100-199.compare.ndjson.gz"), Size: aws.Int64(1024)},
			{Key: aws.String("p/200-299.compare.ndjson.gz"), Size: aws.Int64(1024)},
			{Key: aws.String("p/300-399.compare.ndjson.gz"), Size: aws.Int64(1024)},
		},
	}
	dl := &mockDownloader{pages: map[string][]shadow.CompareResult{
		"p/100-199.compare.ndjson.gz": makeResults(100, 199, true),
	}}
	store := NewStore(lister, dl, "bucket", "p/")

	out, err := store.Summarize(context.Background(), SummarizeInput{
		Start: 100, End: 399, MaxPages: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Error("expected Truncated=true")
	}
}
