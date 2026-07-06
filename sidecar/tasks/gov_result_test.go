package tasks

import (
	"testing"
	"time"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
)

func TestClassifyGovResult_CommittedOK(t *testing.T) {
	now := time.Now().UTC()
	out, err := classifyGovResult("gov-software-upgrade", &SignAndBroadcastResult{
		TxHash: "ABC", Height: 10, Code: 0, IncludedAt: &now, ProposalID: 5,
	})
	if err != nil {
		t.Fatalf("committed-ok should not error, got %v", err)
	}
	if out.InclusionStatus != InclusionCommittedOK || out.ProposalID != 5 || out.TxHash != "ABC" {
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
	if out.InclusionStatus != InclusionCommittedFailed || out.Code != 11 {
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
	if out.InclusionStatus != InclusionPending || out.TxHash != "ABC" {
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
