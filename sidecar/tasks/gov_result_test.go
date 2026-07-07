package tasks

import (
	"testing"
	"time"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"
	abci "github.com/sei-protocol/sei-chain/sei-tendermint/abci/types"

	"github.com/sei-protocol/seictl/sidecar/wire"
)

func TestClassifyGovResult_CommittedOK(t *testing.T) {
	now := time.Now().UTC()
	out, err := classifyGovResult("gov-software-upgrade", &SignAndBroadcastResult{
		TxHash: "ABC", Height: 10, Code: 0, IncludedAt: &now, ProposalID: 5,
	})
	if err != nil {
		t.Fatalf("committed-ok should not error, got %v", err)
	}
	if out.InclusionStatus != wire.InclusionCommittedOK || out.ProposalID != 5 || out.TxHash != "ABC" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestClassifyGovResult_CommittedFailed_Terminal(t *testing.T) {
	now := time.Now().UTC()
	out, err := classifyGovResult("gov-param-change", &SignAndBroadcastResult{
		TxHash: "ABC", Height: 10, Code: 11, Codespace: "sdk", RawLog: "insufficient funds", IncludedAt: &now,
	})
	if err == nil || !IsTerminal(err) {
		t.Fatalf("committed-failed should be a terminal error, got %v", err)
	}
	if out.InclusionStatus != wire.InclusionCommittedFailed || out.Code != 11 {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestClassifyGovResult_Pending_NonTerminal(t *testing.T) {
	out, err := classifyGovResult("gov-vote", &SignAndBroadcastResult{
		TxHash: "ABC", IncludedAt: nil,
	})
	if err == nil || IsTerminal(err) {
		t.Fatalf("pending should be a non-terminal error (controller re-submits), got %v", err)
	}
	if out.InclusionStatus != wire.InclusionPending || out.TxHash != "ABC" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestParseProposalID(t *testing.T) {
	resp := &sdk.TxResponse{Logs: sdk.ABCIMessageLogs{{
		Events: sdk.StringEvents{{
			Type: "submit_proposal",
			Attributes: []sdk.Attribute{
				{Key: "proposal_id", Value: "42"},
			},
		}},
	}}}
	if got := parseProposalID(resp); got != 42 {
		t.Fatalf("proposal id = %d, want 42", got)
	}
	if got := parseProposalID(&sdk.TxResponse{}); got != 0 {
		t.Fatalf("absent event should yield 0, got %d", got)
	}
}

// B2: a /tx response may carry the event as raw ABCI events with byte-keyed
// attributes (SDK-version dependent) rather than parsed Logs.
func TestParseProposalID_FromRawEvents(t *testing.T) {
	resp := &sdk.TxResponse{Events: []abci.Event{{
		Type: "submit_proposal",
		Attributes: []abci.EventAttribute{
			{Key: []byte("proposal_id"), Value: []byte("42")},
		},
	}}}
	if got := parseProposalID(resp); got != 42 {
		t.Fatalf("proposal id from raw events = %d, want 42", got)
	}
}

// TestVoteOptionValuesMatchGovtypes guards the wire.VoteOption consts against
// drift from the cosmos govtypes enum the server casts to
// (govtypes.VoteOption(wire.Option*)). A renumber would silently mismap a vote.
func TestVoteOptionValuesMatchGovtypes(t *testing.T) {
	cases := []struct {
		w wire.VoteOption
		g govtypes.VoteOption
	}{
		{wire.OptionEmpty, govtypes.OptionEmpty},
		{wire.OptionYes, govtypes.OptionYes},
		{wire.OptionAbstain, govtypes.OptionAbstain},
		{wire.OptionNo, govtypes.OptionNo},
		{wire.OptionNoWithVeto, govtypes.OptionNoWithVeto},
	}
	for _, c := range cases {
		if int(c.w) != int(c.g) {
			t.Fatalf("wire.VoteOption %d != govtypes %d — ParseVoteOption cast would mismap", c.w, c.g)
		}
	}
}
