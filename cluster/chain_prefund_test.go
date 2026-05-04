package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

const validSeiPrefund = "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"

func TestParsePrefundFlags(t *testing.T) {
	cases := []struct {
		name    string
		input   []string
		want    []PrefundedAccount
		wantErr string
	}{
		{name: "nil", input: nil},
		{name: "empty", input: []string{}},
		{name: "single", input: []string{"sei1abc=1000usei"}, want: []PrefundedAccount{{Address: "sei1abc", Balance: "1000usei"}}},
		{name: "multiple", input: []string{"sei1abc=1usei", "sei1def=2usei,3uatom"}, want: []PrefundedAccount{
			{Address: "sei1abc", Balance: "1usei"},
			{Address: "sei1def", Balance: "2usei,3uatom"},
		}},
		{name: "missing equals", input: []string{"sei1abc"}, wantErr: "expected addr=balance"},
		{name: "empty addr", input: []string{"=1usei"}, wantErr: "expected addr=balance"},
		{name: "empty balance", input: []string{"sei1abc="}, wantErr: "expected addr=balance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePrefundFlags(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error: got %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestRenderGenesisAccountsBlock(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := renderGenesisAccountsBlock(nil); got != "" {
			t.Errorf("nil → got %q, want empty", got)
		}
	})

	t.Run("single", func(t *testing.T) {
		got := renderGenesisAccountsBlock([]PrefundedAccount{{Address: "sei1abc", Balance: "1000usei"}})
		want := "\n    accounts:\n      - address: sei1abc\n        balance: 1000usei"
		if got != want {
			t.Errorf("single:\ngot  %q\nwant %q", got, want)
		}
	})

	t.Run("multiple", func(t *testing.T) {
		got := renderGenesisAccountsBlock([]PrefundedAccount{
			{Address: "sei1abc", Balance: "1usei"},
			{Address: "sei1def", Balance: "2usei"},
		})
		if !strings.Contains(got, "      - address: sei1abc") || !strings.Contains(got, "      - address: sei1def") {
			t.Errorf("multiple: got %q", got)
		}
	})
}

func TestRunChainUp_PrefundFlow(t *testing.T) {
	t.Run("envelope echoes prefunded accounts", func(t *testing.T) {
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
			Prefund:    []string{validSeiPrefund + "=1000000000usei"},
		}, &buf, stubChainDeps(t, "bdc"))
		if err != nil {
			t.Fatalf("runChainUpCmd: %v\n%s", err, buf.String())
		}

		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("envelope: %v", err)
		}
		var data chainUpResult
		_ = json.Unmarshal(env.Data, &data)

		if len(data.PrefundedAccounts) != 1 {
			t.Fatalf("PrefundedAccounts: got %d, want 1", len(data.PrefundedAccounts))
		}
		if data.PrefundedAccounts[0].Address != validSeiPrefund {
			t.Errorf("address: got %q", data.PrefundedAccounts[0].Address)
		}
	})

	t.Run("rendered manifest contains genesis.accounts list", func(t *testing.T) {
		docs, _, err := renderChainManifests("bdc", "qa", "eng-bdc", "bench-bdc-qa",
			"img@sha256:0", "0123456789ab", 4,
			[]PrefundedAccount{{Address: validSeiPrefund, Balance: "1000usei"}})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(docs) != 1 {
			t.Fatalf("expected 1 doc, got %d", len(docs))
		}
		body := string(docs[0])
		for _, want := range []string{
			"accounts:",
			"- address: " + validSeiPrefund,
			"balance: 1000usei",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rendered body missing %q\n%s", want, body)
			}
		}
		// Catch indent drift if chain.yaml's `genesis:` is ever re-nested.
		var parsed any
		if err := yaml.Unmarshal(docs[0], &parsed); err != nil {
			t.Errorf("rendered manifest is not valid YAML: %v", err)
		}
	})

	t.Run("rendered manifest omits accounts when no prefund", func(t *testing.T) {
		docs, _, err := renderChainManifests("bdc", "qa", "eng-bdc", "bench-bdc-qa",
			"img@sha256:0", "0123456789ab", 4, nil)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		// Match the rendered spec field, not the template's leading comment.
		if strings.Contains(string(docs[0]), "    accounts:") {
			t.Errorf("nil prefund should omit accounts; got:\n%s", string(docs[0]))
		}
	})

	t.Run("rejects malformed prefund", func(t *testing.T) {
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
			Prefund:    []string{"no-equals-sign"},
		}, &buf, stubChainDeps(t, "bdc"))
		if err == nil {
			t.Fatalf("expected error, got %s", buf.String())
		}
		if !strings.Contains(buf.String(), "addr=balance") {
			t.Errorf("error body: got %s", buf.String())
		}
	})
}
