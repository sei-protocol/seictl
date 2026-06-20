package shadow

import "testing"

func TestReasonFor(t *testing.T) {
	tests := []struct {
		name   string
		result CompareResult
		want   string
	}{
		{
			name:   "clean result has no reason",
			result: CompareResult{Match: true},
			want:   "",
		},
		{
			name: "layer0 apphash only",
			result: CompareResult{
				Match:  false,
				Layer0: Layer0Result{AppHashMatch: false, LastResultsHashMatch: true, GasUsedMatch: true},
			},
			want: "layer0-apphash",
		},
		{
			name: "layer1 receipt field divergence",
			result: CompareResult{
				Match: false,
				Layer1: &Layer1Result{
					TxCountMatch: true,
					Divergences:  []TxDivergence{{TxIndex: 0, Fields: []FieldDivergence{{Field: "gasUsed"}}}},
				},
			},
			want: "layer1-receipt",
		},
		{
			name: "layer1 tx count mismatch is a results divergence",
			result: CompareResult{
				Match:  false,
				Layer1: &Layer1Result{TxCountMatch: false},
			},
			want: "layer1-results",
		},
		{
			name: "layer1 indeterminate",
			result: CompareResult{
				Match:  false,
				Layer1: &Layer1Result{TxCountMatch: true, Indeterminate: true},
			},
			want: "layer1-indeterminate",
		},
		{
			name: "layer2 storage divergence wins over layer1",
			result: CompareResult{
				Match:  false,
				Layer1: &Layer1Result{TxCountMatch: false},
				Layer2: &Layer2Result{Divergences: []StateDivergence{{Kind: "storage"}}},
			},
			want: "layer2-storage",
		},
		{
			name: "layer2 balance divergence",
			result: CompareResult{
				Match:  false,
				Layer2: &Layer2Result{Divergences: []StateDivergence{{Kind: "balance"}}},
			},
			want: "layer2-balance",
		},
		{
			name: "layer2 indeterminate forces a reason even without a divergence kind",
			result: CompareResult{
				Match:  false,
				Layer2: &Layer2Result{Indeterminate: true},
			},
			want: "layer2-indeterminate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReasonFor(&tt.result); got != tt.want {
				t.Errorf("ReasonFor() = %q, want %q", got, tt.want)
			}
		})
	}
}
