package shadow

import (
	"context"
	"fmt"
	"time"

	"github.com/sei-protocol/seictl/sidecar/rpc"
	"github.com/sei-protocol/seilog"
)

var log = seilog.NewLogger("seictl", "shadow")

// Comparator performs block-by-block comparison between a shadow node and
// a canonical chain node via their RPC endpoints.
type Comparator struct {
	shadowClient    *rpc.Client
	canonicalClient *rpc.Client

	// migrationMode tunes the verdict for an AppHash-breaking migration shadow
	// (e.g. memiavl->flatkv): the shadow's AppHash diverges from canonical by
	// design every block, so AppHash mismatch is expected and informational.
	// The correctness signals become LastResultsHash + gas + per-tx receipts
	// (execution equivalence), and Layer 1 always runs.
	migrationMode bool

	// Layer 2 (logical state diff) runs only when all three are configured.
	shadowState    StateReader
	canonicalState StateReader
	keySource      KeySource
}

// Option configures a Comparator.
type Option func(*Comparator)

// WithMigrationMode treats AppHash divergence as expected (not a mismatch) and
// keys the verdict on execution-results equivalence. Use for a shadow running
// an AppHash-breaking state migration against an un-migrated canonical chain.
func WithMigrationMode() Option {
	return func(c *Comparator) { c.migrationMode = true }
}

// WithLayer2 enables logical state-diff comparison: the keySource yields the
// accounts/slots each block touched, and the two StateReaders (EVM RPC on the
// shadow and canonical chains) supply their logical values to compare.
func WithLayer2(shadowState, canonicalState StateReader, keySource KeySource) Option {
	return func(c *Comparator) {
		c.shadowState = shadowState
		c.canonicalState = canonicalState
		c.keySource = keySource
	}
}

// NewComparator creates a Comparator that queries shadowRPC for the local
// shadow node and canonicalRPC for the reference chain.
func NewComparator(shadowRPC, canonicalRPC string, opts ...Option) *Comparator {
	c := &Comparator{
		shadowClient:    rpc.NewClient(shadowRPC, nil),
		canonicalClient: rpc.NewClient(canonicalRPC, nil),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CompareBlock performs a layered comparison for the given block height.
// Layer 0 (block headers) always runs. Layer 1 (transaction receipts) runs when
// a real divergence is detected, and always in migration mode — where AppHash,
// the cheap Layer 0 signal, is expected to differ, so the receipt check is the
// real correctness signal.
func (c *Comparator) CompareBlock(ctx context.Context, height int64) (*CompareResult, error) {
	result := &CompareResult{
		Height:        height,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Match:         true,
		MigrationMode: c.migrationMode,
	}

	// --- Layer 0: block header comparison ---
	l0, err := c.compareLayer0(ctx, height)
	if err != nil {
		return nil, err
	}
	result.Layer0 = *l0

	// In migration mode AppHash mismatch is expected; a real Layer 0 divergence
	// is an execution-results mismatch (LastResultsHash/gas). Otherwise any
	// Layer 0 field mismatch (including AppHash) counts.
	realL0Divergence := !l0.Match()
	if c.migrationMode {
		realL0Divergence = !l0.LastResultsHashMatch || !l0.GasUsedMatch
	}

	// --- Layer 1: transaction receipt comparison ---
	if realL0Divergence || c.migrationMode {
		l1, err := c.compareLayer1(ctx, height)
		if err != nil {
			log.Warn("layer 1 comparison failed, returning layer 0 result only",
				"height", height, "err", err)
		} else {
			result.Layer1 = l1
		}
	}
	l1Diverged := result.Layer1 != nil && len(result.Layer1.Divergences) > 0

	// --- Layer 2: logical state diff (when configured) ---
	if c.layer2Enabled() && (realL0Divergence || c.migrationMode) {
		l2, err := c.compareLayer2(ctx, height)
		if err != nil {
			log.Warn("layer 2 state comparison failed, skipping it for this height",
				"height", height, "err", err)
		} else {
			result.Layer2 = l2
		}
	}
	l2Diverged := result.Layer2 != nil && len(result.Layer2.Divergences) > 0

	switch {
	case realL0Divergence:
		result.Match = false
		layer := 0
		result.DivergenceLayer = &layer
	case l1Diverged:
		result.Match = false
		layer := 1
		result.DivergenceLayer = &layer
	case l2Diverged:
		result.Match = false
		layer := 2
		result.DivergenceLayer = &layer
	}

	return result, nil
}

func (c *Comparator) layer2Enabled() bool {
	return c.keySource != nil && c.shadowState != nil && c.canonicalState != nil
}

// compareLayer2 fetches the accounts a block touched and compares their logical
// state (storage/code/nonce) between the shadow and canonical chains.
func (c *Comparator) compareLayer2(ctx context.Context, height int64) (*Layer2Result, error) {
	touched, err := c.keySource.TouchedAccounts(ctx, height)
	if err != nil {
		return nil, fmt.Errorf("resolving touched accounts at height %d: %w", height, err)
	}
	return compareState(ctx, height, touched, c.shadowState, c.canonicalState)
}
