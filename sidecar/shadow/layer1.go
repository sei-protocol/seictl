package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// compareLayer1 fetches block_results from both chains and compares
// individual transaction receipts to identify which transactions diverged.
func (c *Comparator) compareLayer1(ctx context.Context, height int64) (*Layer1Result, error) {
	shadowResults, err := queryBlockResults(ctx, c.shadowRPC, height)
	if err != nil {
		return nil, fmt.Errorf("querying shadow block_results at height %d: %w", height, err)
	}
	canonicalResults, err := queryBlockResults(ctx, c.canonicalRPC, height)
	if err != nil {
		return nil, fmt.Errorf("querying canonical block_results at height %d: %w", height, err)
	}

	sTxs := shadowResults.txResults()
	cTxs := canonicalResults.txResults()

	result := &Layer1Result{
		TotalTxs:     max(len(sTxs), len(cTxs)),
		TxCountMatch: len(sTxs) == len(cTxs),
	}

	// Compare the overlapping transactions.
	minLen := min(len(sTxs), len(cTxs))
	for i := 0; i < minLen; i++ {
		divergence := compareTxReceipts(i, sTxs[i], cTxs[i])
		if divergence != nil {
			result.Divergences = append(result.Divergences, *divergence)
		}
	}

	// Any extra transactions on either side are divergences by definition.
	if len(sTxs) > minLen {
		for i := minLen; i < len(sTxs); i++ {
			result.Divergences = append(result.Divergences, TxDivergence{
				TxIndex: i,
				Fields: []FieldDivergence{{
					Field:     "presence",
					Shadow:    "present",
					Canonical: "missing",
				}},
			})
		}
	}
	if len(cTxs) > minLen {
		for i := minLen; i < len(cTxs); i++ {
			result.Divergences = append(result.Divergences, TxDivergence{
				TxIndex: i,
				Fields: []FieldDivergence{{
					Field:     "presence",
					Shadow:    "missing",
					Canonical: "present",
				}},
			})
		}
	}

	return result, nil
}

// compareTxReceipts compares critical fields from two transaction results.
// Returns nil when the receipts match.
func compareTxReceipts(idx int, shadow, canonical txResult) *TxDivergence {
	var fields []FieldDivergence

	if shadow.Code != canonical.Code {
		fields = append(fields, FieldDivergence{
			Field: "code", Shadow: shadow.Code, Canonical: canonical.Code,
		})
	}
	if shadow.GasUsed != canonical.GasUsed {
		fields = append(fields, FieldDivergence{
			Field: "gasUsed", Shadow: shadow.GasUsed, Canonical: canonical.GasUsed,
		})
	}
	if shadow.GasWanted != canonical.GasWanted {
		fields = append(fields, FieldDivergence{
			Field: "gasWanted", Shadow: shadow.GasWanted, Canonical: canonical.GasWanted,
		})
	}
	if shadow.Log != canonical.Log {
		fields = append(fields, FieldDivergence{
			Field: "log", Shadow: shadow.Log, Canonical: canonical.Log,
		})
	}
	if !reflect.DeepEqual(shadow.Events, canonical.Events) {
		fields = append(fields, FieldDivergence{
			Field: "events", Shadow: shadow.Events, Canonical: canonical.Events,
		})
	}

	if len(fields) == 0 {
		return nil
	}
	return &TxDivergence{TxIndex: idx, Fields: fields}
}

// blockResultsResponse is the subset of the Tendermint /block_results
// RPC response needed for transaction receipt comparison.
type blockResultsResponse struct {
	Result struct {
		TxsResults []txResult `json:"txs_results"`
	} `json:"result"`
}

type txResult struct {
	Code      int             `json:"code"`
	Log       string          `json:"log"`
	GasUsed   string          `json:"gas_used"`
	GasWanted string          `json:"gas_wanted"`
	Events    json.RawMessage `json:"events"`
}

func (b *blockResultsResponse) txResults() []txResult {
	return b.Result.TxsResults
}

// queryBlockResults fetches /block_results at the given height.
func queryBlockResults(ctx context.Context, rpcEndpoint string, height int64) (*blockResultsResponse, error) {
	url := fmt.Sprintf("%s/block_results?height=%d", rpcEndpoint, height)
	body, err := rpcGet(ctx, url)
	if err != nil {
		return nil, err
	}

	var resp blockResultsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding /block_results response: %w", err)
	}
	return &resp, nil
}
