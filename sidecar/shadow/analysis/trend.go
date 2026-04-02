package analysis

import (
	"context"
	"io"
	"math"
)

const transitionThreshold = 0.25 // 25 percentage points

// Trends analyzes divergence patterns over a block range.
func (s *Store) Trends(ctx context.Context, input TrendsInput) (*TrendsOutput, error) {
	windowSize := input.WindowSize
	if windowSize <= 0 {
		windowSize = 10000
	}
	maxPages := input.MaxPages
	if maxPages <= 0 {
		maxPages = 200
	}

	iter, _, err := s.OpenRange(ctx, input.Start, input.End, maxPages)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	bucketMap := make(map[int64]*TrendBucket)
	var totalBlocks, totalDiverged int64

	// Track consecutive divergent heights for cluster detection.
	var clusters []DivergenceCluster
	var clusterStart int64
	var lastDivergedHeight int64
	inCluster := false

	for {
		result, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Bucket assignment.
		idx := (result.Height - input.Start) / windowSize
		windowStart := input.Start + idx*windowSize
		windowEnd := windowStart + windowSize - 1
		if windowEnd > input.End {
			windowEnd = input.End
		}

		b, ok := bucketMap[idx]
		if !ok {
			b = &TrendBucket{
				StartHeight: windowStart,
				EndHeight:   windowEnd,
			}
			bucketMap[idx] = b
		}
		b.TotalBlocks++
		totalBlocks++

		if !result.Match {
			b.DivergedBlocks++
			totalDiverged++

			// Cluster detection: contiguous divergent blocks.
			if !inCluster {
				clusterStart = result.Height
				inCluster = true
			}
			lastDivergedHeight = result.Height
		} else if inCluster {
			length := lastDivergedHeight - clusterStart + 1
			if length >= 2 {
				clusters = append(clusters, DivergenceCluster{
					StartHeight: clusterStart,
					EndHeight:   lastDivergedHeight,
					Length:       length,
				})
			}
			inCluster = false
		}
	}

	// Close any open cluster.
	if inCluster {
		length := lastDivergedHeight - clusterStart + 1
		if length >= 2 {
			clusters = append(clusters, DivergenceCluster{
				StartHeight: clusterStart,
				EndHeight:   lastDivergedHeight,
				Length:       length,
			})
		}
	}

	// Sort buckets by window index.
	maxIdx := int64(-1)
	for idx := range bucketMap {
		if idx > maxIdx {
			maxIdx = idx
		}
	}

	var buckets []TrendBucket
	for i := int64(0); i <= maxIdx; i++ {
		b, ok := bucketMap[i]
		if !ok {
			continue
		}
		if b.TotalBlocks > 0 {
			b.DivergenceRate = float64(b.DivergedBlocks) / float64(b.TotalBlocks)
		}
		buckets = append(buckets, *b)
	}

	// Detect transitions: adjacent windows where rate changes by >25pp.
	var transitions []Transition
	for i := 1; i < len(buckets); i++ {
		delta := buckets[i].DivergenceRate - buckets[i-1].DivergenceRate
		if math.Abs(delta) >= transitionThreshold {
			direction := "degrading"
			if delta < 0 {
				direction = "improving"
			}
			transitions = append(transitions, Transition{
				Height:     buckets[i].StartHeight,
				RateBefore: buckets[i-1].DivergenceRate,
				RateAfter:  buckets[i].DivergenceRate,
				Direction:  direction,
			})
		}
	}

	var overallRate float64
	if totalBlocks > 0 {
		overallRate = float64(totalDiverged) / float64(totalBlocks)
	}

	if buckets == nil {
		buckets = []TrendBucket{}
	}
	if clusters == nil {
		clusters = []DivergenceCluster{}
	}
	if transitions == nil {
		transitions = []Transition{}
	}

	return &TrendsOutput{
		Range: EffectiveRange{
			Start:        input.Start,
			End:          input.End,
			BlockCount:   totalBlocks,
			PagesFetched: iter.PageCount(),
		},
		OverallRate: overallRate,
		Buckets:     buckets,
		Clusters:    clusters,
		Transitions: transitions,
	}, nil
}
