package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/sidecar/tasks"
)

func TestRenderDigestMarkdown(t *testing.T) {
	// Decode from JSON so the test does not depend on the unexported per-bucket
	// value type, exercising the same shape fetchDigestRecord produces.
	const recordJSON = `{
		"height": 198740042,
		"normalization": "semantic",
		"flatkv_digest": "aaaaaaaabbbbbbbbccccccccdddddddd",
		"memiavl_digest": "aaaaaaaabbbbbbbbccccccccdddddddd",
		"per_bucket": {
			"account": {"flatkv": "11111111aaaa", "memiavl": "11111111aaaa", "match": true},
			"code":    {"flatkv": "22222222bbbb", "memiavl": "33333333cccc", "match": false},
			"storage": {"flatkv": "44444444dddd", "memiavl": "44444444dddd", "match": true},
			"legacy":  {"flatkv": "55555555eeee", "memiavl": "55555555eeee", "match": true}
		},
		"match": false,
		"axes_proved": ["nonce", "code", "code_hash", "storage", "legacy"],
		"generated_at": "2026-06-17T00:00:00Z"
	}`

	var record tasks.EndpointDigestRecord
	if err := json.Unmarshal([]byte(recordJSON), &record); err != nil {
		t.Fatalf("unmarshalling record: %v", err)
	}

	out := renderDigestMarkdown(&record)

	wantContains := []string{
		"# EVM Logical Digest — Height 198740042 (semantic)",
		"**Generated at:** 2026-06-17T00:00:00Z",
		"**Overall match:** ❌",
		"## Per-Bucket Digests",
		"| account |",
		"| code |",
		"| storage |",
		"| legacy |",
		"**Axes proved:** code, code_hash, legacy, nonce, storage",
	}
	for _, w := range wantContains {
		if !strings.Contains(out, w) {
			t.Errorf("rendered output missing %q\n--- output ---\n%s", w, out)
		}
	}
}

func TestResolveS3Ref(t *testing.T) {
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
			name:       "env expands to bucket name",
			env:        "prod",
			wantBucket: "prod-sei-shadow-results",
			wantPrefix: "shadow-results/",
			wantRegion: "eu-central-1",
		},
		{
			name:       "bucket passed directly",
			bucket:     "my-custom-bucket",
			wantBucket: "my-custom-bucket",
			wantPrefix: "shadow-results/",
			wantRegion: "eu-central-1",
		},
		{
			name:    "env and bucket are mutually exclusive",
			env:     "prod",
			bucket:  "my-bucket",
			wantErr: true,
		},
		{
			name:    "neither env nor bucket",
			wantErr: true,
		},
		{
			name:       "custom prefix gets trailing slash",
			env:        "dev",
			prefix:     "custom-prefix",
			wantBucket: "dev-sei-shadow-results",
			wantPrefix: "custom-prefix/",
			wantRegion: "eu-central-1",
		},
		{
			name:       "custom prefix with trailing slash unchanged",
			env:        "dev",
			prefix:     "custom-prefix/",
			wantBucket: "dev-sei-shadow-results",
			wantPrefix: "custom-prefix/",
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
		{
			name:       "empty prefix defaults",
			env:        "staging",
			prefix:     "",
			wantBucket: "staging-sei-shadow-results",
			wantPrefix: "shadow-results/",
			wantRegion: "eu-central-1",
		},
		{
			name:       "empty region defaults",
			env:        "prod",
			region:     "",
			wantBucket: "prod-sei-shadow-results",
			wantPrefix: "shadow-results/",
			wantRegion: "eu-central-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, prefix, region, err := resolveS3Ref(tt.env, tt.bucket, tt.prefix, tt.region)
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
