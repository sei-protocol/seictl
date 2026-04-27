package validate

import (
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/internal/clioutput"
)

func TestAlias(t *testing.T) {
	tests := map[string]struct {
		in       string
		wantErr  bool
		category string
	}{
		"single letter":         {in: "a"},
		"alphanumeric":          {in: "bdc"},
		"with digits":           {in: "abc123"},
		"with hyphen":           {in: "ab-cd"},
		"max length 30":         {in: strings.Repeat("a", 30)},
		"empty":                 {in: "", wantErr: true, category: clioutput.CatAliasInvalid},
		"starts with digit":     {in: "1abc", wantErr: true, category: clioutput.CatAliasInvalid},
		"starts with hyphen":    {in: "-abc", wantErr: true, category: clioutput.CatAliasInvalid},
		"ends with hyphen":      {in: "abc-", wantErr: true, category: clioutput.CatAliasInvalid},
		"underscore":            {in: "ab_c", wantErr: true, category: clioutput.CatAliasInvalid},
		"uppercase":             {in: "BAD", wantErr: true, category: clioutput.CatAliasInvalid},
		"too long":              {in: strings.Repeat("a", 31), wantErr: true, category: clioutput.CatAliasInvalid},
		"reserved kube-system":  {in: "kube-system", wantErr: true, category: clioutput.CatAliasInvalid},
		"reserved default":      {in: "default", wantErr: true, category: clioutput.CatAliasInvalid},
		"reserved autobake":     {in: "autobake", wantErr: true, category: clioutput.CatAliasInvalid},
		"reserved flux-system":  {in: "flux-system", wantErr: true, category: clioutput.CatAliasInvalid},
		"reserved istio-system": {in: "istio-system", wantErr: true, category: clioutput.CatAliasInvalid},
		"reserved tide-agents":  {in: "tide-agents", wantErr: true, category: clioutput.CatAliasInvalid},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Alias(tc.in)
			if (got != nil) != tc.wantErr {
				t.Errorf("Alias(%q) err=%v, wantErr=%v", tc.in, got, tc.wantErr)
			}
			if tc.wantErr && got != nil && got.Category != tc.category {
				t.Errorf("category: got %q, want %q", got.Category, tc.category)
			}
		})
	}
}

func TestName(t *testing.T) {
	tests := map[string]struct {
		alias, name string
		wantErr     bool
	}{
		// Name regex caps `name` at 40 chars; the combined-too-long path
		// is only reachable when `alias` is also long.
		"basic":              {alias: "bdc", name: "demo"},
		"name with digits":   {alias: "bdc", name: "v2-demo"},
		"max name 40":        {alias: "bdc", name: strings.Repeat("a", 40)},
		"name 41 fails":      {alias: "bdc", name: strings.Repeat("a", 41), wantErr: true},
		"combined too long":  {alias: strings.Repeat("a", 30), name: strings.Repeat("b", 30), wantErr: true},
		"name uppercase":     {alias: "bdc", name: "Demo", wantErr: true},
		"name starts with -": {alias: "bdc", name: "-demo", wantErr: true},
		"name ends with -":   {alias: "bdc", name: "demo-", wantErr: true},
		"name empty":         {alias: "bdc", name: "", wantErr: true},
	}
	for tn, tc := range tests {
		t.Run(tn, func(t *testing.T) {
			got := Name(tc.alias, tc.name)
			if (got != nil) != tc.wantErr {
				t.Errorf("Name(%q,%q) err=%v, wantErr=%v", tc.alias, tc.name, got, tc.wantErr)
			}
		})
	}
}

func TestSize(t *testing.T) {
	for _, ok := range []string{"s", "m", "l"} {
		if err := Size(ok); err != nil {
			t.Errorf("Size(%q) unexpected err: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "x", "S", "small", "xl"} {
		if err := Size(bad); err == nil {
			t.Errorf("Size(%q) should fail", bad)
		}
	}
}

func TestDurationMinutes(t *testing.T) {
	tests := map[string]struct {
		n       int
		wantErr bool
	}{
		"min":      {n: 1},
		"max":      {n: 240},
		"middle":   {n: 60},
		"zero":     {n: 0, wantErr: true},
		"negative": {n: -1, wantErr: true},
		"over max": {n: 241, wantErr: true},
		"way over": {n: 10000, wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := DurationMinutes(tc.n)
			if (got != nil) != tc.wantErr {
				t.Errorf("DurationMinutes(%d) err=%v, wantErr=%v", tc.n, got, tc.wantErr)
			}
		})
	}
}

func TestNamespace(t *testing.T) {
	tests := map[string]struct {
		ns, alias string
		wantErr   bool
		category  string
	}{
		"matches alias":      {ns: "eng-bdc", alias: "bdc"},
		"read-only no alias": {ns: "eng-bdc", alias: ""},
		"alias mismatch":     {ns: "eng-other", alias: "bdc", wantErr: true, category: clioutput.CatNamespacePolicy},
		"too long":           {ns: strings.Repeat("a", 64), alias: "", wantErr: true, category: clioutput.CatNamespacePolicy},
		"uppercase":          {ns: "Bad", alias: "", wantErr: true, category: clioutput.CatNamespacePolicy},
		"underscore":         {ns: "bad_ns", alias: "", wantErr: true, category: clioutput.CatNamespacePolicy},
		"starts with hyphen": {ns: "-bad", alias: "", wantErr: true, category: clioutput.CatNamespacePolicy},
		"empty":              {ns: "", alias: "", wantErr: true, category: clioutput.CatNamespacePolicy},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Namespace(tc.ns, tc.alias)
			if (got != nil) != tc.wantErr {
				t.Errorf("Namespace(%q,%q) err=%v, wantErr=%v", tc.ns, tc.alias, got, tc.wantErr)
			}
			if tc.wantErr && got != nil && got.Category != tc.category {
				t.Errorf("category: got %q, want %q", got.Category, tc.category)
			}
		})
	}
}

func TestImage(t *testing.T) {
	good := AllowedRegistry + "/" + AllowedRepoPrefix
	tests := map[string]struct {
		ref     string
		wantErr bool
	}{
		"with tag":           {ref: good + "sei-chain:v1.2.3"},
		"with digest":        {ref: good + "sei-chain@sha256:" + strings.Repeat("a", 64)},
		"empty":              {ref: "", wantErr: true},
		"no slash":           {ref: "sei-chain:v1", wantErr: true},
		"wrong registry":     {ref: "docker.io/sei/sei-chain:v1", wantErr: true},
		"missing sei prefix": {ref: AllowedRegistry + "/other/foo:v1", wantErr: true},
		"empty repo":         {ref: AllowedRegistry + "/sei/", wantErr: true},
		"no tag or digest":   {ref: good + "sei-chain", wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Image(tc.ref)
			if (got != nil) != tc.wantErr {
				t.Errorf("Image(%q) err=%v, wantErr=%v", tc.ref, got, tc.wantErr)
			}
			if got != nil && got.Category != clioutput.CatImagePolicy {
				t.Errorf("category: got %q, want %q", got.Category, clioutput.CatImagePolicy)
			}
		})
	}
}
