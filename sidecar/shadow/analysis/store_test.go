package analysis

import (
	"testing"
)

func TestResolveRef(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		bucket     string
		prefix     string
		region     string
		wantBucket string
		wantPrefix string
		wantRegion string
		wantErr    bool
	}{
		{
			name:       "env only",
			env:        "prod",
			wantBucket: "prod-sei-shadow-results",
			wantPrefix: "shadow-results/",
			wantRegion: "eu-central-1",
		},
		{
			name:       "bucket only",
			bucket:     "my-bucket",
			wantBucket: "my-bucket",
			wantPrefix: "shadow-results/",
			wantRegion: "eu-central-1",
		},
		{
			name:    "both set",
			env:     "prod",
			bucket:  "my-bucket",
			wantErr: true,
		},
		{
			name:    "neither set",
			wantErr: true,
		},
		{
			name:       "custom prefix without trailing slash",
			env:        "dev",
			prefix:     "custom",
			wantBucket: "dev-sei-shadow-results",
			wantPrefix: "custom/",
			wantRegion: "eu-central-1",
		},
		{
			name:       "custom prefix with trailing slash",
			env:        "dev",
			prefix:     "custom/",
			wantBucket: "dev-sei-shadow-results",
			wantPrefix: "custom/",
			wantRegion: "eu-central-1",
		},
		{
			name:       "custom region",
			env:        "prod",
			region:     "us-east-2",
			wantBucket: "prod-sei-shadow-results",
			wantPrefix: "shadow-results/",
			wantRegion: "us-east-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, prefix, region, err := ResolveRef(tt.env, tt.bucket, tt.prefix, tt.region)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bucket != tt.wantBucket {
				t.Errorf("bucket = %q, want %q", bucket, tt.wantBucket)
			}
			if prefix != tt.wantPrefix {
				t.Errorf("prefix = %q, want %q", prefix, tt.wantPrefix)
			}
			if region != tt.wantRegion {
				t.Errorf("region = %q, want %q", region, tt.wantRegion)
			}
		})
	}
}

func TestParseComparePageKey(t *testing.T) {
	tests := []struct {
		key       string
		wantStart int64
		wantEnd   int64
		wantOK    bool
	}{
		{
			key:       "shadow-results/198000000-198000099.compare.ndjson.gz",
			wantStart: 198000000,
			wantEnd:   198000099,
			wantOK:    true,
		},
		{
			key:    "shadow-results/198000000-198000099.ndjson.gz",
			wantOK: false,
		},
		{
			key:    "shadow-results/divergence-198032451.report.json.gz",
			wantOK: false,
		},
		{
			key:       "prefix/100-199.compare.ndjson.gz",
			wantStart: 100,
			wantEnd:   199,
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			entries := []s3Entry{{key: tt.key, sizeBytes: 1024}}
			pages := parsePages(entries)
			if !tt.wantOK {
				if len(pages) != 0 {
					t.Fatalf("expected no pages, got %d", len(pages))
				}
				return
			}
			if len(pages) != 1 {
				t.Fatalf("expected 1 page, got %d", len(pages))
			}
			if pages[0].StartHeight != tt.wantStart {
				t.Errorf("start = %d, want %d", pages[0].StartHeight, tt.wantStart)
			}
			if pages[0].EndHeight != tt.wantEnd {
				t.Errorf("end = %d, want %d", pages[0].EndHeight, tt.wantEnd)
			}
		})
	}
}

func TestParseDivergenceReportKey(t *testing.T) {
	tests := []struct {
		key        string
		wantHeight int64
		wantOK     bool
	}{
		{
			key:        "shadow-results/divergence-198032451.report.json.gz",
			wantHeight: 198032451,
			wantOK:     true,
		},
		{
			key:    "shadow-results/198000000-198000099.compare.ndjson.gz",
			wantOK: false,
		},
		{
			key:    "shadow-results/198000000-198000099.ndjson.gz",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			entries := []s3Entry{{key: tt.key, sizeBytes: 512}}
			reports := parseDivergenceReports(entries)
			if !tt.wantOK {
				if len(reports) != 0 {
					t.Fatalf("expected no reports, got %d", len(reports))
				}
				return
			}
			if len(reports) != 1 {
				t.Fatalf("expected 1 report, got %d", len(reports))
			}
			if reports[0].Height != tt.wantHeight {
				t.Errorf("height = %d, want %d", reports[0].Height, tt.wantHeight)
			}
		})
	}
}
