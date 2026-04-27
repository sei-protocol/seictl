package aws

import (
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/internal/clioutput"
)

func TestParseImageRef(t *testing.T) {
	tests := map[string]struct {
		ref        string
		wantHost   string
		wantRepo   string
		wantTag    string
		wantDigest string
		wantErrCat string
	}{
		"with tag": {
			ref:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1.2.3",
			wantHost: "189176372795.dkr.ecr.us-east-2.amazonaws.com",
			wantRepo: "sei/sei-chain",
			wantTag:  "v1.2.3",
		},
		"with digest": {
			ref:        "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain@sha256:" + strings.Repeat("a", 64),
			wantHost:   "189176372795.dkr.ecr.us-east-2.amazonaws.com",
			wantRepo:   "sei/sei-chain",
			wantDigest: "sha256:" + strings.Repeat("a", 64),
		},
		"missing slash":     {ref: "sei-chain:v1", wantErrCat: clioutput.CatImageResolution},
		"no tag or digest":  {ref: "host.example.com/sei/sei-chain", wantErrCat: clioutput.CatImageResolution},
		"non-sha256 digest": {ref: "host.example.com/sei/sei-chain@md5:abc", wantErrCat: clioutput.CatImageResolution},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			host, repo, tag, digest, err := parseImageRef(tc.ref)
			if tc.wantErrCat != "" {
				if err == nil || err.Category != tc.wantErrCat {
					t.Fatalf("error: got %+v, want category %q", err, tc.wantErrCat)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %+v", err)
			}
			if host != tc.wantHost || repo != tc.wantRepo || tag != tc.wantTag || digest != tc.wantDigest {
				t.Errorf("got (%q,%q,%q,%q)", host, repo, tag, digest)
			}
		})
	}
}

func TestParseECRHost(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		account, region, err := parseECRHost("189176372795.dkr.ecr.us-east-2.amazonaws.com")
		if err != nil {
			t.Fatalf("err: %+v", err)
		}
		if account != "189176372795" || region != "us-east-2" {
			t.Errorf("got %q / %q", account, region)
		}
	})
	t.Run("not ECR", func(t *testing.T) {
		_, _, err := parseECRHost("docker.io")
		if err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestAssertECRDigestRef(t *testing.T) {
	good := "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain@sha256:" + strings.Repeat("a", 64)
	if err := AssertECRDigestRef(good); err != nil {
		t.Errorf("digest ref should pass: %v", err)
	}
	if err := AssertECRDigestRef("189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1"); err == nil {
		t.Errorf("tag ref must fail")
	}
}

func TestResolveDigest_PassThroughDigest(t *testing.T) {
	// Already digested — must not call ECR.
	ref := "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain@sha256:" + strings.Repeat("b", 64)
	got, err := ResolveDigest(nil, ref)
	if err != nil {
		t.Fatalf("err: %+v", err)
	}
	if got != "sha256:"+strings.Repeat("b", 64) {
		t.Errorf("got %q", got)
	}
}
