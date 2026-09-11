package cliutil

import (
	"strings"
	"testing"
)

func TestRequireHarnessNames(t *testing.T) {
	cases := []struct {
		name    string
		chainID string
		runID   string
		prefix  string
		wantErr string
	}{
		{name: "valid", chainID: "bench-a", runID: "exp-42", prefix: "network-partition"},
		{name: "empty run-id", chainID: "bench-a", prefix: "seiload", wantErr: "--run-id is required"},
		{name: "uppercase run-id", chainID: "bench-a", runID: "Exp42", prefix: "seiload", wantErr: "--run-id"},
		{name: "dotted chain-id", chainID: "bench.a", runID: "exp-42", prefix: "seiload", wantErr: "--chain-id"},
		{name: "name overflows 63 chars", chainID: "bench-a", runID: strings.Repeat("a", 50), prefix: "network-partition", wantErr: "resource name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireHarnessNames(tc.chainID, tc.runID, tc.prefix)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
