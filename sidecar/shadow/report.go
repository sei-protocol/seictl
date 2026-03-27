package shadow

import (
	"context"
	"fmt"
	"time"
)

// BuildDivergenceReport captures a complete investigation artifact at the
// given height. It pairs the comparison result with the full raw RPC
// responses from both chains so engineers can diagnose offline.
func (c *Comparator) BuildDivergenceReport(ctx context.Context, height int64, comparison CompareResult) (*DivergenceReport, error) {
	shadowSnap, err := captureChainSnapshot(ctx, c.shadowRPC, height)
	if err != nil {
		return nil, fmt.Errorf("capturing shadow snapshot at height %d: %w", height, err)
	}

	canonicalSnap, err := captureChainSnapshot(ctx, c.canonicalRPC, height)
	if err != nil {
		return nil, fmt.Errorf("capturing canonical snapshot at height %d: %w", height, err)
	}

	return &DivergenceReport{
		Height:     height,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Comparison: comparison,
		Shadow:     *shadowSnap,
		Canonical:  *canonicalSnap,
	}, nil
}

func captureChainSnapshot(ctx context.Context, rpcEndpoint string, height int64) (*ChainSnapshot, error) {
	block, err := fetchRawBlock(ctx, rpcEndpoint, height)
	if err != nil {
		return nil, err
	}

	blockResults, err := fetchRawBlockResults(ctx, rpcEndpoint, height)
	if err != nil {
		return nil, err
	}

	return &ChainSnapshot{
		Block:        block,
		BlockResults: blockResults,
	}, nil
}

func fetchRawBlock(ctx context.Context, rpcEndpoint string, height int64) ([]byte, error) {
	url := fmt.Sprintf("%s/block?height=%d", rpcEndpoint, height)
	return rpcGet(ctx, url)
}

func fetchRawBlockResults(ctx context.Context, rpcEndpoint string, height int64) ([]byte, error) {
	url := fmt.Sprintf("%s/block_results?height=%d", rpcEndpoint, height)
	return rpcGet(ctx, url)
}
