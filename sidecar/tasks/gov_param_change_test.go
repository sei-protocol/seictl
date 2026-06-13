package tasks

import (
	"strings"
	"testing"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	proposal "github.com/sei-protocol/sei-chain/sei-cosmos/x/params/types/proposal"

	"github.com/sei-protocol/seictl/sidecar/engine"
)

func validParamChangeRequest() GovParamChangeRequest {
	return GovParamChangeRequest{
		ChainID:     "arctic-1",
		KeyName:     "node_admin",
		Title:       "Update Consensus Timeout Params",
		Description: "Tighten consensus timeouts.",
		Changes: []paramChange{
			// struct-valued param (object)
			{Subspace: "baseapp", Key: "TimeoutParams", Value: []byte(`{"propose":"300000000","commit":"200000000"}`)},
		},
		InitialDeposit: "10000000usei",
		Fees:           "8000usei",
		Gas:            300_000,
	}
}

func TestBuildParamChangeMsg(t *testing.T) {
	kr, addr := testKeyring(t)
	cfg := engine.ExecutionConfig{Keyring: kr}

	t.Run("happy path", func(t *testing.T) {
		msg, err := buildParamChangeMsg(cfg, validParamChangeRequest())
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if msg.Proposer != addr.String() {
			t.Errorf("proposer = %q, want %q", msg.Proposer, addr.String())
		}
		want, _ := sdk.ParseCoinsNormalized("10000000usei")
		if !msg.InitialDeposit.IsEqual(want) {
			t.Errorf("InitialDeposit = %v, want %v", msg.InitialDeposit, want)
		}
		if err := msg.ValidateBasic(); err != nil {
			t.Errorf("ValidateBasic on returned msg: %v", err)
		}
		if _, ok := msg.GetContent().(*proposal.ParameterChangeProposal); !ok {
			t.Errorf("content type = %T, want *ParameterChangeProposal", msg.GetContent())
		}
	})

	// Regression guard for the prop-252 double-encode bug: the raw JSON
	// value must reach ParamChange.Value stringified exactly ONCE, for any
	// JSON shape — an object or a bare scalar. A value of {"a":"b"} must
	// become {"a":"b"}, never "{\"a\":\"b\"}".
	t.Run("value single-encoded for object and scalar", func(t *testing.T) {
		cases := []struct {
			name, raw string
		}{
			{"object", `{"propose":"300000000","commit":"200000000"}`},
			{"scalar-string", `"86400000000000"`},
			{"scalar-number", `100`},
			{"scalar-bool", `true`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := validParamChangeRequest()
				req.Changes = []paramChange{{Subspace: "staking", Key: "K", Value: []byte(tc.raw)}}
				msg, err := buildParamChangeMsg(cfg, req)
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				pcp := msg.GetContent().(*proposal.ParameterChangeProposal)
				if got := pcp.Changes[0].Value; got != tc.raw {
					t.Errorf("ParamChange.Value = %q, want %q (double-encoded?)", got, tc.raw)
				}
			})
		}
	})

	t.Run("non-usei deposit rejected", func(t *testing.T) {
		req := validParamChangeRequest()
		req.InitialDeposit = "10000000uatom"
		if _, err := buildParamChangeMsg(cfg, req); err == nil {
			t.Fatal("expected error for non-usei deposit")
		} else if !strings.Contains(err.Error(), "not permitted") {
			t.Errorf("err = %v, want denom-not-permitted", err)
		}
	})

	t.Run("empty changes rejected", func(t *testing.T) {
		req := validParamChangeRequest()
		req.Changes = nil
		if _, err := buildParamChangeMsg(cfg, req); err == nil {
			t.Fatal("expected error for empty changes")
		}
	})
}
