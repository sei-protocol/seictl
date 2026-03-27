package shadow

import (
	"context"
	"time"

	"github.com/sei-protocol/seilog"
)

var log = seilog.NewLogger("seictl", "shadow")

// Comparator performs block-by-block comparison between a shadow node and
// a canonical chain node via their RPC endpoints.
type Comparator struct {
	shadowRPC    string
	canonicalRPC string
}

// NewComparator creates a Comparator that queries shadowRPC for the local
// shadow node and canonicalRPC for the reference chain.
func NewComparator(shadowRPC, canonicalRPC string) *Comparator {
	return &Comparator{
		shadowRPC:    shadowRPC,
		canonicalRPC: canonicalRPC,
	}
}

// CompareBlock performs a layered comparison for the given block height.
// Layer 0 (block headers) always runs. If Layer 0 detects a divergence,
// Layer 1 (transaction receipts) is run to provide diagnostic detail.
func (c *Comparator) CompareBlock(ctx context.Context, height int64) (*CompareResult, error) {
	result := &CompareResult{
		Height:    height,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Match:     true,
	}

	// --- Layer 0: block header comparison ---
	l0, err := c.compareLayer0(ctx, height)
	if err != nil {
		return nil, err
	}
	result.Layer0 = *l0

	if !l0.Match() {
		result.Match = false
		layer := 0
		result.DivergenceLayer = &layer

		// --- Layer 1: transaction receipt comparison ---
		l1, err := c.compareLayer1(ctx, height)
		if err != nil {
			log.Warn("layer 1 comparison failed, returning layer 0 result only",
				"height", height, "err", err)
		} else {
			result.Layer1 = l1
			if l1 != nil && len(l1.Divergences) > 0 {
				layer = 1
			}
		}
	}

	return result, nil
}
