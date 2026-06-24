package tasks

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordDigestMetrics(t *testing.T) {
	matchBucket := func(m bool) bucket { return bucket{Match: m} }

	t.Run("match increments runs only", func(t *testing.T) {
		const norm = "match-norm"
		runsBefore := testutil.ToFloat64(evmLogicalDigestRuns.WithLabelValues(norm, "match"))

		recordDigestMetrics(EndpointDigestRecord{
			Normalization: norm,
			Match:         true,
			PerBucket: map[string]bucket{
				"account": matchBucket(true),
				"code":    matchBucket(true),
				"storage": matchBucket(true),
				"legacy":  matchBucket(true),
			},
		})

		if got := testutil.ToFloat64(evmLogicalDigestRuns.WithLabelValues(norm, "match")) - runsBefore; got != 1 {
			t.Errorf("runs{result=match} delta = %v, want 1", got)
		}
	})

	t.Run("mismatch increments per diverging bucket", func(t *testing.T) {
		const norm = "bucket-norm"
		runsBefore := testutil.ToFloat64(evmLogicalDigestRuns.WithLabelValues(norm, "mismatch"))
		storageBefore := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "storage"))
		codeBefore := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "code"))
		finalBefore := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "final"))

		recordDigestMetrics(EndpointDigestRecord{
			Normalization: norm,
			Match:         false,
			PerBucket: map[string]bucket{
				"account": matchBucket(true),
				"code":    matchBucket(false),
				"storage": matchBucket(false),
				"legacy":  matchBucket(true),
			},
		})

		if got := testutil.ToFloat64(evmLogicalDigestRuns.WithLabelValues(norm, "mismatch")) - runsBefore; got != 1 {
			t.Errorf("runs{result=mismatch} delta = %v, want 1", got)
		}
		if got := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "storage")) - storageBefore; got != 1 {
			t.Errorf("mismatches{reason=storage} delta = %v, want 1", got)
		}
		if got := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "code")) - codeBefore; got != 1 {
			t.Errorf("mismatches{reason=code} delta = %v, want 1", got)
		}
		if got := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "final")) - finalBefore; got != 0 {
			t.Errorf("mismatches{reason=final} delta = %v, want 0 (per-bucket attributed)", got)
		}
	})

	t.Run("final-only mismatch attributes to final", func(t *testing.T) {
		const norm = "final-norm"
		finalBefore := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "final"))

		// Combined digest differs but every per-bucket digest matches.
		recordDigestMetrics(EndpointDigestRecord{
			Normalization: norm,
			Match:         false,
			PerBucket: map[string]bucket{
				"account": matchBucket(true),
				"code":    matchBucket(true),
				"storage": matchBucket(true),
				"legacy":  matchBucket(true),
			},
		})

		if got := testutil.ToFloat64(evmLogicalDigestMismatches.WithLabelValues(norm, "final")) - finalBefore; got != 1 {
			t.Errorf("mismatches{reason=final} delta = %v, want 1", got)
		}
	})
}
