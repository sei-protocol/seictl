// Package tasks — gov-software-upgrade handler.
//
// This handler signs software-upgrade proposals as the validator's
// operator account. API authentication is controlled by
// SEI_SIDECAR_AUTHN_MODE; see sidecar/server/auth.go for the
// deployment guidance.
//
// REHYDRATION WARNING — sei-protocol/seictl#174
//
// MsgSubmitProposal is NOT chain-idempotent: a crash between
// BroadcastSync and task-result persist re-runs this handler on
// pod restart, which re-signs at sequence+1 and broadcasts a SECOND
// proposal with identical content. The operator pays InitialDeposit
// twice and two near-identical proposals appear on chain. Each future
// proposal-type handler is a separate file deliberately — that file
// MUST evaluate its own rehydration impact before shipping. #174
// tracks the pre-broadcast txHash marker that closes this window for
// all sign-tx handlers.

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

var govSoftwareUpgradeLog = seilog.NewLogger("seictl", "task", "gov-software-upgrade")

// GovSoftwareUpgradeRequest holds gov-software-upgrade params.
// Idempotency is NOT handled — see the REHYDRATION WARNING at the
// top of this file.
type GovSoftwareUpgradeRequest struct {
	ChainID string `json:"chainId"`
	KeyName string `json:"keyName"`

	Title       string `json:"title"`
	Description string `json:"description"`

	UpgradeName   string `json:"upgradeName"`
	UpgradeHeight int64  `json:"upgradeHeight"`
	UpgradeInfo   string `json:"upgradeInfo,omitempty"`

	InitialDeposit string `json:"initialDeposit"`

	Memo string `json:"memo,omitempty"`
	Fees string `json:"fees"`
	Gas  uint64 `json:"gas"`
}

// GovSoftwareUpgrader captures cfg by value at construction;
// engine.Config is documented read-only after startup, so the copy
// is safe.
type GovSoftwareUpgrader struct {
	cfg engine.ExecutionConfig
}

func NewGovSoftwareUpgrader(cfg engine.ExecutionConfig) *GovSoftwareUpgrader {
	return &GovSoftwareUpgrader{cfg: cfg}
}

// Handler delegates to SignAndBroadcast after MsgSubmitProposal
// construction. See the REHYDRATION WARNING at the top of this file:
// a crash between broadcast and result-persist re-submits a second
// proposal with identical content. Acceptable for software-upgrade
// (the upgrade module applies once at the named height) but the
// pattern must not be reused for other proposal types without first
// wiring the pre-broadcast txHash marker from #174.
func (g *GovSoftwareUpgrader) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, params GovSoftwareUpgradeRequest) error {
		msg, err := buildSoftwareUpgradeMsg(g.cfg, params)
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
		govSoftwareUpgradeLog.Info("proposal broadcast",
			"taskId", engine.TaskIDFromContext(ctx),
			"chainId", params.ChainID,
			"keyName", params.KeyName,
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

func buildSoftwareUpgradeMsg(cfg engine.ExecutionConfig, params GovSoftwareUpgradeRequest) (*govtypes.MsgSubmitProposal, error) {
	if cfg.Keyring == nil {
		return nil, Terminal(errors.New("keyring not configured: set SEI_KEYRING_BACKEND/SEI_KEYRING_PASSPHRASE on the sidecar"))
	}
	if params.KeyName == "" {
		return nil, Terminal(errors.New("keyName required"))
	}
	if params.Title == "" {
		return nil, Terminal(errors.New("title required"))
	}
	if params.Description == "" {
		return nil, Terminal(errors.New("description required"))
	}
	if params.UpgradeName == "" {
		return nil, Terminal(errors.New("upgradeName required"))
	}
	if params.UpgradeHeight <= 0 {
		return nil, Terminal(errors.New("upgradeHeight required (must be > 0)"))
	}
	info, err := cfg.Keyring.Key(params.KeyName)
	if err != nil {
		return nil, Terminal(fmt.Errorf("keyring entry %q: %w", params.KeyName, err))
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
	content := upgradetypes.NewSoftwareUpgradeProposal(params.Title, params.Description, upgradetypes.Plan{
		Name:   params.UpgradeName,
		Height: params.UpgradeHeight,
		Info:   params.UpgradeInfo,
	})
	msg, err := govtypes.NewMsgSubmitProposal(content, deposit, info.GetAddress())
	if err != nil {
		return nil, Terminal(fmt.Errorf("build MsgSubmitProposal: %w", err))
	}
	return msg, nil
}
