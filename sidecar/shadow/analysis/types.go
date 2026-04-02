package analysis

// ListInput configures the list operation.
type ListInput struct {
	Start *int64
	End   *int64
	Limit int // default 100, max 1000
}

// ListOutput is the inventory of comparison data in S3.
type ListOutput struct {
	Pages             []PageEntry       `json:"pages"`
	DivergenceReports []DivergenceEntry `json:"divergenceReports"`
	Summary           DataInventory     `json:"summary"`
}

// PageEntry describes a single comparison NDJSON page in S3.
type PageEntry struct {
	Key         string `json:"key"`
	StartHeight int64  `json:"startHeight"`
	EndHeight   int64  `json:"endHeight"`
	BlockCount  int    `json:"blockCount"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// DivergenceEntry describes a standalone divergence report in S3.
type DivergenceEntry struct {
	Key       string `json:"key"`
	Height    int64  `json:"height"`
	SizeBytes int64  `json:"sizeBytes"`
}

// DataInventory provides a quick overview of the dataset.
type DataInventory struct {
	TotalPages             int   `json:"totalPages"`
	TotalDivergenceReports int   `json:"totalDivergenceReports"`
	MinHeight              int64 `json:"minHeight"`
	MaxHeight              int64 `json:"maxHeight"`
	TotalBlocksCovered     int64 `json:"totalBlocksCovered"`
}

// SummarizeInput configures the summarize operation.
type SummarizeInput struct {
	Start    int64
	End      int64
	MaxPages int // default 100
}

// SummarizeOutput holds aggregated comparison statistics.
type SummarizeOutput struct {
	Range            EffectiveRange   `json:"range"`
	Totals           SummaryTotals    `json:"totals"`
	Layer0Breakdown  Layer0Breakdown  `json:"layer0Breakdown"`
	Layer1Breakdown  *Layer1Breakdown `json:"layer1Breakdown,omitempty"`
	DivergentHeights []int64          `json:"divergentHeights"`
	Truncated        bool             `json:"truncated"`
}

// EffectiveRange describes the block range that was actually analyzed.
type EffectiveRange struct {
	Start        int64 `json:"start"`
	End          int64 `json:"end"`
	BlockCount   int64 `json:"blockCount"`
	PagesFetched int   `json:"pagesFetched"`
}

// SummaryTotals holds top-line match/diverge numbers.
type SummaryTotals struct {
	TotalBlocks    int64   `json:"totalBlocks"`
	MatchedBlocks  int64   `json:"matchedBlocks"`
	DivergedBlocks int64   `json:"divergedBlocks"`
	MatchRate      float64 `json:"matchRate"`
}

// Layer0Breakdown counts mismatches by Layer 0 field.
type Layer0Breakdown struct {
	AppHashMismatches         int64 `json:"appHashMismatches"`
	LastResultsHashMismatches int64 `json:"lastResultsHashMismatches"`
	GasUsedMismatches         int64 `json:"gasUsedMismatches"`
}

// Layer1Breakdown counts mismatches by Layer 1 receipt field.
type Layer1Breakdown struct {
	TotalTxDivergences int64            `json:"totalTxDivergences"`
	ByField            map[string]int64 `json:"byField"`
	TxCountMismatches  int64            `json:"txCountMismatches"`
}

// SearchInput configures the search operation.
type SearchInput struct {
	Start  *int64
	End    *int64
	Layer  *int
	Fields []string
	Limit  int // default 50, max 500
	Offset int
}

// SearchOutput holds the search results.
type SearchOutput struct {
	Results      []DivergenceMatch `json:"results"`
	TotalMatches int               `json:"totalMatches"`
	HasMore      bool              `json:"hasMore"`
}

// DivergenceMatch describes a single divergent block found by search.
type DivergenceMatch struct {
	Height          int64                `json:"height"`
	Timestamp       string               `json:"timestamp"`
	DivergenceLayer int                  `json:"divergenceLayer"`
	Layer0Fields    []string             `json:"layer0Fields,omitempty"`
	Layer1Summary   *SearchLayer1Summary `json:"layer1Summary,omitempty"`
}

// SearchLayer1Summary provides a compact view of Layer 1 divergences.
type SearchLayer1Summary struct {
	TotalTxs        int      `json:"totalTxs"`
	DivergedTxCount int      `json:"divergedTxCount"`
	DivergedFields  []string `json:"divergedFields"`
	TxCountMatch    bool     `json:"txCountMatch"`
}

// TrendsInput configures the trends analysis.
type TrendsInput struct {
	Start      int64
	End        int64
	WindowSize int64 // default 10000
	MaxPages   int   // default 200
}

// TrendsOutput holds trend analysis results.
type TrendsOutput struct {
	Range       EffectiveRange      `json:"range"`
	OverallRate float64             `json:"overallRate"`
	Buckets     []TrendBucket       `json:"buckets"`
	Clusters    []DivergenceCluster `json:"clusters"`
	Transitions []Transition        `json:"transitions"`
}

// TrendBucket holds statistics for a single analysis window.
type TrendBucket struct {
	StartHeight    int64   `json:"startHeight"`
	EndHeight      int64   `json:"endHeight"`
	TotalBlocks    int64   `json:"totalBlocks"`
	DivergedBlocks int64   `json:"divergedBlocks"`
	DivergenceRate float64 `json:"divergenceRate"`
}

// DivergenceCluster identifies a contiguous run of divergent blocks.
type DivergenceCluster struct {
	StartHeight int64 `json:"startHeight"`
	EndHeight   int64 `json:"endHeight"`
	Length      int64 `json:"length"`
}

// Transition identifies a point where divergence behavior changes significantly.
type Transition struct {
	Height     int64   `json:"height"`
	RateBefore float64 `json:"rateBefore"`
	RateAfter  float64 `json:"rateAfter"`
	Direction  string  `json:"direction"` // "improving" or "degrading"
}
