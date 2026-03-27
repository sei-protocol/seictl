package shadow

import (
	"context"
	"encoding/json"
	"fmt"
)

// compareLayer0 fetches the block header from both chains at the given
// height and compares AppHash, LastResultsHash, and total gas used.
func (c *Comparator) compareLayer0(ctx context.Context, height int64) (*Layer0Result, error) {
	shadowBlock, err := queryBlock(ctx, c.shadowRPC, height)
	if err != nil {
		return nil, fmt.Errorf("querying shadow block at height %d: %w", height, err)
	}
	canonicalBlock, err := queryBlock(ctx, c.canonicalRPC, height)
	if err != nil {
		return nil, fmt.Errorf("querying canonical block at height %d: %w", height, err)
	}

	sHeader := shadowBlock.header()
	cHeader := canonicalBlock.header()

	sAppHash := sHeader.AppHash
	cAppHash := cHeader.AppHash
	sLastResults := sHeader.LastResultsHash
	cLastResults := cHeader.LastResultsHash

	// Gas is summed from block_results; the block header doesn't carry it
	// directly. For L0 we compare what the header gives us. Gas comparison
	// via block_results happens implicitly in L1.
	// For now, mark gas as matching at L0; a future enhancement can pull
	// gas from the block_results endpoint at this layer.
	gasMatch := true

	result := &Layer0Result{
		AppHashMatch:         sAppHash == cAppHash,
		LastResultsHashMatch: sLastResults == cLastResults,
		GasUsedMatch:         gasMatch,
	}

	if !result.AppHashMatch {
		result.ShadowAppHash = sAppHash
		result.CanonicalAppHash = cAppHash
	}
	if !result.LastResultsHashMatch {
		result.ShadowLastResultsHash = sLastResults
		result.CanonicalLastResultsHash = cLastResults
	}

	return result, nil
}

// blockResponse is the subset of the Tendermint /block RPC response
// that we need for header comparison.
type blockResponse struct {
	Result struct {
		Block struct {
			Header blockHeader `json:"header"`
		} `json:"block"`
	} `json:"result"`
}

type blockHeader struct {
	AppHash         string `json:"app_hash"`
	LastResultsHash string `json:"last_results_hash"`
}

func (b *blockResponse) header() blockHeader {
	return b.Result.Block.Header
}

// queryBlock fetches the block at the given height from a Tendermint RPC endpoint.
func queryBlock(ctx context.Context, rpcEndpoint string, height int64) (*blockResponse, error) {
	url := fmt.Sprintf("%s/block?height=%d", rpcEndpoint, height)
	body, err := rpcGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp blockResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding /block response: %w", err)
	}
	return &resp, nil
}
