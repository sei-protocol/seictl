package analysis

import (
	"context"
	"io"
)

const maxDivergentHeights = 1000

// Summarize aggregates comparison statistics for a block range.
func (s *Store) Summarize(ctx context.Context, input SummarizeInput) (*SummarizeOutput, error) {
	maxPages := input.MaxPages
	if maxPages <= 0 {
		maxPages = 100
	}

	iter, totalPages, err := s.OpenRange(ctx, input.Start, input.End, maxPages)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	out := &SummarizeOutput{
		Range: EffectiveRange{
			Start: input.Start,
			End:   input.End,
		},
		Truncated: totalPages > maxPages,
	}
	var l1 Layer1Breakdown

	var minSeen, maxSeen int64
	firstSeen := false

	for {
		result, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		out.Totals.TotalBlocks++

		if !firstSeen || result.Height < minSeen {
			minSeen = result.Height
			firstSeen = true
		}
		if result.Height > maxSeen {
			maxSeen = result.Height
		}

		if result.Match {
			out.Totals.MatchedBlocks++
			continue
		}

		out.Totals.DivergedBlocks++
		if len(out.DivergentHeights) < maxDivergentHeights {
			out.DivergentHeights = append(out.DivergentHeights, result.Height)
		}

		// Layer 0 breakdown.
		if !result.Layer0.AppHashMatch {
			out.Layer0Breakdown.AppHashMismatches++
		}
		if !result.Layer0.LastResultsHashMatch {
			out.Layer0Breakdown.LastResultsHashMismatches++
		}
		if !result.Layer0.GasUsedMatch {
			out.Layer0Breakdown.GasUsedMismatches++
		}

		// Layer 1 breakdown.
		if result.Layer1 != nil {
			if !result.Layer1.TxCountMatch {
				l1.TxCountMismatches++
			}
			for _, div := range result.Layer1.Divergences {
				l1.TotalTxDivergences++
				for _, f := range div.Fields {
					if l1.ByField == nil {
						l1.ByField = make(map[string]int64)
					}
					l1.ByField[f.Field]++
				}
			}
		}
	}

	// Finalize.
	if out.Totals.TotalBlocks > 0 {
		out.Totals.MatchRate = float64(out.Totals.MatchedBlocks) / float64(out.Totals.TotalBlocks)
	}
	out.Range.Start = minSeen
	out.Range.End = maxSeen
	out.Range.BlockCount = out.Totals.TotalBlocks
	out.Range.PagesFetched = iter.PageCount()

	if l1.TotalTxDivergences > 0 || l1.TxCountMismatches > 0 {
		out.Layer1Breakdown = &l1
	}
	if out.DivergentHeights == nil {
		out.DivergentHeights = []int64{}
	}

	return out, nil
}
