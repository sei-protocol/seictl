package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
)

func TestRunGovVote_Happy(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "ignored-overwritten", Height: 0},
		queryDefault:  &sdk.TxResponse{Code: 0, Height: 200, GasUsed: 142318, GasWanted: 200000},
	}
	installFactory(t, tc)

	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000010")
	res, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "yes",
	})
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if res.MsgType != "/cosmos.gov.v1beta1.MsgVote" {
		t.Fatalf("MsgType = %q, want /cosmos.gov.v1beta1.MsgVote", res.MsgType)
	}
	if res.ProposalID != 42 {
		t.Fatalf("ProposalID = %d, want 42", res.ProposalID)
	}
	if got, want := res.Option, govtypes.OptionYes.String(); got != want {
		t.Fatalf("Option = %q, want %q", got, want)
	}
	if res.Code != 0 {
		t.Fatalf("Code = %d, want 0", res.Code)
	}
	if res.ChainID != "pacific-1" {
		t.Fatalf("ChainID = %q", res.ChainID)
	}
	if res.TxHash == "" || res.TxHash != strings.ToUpper(res.TxHash) {
		t.Fatalf("TxHash must be uppercase hex: %q", res.TxHash)
	}
}

func TestRunGovVote_ChainConfusionGuard(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})
	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000011")
	_, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "wrong-chain",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "yes",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal, got %v", err)
	}
}

func TestRunGovVote_RejectsNonUSeiFees(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})
	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000012")
	_, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "yes",
		Fees:       "20sei", // the documented seienv vote.go:15 latent bug
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal denom error, got %v", err)
	}
}

func TestRunGovVote_InvalidOption(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})
	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000013")
	_, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "maybe",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal invalid-option error, got %v", err)
	}
}

func TestRunGovVote_MissingKey(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})
	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000014")
	_, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "ghost",
		ProposalID: 42,
		Option:     "yes",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal missing-key error, got %v", err)
	}
}

func TestRunGovVote_EmptyKeyName(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})
	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000015")
	_, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "",
		ProposalID: 42,
		Option:     "yes",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal missing-keyName error, got %v", err)
	}
}

func TestRunGovVote_IdempotentRetry_NoSecondBroadcast(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "set-by-helper", Height: 0},
		queryDefault:  &sdk.TxResponse{Code: 0, Height: 200, GasUsed: 142318, GasWanted: 200000},
	}
	installFactory(t, tc)

	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000016")
	res1, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "yes",
	})
	if err != nil {
		t.Fatalf("first vote: %v", err)
	}
	firstBroadcasts := tc.broadcasts

	res2, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "yes",
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if tc.broadcasts != firstBroadcasts {
		t.Fatalf("idempotent retry must NOT trigger a second broadcast: %d > %d", tc.broadcasts, firstBroadcasts)
	}
	if res1.TxHash != res2.TxHash {
		t.Fatalf("retry produced different hash: %q vs %q", res2.TxHash, res1.TxHash)
	}
}

func TestRunGovVote_PassesThroughSignAndBroadcastErrors(t *testing.T) {
	// Regression: a transport-level broadcast failure should bubble
	// through runGovVote without being silently swallowed.
	cfg, _ := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastErr:  errors.New("seid down"),
	}
	installFactory(t, tc)

	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000017")
	_, err := runGovVote(ctx, cfg, GovVoteParams{
		ChainID:    "pacific-1",
		KeyName:    "node_admin",
		ProposalID: 42,
		Option:     "yes",
	})
	if err == nil || !strings.Contains(err.Error(), "broadcast") {
		t.Fatalf("expected broadcast error to propagate, got %v", err)
	}
}

func TestGovVoteHandler_Wires(t *testing.T) {
	// End-to-end through the engine.TypedHandler dispatch — confirms
	// the params get unmarshaled correctly from map[string]any.
	cfg, _ := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, Height: 0},
		queryDefault:  &sdk.TxResponse{Code: 0, Height: 200},
	}
	installFactory(t, tc)

	handler := GovVoteHandler(func() engine.ExecutionConfig { return cfg })
	ctx := engine.WithTaskID(context.Background(), "00000000-0000-0000-0000-000000000018")
	err := handler(ctx, map[string]any{
		"chainId":    "pacific-1",
		"keyName":    "node_admin",
		"proposalId": float64(42), // JSON unmarshals integers as float64
		"option":     "yes",
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if tc.broadcasts != 1 {
		t.Fatalf("expected one broadcast, got %d", tc.broadcasts)
	}
}
