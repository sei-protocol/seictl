package tasks

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
)

func validSoftwareUpgradeRequest() GovSubmitProposalRequest {
	return GovSubmitProposalRequest{
		ChainID:        "pacific-1",
		KeyName:        "node_admin",
		Type:           ProposalTypeSoftwareUpgrade,
		Title:          "Upgrade to v0.42",
		Description:    "Routine release; binaries published in release notes.",
		UpgradeName:    "v0.42",
		UpgradeHeight:  10_000_000,
		UpgradeInfo:    "https://github.com/sei-protocol/sei-chain/releases/tag/v0.42",
		InitialDeposit: "10000000usei",
		Fees:           "4000usei",
		Gas:            300_000,
	}
}

func TestBuildProposalContent(t *testing.T) {
	t.Run("software-upgrade happy path", func(t *testing.T) {
		c, err := buildProposalContent(validSoftwareUpgradeRequest())
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		sup, ok := c.(*upgradetypes.SoftwareUpgradeProposal)
		if !ok {
			t.Fatalf("content type = %T, want *SoftwareUpgradeProposal", c)
		}
		if sup.Plan.Name != "v0.42" {
			t.Errorf("plan.Name = %q, want v0.42", sup.Plan.Name)
		}
		if sup.Plan.Height != 10_000_000 {
			t.Errorf("plan.Height = %d, want 10_000_000", sup.Plan.Height)
		}
		// Lock that the SDK's own ValidateBasic accepts what we produce
		// — signAndBroadcast runs it immediately after.
		if err := c.ValidateBasic(); err != nil {
			t.Errorf("ValidateBasic on returned content: %v", err)
		}
	})

	mutations := []struct {
		name      string
		mutate    func(*GovSubmitProposalRequest)
		wantInErr string
	}{
		{"missing type", func(r *GovSubmitProposalRequest) { r.Type = "" }, "type required"},
		{"unknown type", func(r *GovSubmitProposalRequest) { r.Type = "treasury-spend" }, "unsupported"},
		{"missing title", func(r *GovSubmitProposalRequest) { r.Title = "" }, "title required"},
		{"missing description", func(r *GovSubmitProposalRequest) { r.Description = "" }, "description required"},
		{"missing upgradeName", func(r *GovSubmitProposalRequest) { r.UpgradeName = "" }, "upgradeName required"},
		{"zero upgradeHeight", func(r *GovSubmitProposalRequest) { r.UpgradeHeight = 0 }, "upgradeHeight required"},
		{"negative upgradeHeight", func(r *GovSubmitProposalRequest) { r.UpgradeHeight = -1 }, "upgradeHeight required"},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			req := validSoftwareUpgradeRequest()
			m.mutate(&req)
			_, err := buildProposalContent(req)
			if err == nil {
				t.Fatalf("want err containing %q, got nil", m.wantInErr)
			}
			if !strings.Contains(err.Error(), m.wantInErr) {
				t.Fatalf("err = %q, want substring %q", err.Error(), m.wantInErr)
			}
		})
	}
}

func TestBuildSubmitProposalMsg(t *testing.T) {
	kr, addr := testKeyring(t)
	cfg := engine.ExecutionConfig{Keyring: kr}

	t.Run("happy path", func(t *testing.T) {
		msg, err := buildSubmitProposalMsg(cfg, validSoftwareUpgradeRequest())
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
		// Lock that the SDK's MsgSubmitProposal.ValidateBasic accepts
		// what we produce — signAndBroadcast runs it next.
		if err := msg.ValidateBasic(); err != nil {
			t.Errorf("ValidateBasic on returned msg: %v", err)
		}
	})

	t.Run("nil keyring is Terminal", func(t *testing.T) {
		_, err := buildSubmitProposalMsg(engine.ExecutionConfig{}, validSoftwareUpgradeRequest())
		if !IsTerminal(err) {
			t.Fatalf("want Terminal, got %v", err)
		}
	})

	t.Run("missing key is Terminal", func(t *testing.T) {
		req := validSoftwareUpgradeRequest()
		req.KeyName = "ghost"
		_, err := buildSubmitProposalMsg(cfg, req)
		if !IsTerminal(err) {
			t.Fatalf("want Terminal, got %v", err)
		}
	})

	t.Run("unparseable deposit is Terminal", func(t *testing.T) {
		req := validSoftwareUpgradeRequest()
		req.InitialDeposit = "not-a-coin"
		_, err := buildSubmitProposalMsg(cfg, req)
		if !IsTerminal(err) {
			t.Fatalf("want Terminal, got %v", err)
		}
	})

	t.Run("zero deposit is Terminal", func(t *testing.T) {
		req := validSoftwareUpgradeRequest()
		req.InitialDeposit = "0usei"
		_, err := buildSubmitProposalMsg(cfg, req)
		if !IsTerminal(err) {
			t.Fatalf("want Terminal, got %v", err)
		}
	})

	t.Run("non-usei deposit is Terminal", func(t *testing.T) {
		req := validSoftwareUpgradeRequest()
		req.InitialDeposit = "10sei" // wrong denom — chain would reject too, but cheaper to catch here
		_, err := buildSubmitProposalMsg(cfg, req)
		if !IsTerminal(err) {
			t.Fatalf("want Terminal, got %v", err)
		}
		if !strings.Contains(err.Error(), "usei") {
			t.Fatalf("error should mention usei, got %q", err.Error())
		}
	})
}

// TestGovSubmitProposalHandler_HappyPath threads the handler end-to-end
// through signAndBroadcast with a fake txClient, catching any drift
// between MsgSubmitProposal construction and the sign-and-broadcast
// scaffolding — particularly InterfaceRegistry registration of
// SoftwareUpgradeProposal under govtypes.Content.
func TestGovSubmitProposalHandler_HappyPath(t *testing.T) {
	cfg, _ := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "h", Height: 0},
		queryDefault:  &sdk.TxResponse{Code: 0, Height: 7},
	}

	msg, err := buildSubmitProposalMsg(cfg, validSoftwareUpgradeRequest())
	if err != nil {
		t.Fatalf("buildSubmitProposalMsg: %v", err)
	}
	info, err := cfg.Keyring.Key("node_admin")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	result, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     msg,
		Fees:    "4000usei",
		Gas:     300_000,
		TaskID:  "00000000-0000-0000-0000-0000000000aa",
	}, info.GetAddress())
	if err != nil {
		t.Fatalf("signAndBroadcast: %v", err)
	}
	if result.TxHash != "h" {
		t.Errorf("TxHash = %q, want %q", result.TxHash, "h")
	}
	if tc.broadcasts != 1 {
		t.Errorf("broadcasts = %d, want 1", tc.broadcasts)
	}
	// MsgSubmitProposal packs the content as an Any, so reaching the
	// broadcast step at all proves newSignTxInterfaceRegistry has the
	// upgrade-types registration; a missing registration fails in
	// txCfg.TxEncoder before the fakeTxClient ever sees bytes.
}
