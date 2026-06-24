package main

import (
	"testing"
)

func TestComparePageRe(t *testing.T) {
	tests := []struct {
		key       string
		wantStart int64
		wantEnd   int64
		wantMatch bool
	}{
		{
			key:       "shadow-results/198000000-198000099.compare.ndjson.gz",
			wantStart: 198000000,
			wantEnd:   198000099,
			wantMatch: true,
		},
		{
			key:       "prefix/100-199.compare.ndjson.gz",
			wantStart: 100,
			wantEnd:   199,
			wantMatch: true,
		},
		{
			key:       "deep/nested/prefix/1-50.compare.ndjson.gz",
			wantStart: 1,
			wantEnd:   50,
			wantMatch: true,
		},
		{
			// Raw export page — must NOT match.
			key:       "shadow-results/198000000-198000099.ndjson.gz",
			wantMatch: false,
		},
		{
			// Divergence report — must NOT match.
			key:       "shadow-results/divergence-198032451.report.json.gz",
			wantMatch: false,
		},
		{
			// Unrelated file.
			key:       "shadow-results/checkpoint.json",
			wantMatch: false,
		},
		{
			// Empty key.
			key:       "",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := comparePageRe.FindStringSubmatch(tt.key)
			if !tt.wantMatch {
				if len(m) >= 3 {
					t.Fatalf("expected no match for %q, got %v", tt.key, m)
				}
				return
			}
			if len(m) < 3 {
				t.Fatalf("expected match for %q, got nil", tt.key)
			}
			// m[1] and m[2] parsed by strconv in the real code; verify they're numeric.
			if m[1] == "" || m[2] == "" {
				t.Fatalf("empty capture groups for %q", tt.key)
			}
		})
	}
}

func TestEndpointDigestRe(t *testing.T) {
	tests := []struct {
		key      string
		wantH    string
		wantNorm string
		want     bool
	}{
		{
			key:      "shadow-results/endpoint-digest-198032451-semantic.json.gz",
			wantH:    "198032451",
			wantNorm: "semantic",
			want:     true,
		},
		{
			key:      "prefix/endpoint-digest-1-translator.json.gz",
			wantH:    "1",
			wantNorm: "translator",
			want:     true,
		},
		{
			// Divergence report — must NOT match.
			key:  "shadow-results/divergence-198032451.report.json.gz",
			want: false,
		},
		{
			// Comparison page — must NOT match.
			key:  "shadow-results/198000000-198000099.compare.ndjson.gz",
			want: false,
		},
		{
			key:  "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := endpointDigestRe.FindStringSubmatch(tt.key)
			if !tt.want {
				if len(m) >= 3 {
					t.Fatalf("expected no match for %q, got %v", tt.key, m)
				}
				return
			}
			if len(m) < 3 {
				t.Fatalf("expected match for %q, got nil", tt.key)
			}
			if m[1] != tt.wantH {
				t.Errorf("height = %q, want %q", m[1], tt.wantH)
			}
			if m[2] != tt.wantNorm {
				t.Errorf("normalization = %q, want %q", m[2], tt.wantNorm)
			}
		})
	}
}

func TestDivergenceReportRe(t *testing.T) {
	tests := []struct {
		key        string
		wantHeight string
		wantMatch  bool
	}{
		{
			key:        "shadow-results/divergence-198032451.report.json.gz",
			wantHeight: "198032451",
			wantMatch:  true,
		},
		{
			key:        "prefix/divergence-1.report.json.gz",
			wantHeight: "1",
			wantMatch:  true,
		},
		{
			// Comparison page — must NOT match.
			key:       "shadow-results/198000000-198000099.compare.ndjson.gz",
			wantMatch: false,
		},
		{
			// Raw export page — must NOT match.
			key:       "shadow-results/198000000-198000099.ndjson.gz",
			wantMatch: false,
		},
		{
			key:       "",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := divergenceReportRe.FindStringSubmatch(tt.key)
			if !tt.wantMatch {
				if len(m) >= 2 {
					t.Fatalf("expected no match for %q, got %v", tt.key, m)
				}
				return
			}
			if len(m) < 2 {
				t.Fatalf("expected match for %q, got nil", tt.key)
			}
			if m[1] != tt.wantHeight {
				t.Errorf("height = %q, want %q", m[1], tt.wantHeight)
			}
		})
	}
}
