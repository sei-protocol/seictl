package tasks

import "github.com/prometheus/client_golang/prometheus"

var (
	// evmLogicalDigestRuns counts evm-logical-digest comparisons published, one
	// per (height, normalization) record. result is "match" or "mismatch". This
	// is the liveness signal: rate(...)==0 means the oracle stopped running, which
	// divergence counters alone cannot distinguish from "ran and found nothing".
	// pod/chain come from Prometheus scrape/target labels, not metric labels.
	evmLogicalDigestRuns = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "seictl_evm_logical_digest_runs_total",
			Help: "Total evm-logical-digest comparisons published, labelled by normalization and match outcome.",
		},
		[]string{"normalization", "result"},
	)

	// evmLogicalDigestMismatches counts diverging buckets in mismatched records.
	// reason is the diverging bucket (account|code|storage|legacy) or "final" when
	// only the combined digest differs. Incremented once per diverging bucket, so
	// a record with two bad buckets adds two — the granular alertability signal.
	evmLogicalDigestMismatches = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "seictl_evm_logical_digest_mismatches_total",
			Help: "Total diverging buckets across evm-logical-digest mismatches, labelled by normalization and diverging bucket.",
		},
		[]string{"normalization", "reason"},
	)
)

func init() {
	prometheus.MustRegister(evmLogicalDigestRuns)
	prometheus.MustRegister(evmLogicalDigestMismatches)
}

// recordDigestMetrics emits the run outcome and, on mismatch, one increment per
// diverging bucket. When the record mismatches but no per-bucket digest differs
// (only the combined FINAL digest), it attributes the mismatch to "final".
func recordDigestMetrics(record EndpointDigestRecord) {
	result := "match"
	if !record.Match {
		result = "mismatch"
	}
	evmLogicalDigestRuns.WithLabelValues(record.Normalization, result).Inc()

	if record.Match {
		return
	}

	var anyBucket bool
	for _, name := range []string{"account", "code", "storage", "legacy"} {
		if b, ok := record.PerBucket[name]; ok && !b.Match {
			evmLogicalDigestMismatches.WithLabelValues(record.Normalization, name).Inc()
			anyBucket = true
		}
	}
	if !anyBucket {
		evmLogicalDigestMismatches.WithLabelValues(record.Normalization, "final").Inc()
	}
}
