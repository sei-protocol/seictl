// Package tasks — gov-submit-proposal handler.
//
// SECURITY POSTURE NOTE — sei-protocol/seictl#163 / #165
//
// Reached via the sidecar's HTTP API. In Phase 1-3 the API is
// unauthenticated; anyone with network reach can submit proposals as
// the validator's operator account. Phase 4 (#165) fronts the sidecar
// with kube-rbac-proxy. REMOVE THIS NOTICE when #165 lands.
//
// REHYDRATION WARNING — sei-protocol/seictl#174
//
// MsgSubmitProposal is NOT chain-idempotent: a crash between
// BroadcastSync and task-result persist re-runs this handler on
// pod restart, which re-signs at sequence+1 and broadcasts a SECOND
// proposal with identical content. The operator pays InitialDeposit
// twice and two near-identical proposals appear on chain.
//
// Engine UUID dedupe protects against same-UUID resubmission via the
// API; it does NOT protect against the crash-mid-broadcast window.
// MVP scope is software-upgrade only — the chain-level impact is
// bounded (upgrade module applies once at the named height) but the
// pattern propagates to higher-risk proposal types (community-pool
// spend = double-spend). #174 tracks the pre-broadcast txHash marker
// that closes this window for all sign-tx handlers.

package tasks

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"
	upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var govSubmitLog = seilog.NewLogger("seictl", "task", "gov-submit-proposal")

// ProposalTypeSoftwareUpgrade is the only proposal type supported in
// MVP. Other content types (param-change, community-pool-spend, text)
// share the same scaffolding but need separate review of their
// rehydration-double-broadcast impact before they can ship.
const ProposalTypeSoftwareUpgrade = "software-upgrade"

// GovSubmitProposalRequest holds gov-submit-proposal params. Idempotency
// is NOT handled — MsgSubmitProposal creates a new proposal on every
// broadcast. See the REHYDRATION WARNING at the top of this file.
type GovSubmitProposalRequest struct {
	ChainID string `json:"chainId"`
	KeyName string `json:"keyName"`

	Type string `json:"type"` // software-upgrade

	Title       string `json:"title"`
	Description string `json:"description"`

	UpgradeName   string `json:"upgradeName,omitempty"`
	UpgradeHeight int64  `json:"upgradeHeight,omitempty"`
	UpgradeInfo   string `json:"upgradeInfo,omitempty"`

	InitialDeposit string `json:"initialDeposit"`

	Memo string `json:"memo,omitempty"`
	Fees string `json:"fees"`
	Gas  uint64 `json:"gas"`
}

// GovProposer captures cfg by value at construction; engine.Config is
// documented read-only after startup, so the copy is safe.
type GovProposer struct {
	cfg engine.ExecutionConfig
}

func NewGovProposer(cfg engine.ExecutionConfig) *GovProposer {
	return &GovProposer{cfg: cfg}
}

// Handler delegates to SignAndBroadcast after MsgSubmitProposal
// construction. See the REHYDRATION WARNING at the top of this file:
// a crash between broadcast and result-persist re-submits a second
// proposal with identical content. Acceptable for software-upgrade
// (bounded damage) but the pattern must NOT be reused for non-MVP
// proposal types without first wiring the pre-broadcast txHash marker
// from #174.
func (g *GovProposer) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, params GovSubmitProposalRequest) error {
		msg, err := buildSubmitProposalMsg(g.cfg, params)
		if err != nil {
			return err
		}
		result, err := SignAndBroadcast(ctx, g.cfg, SignAndBroadcastInput{
			ChainID: params.ChainID,
			KeyName: params.KeyName,
			Msg:     msg,
			Fees:    params.Fees,
			Gas:     params.Gas,
			Memo:    params.Memo,
			TaskID:  engine.TaskIDFromContext(ctx),
		})
		if err != nil {
			return err
		}
		inclusionStatus := "undetermined"
		if result.IncludedAt != nil {
			inclusionStatus = "included"
		}
		govSubmitLog.Info("proposal broadcast",
			"taskId", engine.TaskIDFromContext(ctx),
			"chainId", params.ChainID,
			"keyName", params.KeyName,
			"type", params.Type,
			"upgradeName", params.UpgradeName,
			"upgradeHeight", params.UpgradeHeight,
			"deposit", params.InitialDeposit,
			"txHash", result.TxHash,
			"height", result.Height,
			"sequence", result.Sequence,
			"inclusionStatus", inclusionStatus)
		return nil
	})
}

func buildSubmitProposalMsg(cfg engine.ExecutionConfig, params GovSubmitProposalRequest) (*govtypes.MsgSubmitProposal, error) {
	if cfg.Keyring == nil {
		return nil, Terminal(errors.New("keyring not configured: set SEI_KEYRING_BACKEND/SEI_KEYRING_PASSPHRASE on the sidecar"))
	}
	if params.KeyName == "" {
		return nil, Terminal(errors.New("keyName required"))
	}
	info, err := cfg.Keyring.Key(params.KeyName)
	if err != nil {
		return nil, Terminal(fmt.Errorf("keyring entry %q: %w", params.KeyName, err))
	}
	content, err := buildProposalContent(params)
	if err != nil {
		return nil, Terminal(err)
	}
	deposit, err := sdk.ParseCoinsNormalized(params.InitialDeposit)
	if err != nil {
		return nil, Terminal(fmt.Errorf("parse initialDeposit %q: %w", params.InitialDeposit, err))
	}
	if len(deposit) == 0 {
		return nil, Terminal(fmt.Errorf("initialDeposit %q resolves to zero coins", params.InitialDeposit))
	}
	if !deposit.IsAllPositive() {
		return nil, Terminal(fmt.Errorf("initialDeposit %q contains non-positive amounts", params.InitialDeposit))
	}
	// Symmetric with checkFeesDenom: deposit denom is fixed by gov
	// params on Sei (usei); a wrong denom would CheckTx-reject anyway,
	// but rejecting here saves the sign + broadcast roundtrip.
	for _, c := range deposit {
		if c.Denom != feeDenom {
			return nil, Terminal(fmt.Errorf("initialDeposit %q: denom %q not permitted (only %q)", params.InitialDeposit, c.Denom, feeDenom))
		}
	}
	msg, err := govtypes.NewMsgSubmitProposal(content, deposit, info.GetAddress())
	if err != nil {
		return nil, Terminal(fmt.Errorf("build MsgSubmitProposal: %w", err))
	}
	return msg, nil
}

func buildProposalContent(params GovSubmitProposalRequest) (govtypes.Content, error) {
	if params.Title == "" {
		return nil, errors.New("title required")
	}
	if params.Description == "" {
		return nil, errors.New("description required")
	}
	switch params.Type {
	case ProposalTypeSoftwareUpgrade:
		if params.UpgradeName == "" {
			return nil, errors.New("upgradeName required for software-upgrade")
		}
		if params.UpgradeHeight <= 0 {
			return nil, errors.New("upgradeHeight required for software-upgrade (must be > 0)")
		}
		return upgradetypes.NewSoftwareUpgradeProposal(params.Title, params.Description, upgradetypes.Plan{
			Name:   params.UpgradeName,
			Height: params.UpgradeHeight,
			Info:   params.UpgradeInfo,
		}), nil
	case "":
		return nil, errors.New("type required (allowed: software-upgrade)")
	default:
		return nil, fmt.Errorf("unsupported proposal type %q (allowed: software-upgrade)", params.Type)
	}
}
