package analysis

import (
	"context"
	"io"

	"github.com/sei-protocol/seictl/sidecar/shadow"
)

// Search finds divergences matching specific criteria within a block range.
func (s *Store) Search(ctx context.Context, input SearchInput) (*SearchOutput, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	start := int64(0)
	end := int64(1<<63 - 1)
	if input.Start != nil {
		start = *input.Start
	}
	if input.End != nil {
		end = *input.End
	}

	iter, _, err := s.OpenRange(ctx, start, end, -1)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	out := &SearchOutput{}
	skipped := 0

	for {
		result, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if result.Match {
			continue
		}

		if !matchesFilters(result, input.Layer, input.Fields) {
			continue
		}

		out.TotalMatches++

		if skipped < input.Offset {
			skipped++
			continue
		}

		if len(out.Results) >= limit {
			out.HasMore = true
			continue
		}

		out.Results = append(out.Results, buildMatch(result))
	}

	if out.Results == nil {
		out.Results = []DivergenceMatch{}
	}

	return out, nil
}

func matchesFilters(result *shadow.CompareResult, layer *int, fields []string) bool {
	if layer != nil {
		if result.DivergenceLayer == nil || *result.DivergenceLayer != *layer {
			// Also match if layer filter is 1 and we have Layer1 data.
			if *layer == 1 && result.Layer1 != nil && len(result.Layer1.Divergences) > 0 {
				// matched via L1 data
			} else if *layer == 0 && !result.Layer0.Match() {
				// matched via L0 mismatch
			} else {
				return false
			}
		}
	}

	if len(fields) > 0 {
		resultFields := collectFields(result)
		if !anyIntersection(fields, resultFields) {
			return false
		}
	}

	return true
}

func collectFields(result *shadow.CompareResult) []string {
	var fields []string

	if !result.Layer0.AppHashMatch {
		fields = append(fields, "appHash")
	}
	if !result.Layer0.LastResultsHashMatch {
		fields = append(fields, "lastResultsHash")
	}
	if !result.Layer0.GasUsedMatch {
		fields = append(fields, "gasUsed")
	}

	if result.Layer1 != nil {
		for _, div := range result.Layer1.Divergences {
			for _, f := range div.Fields {
				fields = append(fields, f.Field)
			}
		}
	}

	return fields
}

func anyIntersection(wanted, have []string) bool {
	set := make(map[string]bool, len(have))
	for _, f := range have {
		set[f] = true
	}
	for _, w := range wanted {
		if set[w] {
			return true
		}
	}
	return false
}

func buildMatch(result *shadow.CompareResult) DivergenceMatch {
	m := DivergenceMatch{
		Height:    result.Height,
		Timestamp: result.Timestamp,
	}
	if result.DivergenceLayer != nil {
		m.DivergenceLayer = *result.DivergenceLayer
	}

	if !result.Layer0.AppHashMatch {
		m.Layer0Fields = append(m.Layer0Fields, "appHash")
	}
	if !result.Layer0.LastResultsHashMatch {
		m.Layer0Fields = append(m.Layer0Fields, "lastResultsHash")
	}
	if !result.Layer0.GasUsedMatch {
		m.Layer0Fields = append(m.Layer0Fields, "gasUsed")
	}

	if result.Layer1 != nil {
		summary := &SearchLayer1Summary{
			TotalTxs:     result.Layer1.TotalTxs,
			TxCountMatch: result.Layer1.TxCountMatch,
		}
		fieldSet := make(map[string]bool)
		for _, div := range result.Layer1.Divergences {
			summary.DivergedTxCount++
			for _, f := range div.Fields {
				if !fieldSet[f.Field] {
					fieldSet[f.Field] = true
					summary.DivergedFields = append(summary.DivergedFields, f.Field)
				}
			}
		}
		m.Layer1Summary = summary
	}

	return m
}
