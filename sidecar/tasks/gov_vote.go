// Package tasks — gov-vote handler.
//
// SECURITY POSTURE NOTE — sei-protocol/seictl#163 / #165
//
// Reached via the sidecar's HTTP API. In Phase 1-3 the API is
// unauthenticated; anyone with network reach can submit votes as the
// validator's operator account. Phase 4 (#165) fronts the sidecar
// with kube-rbac-proxy. REMOVE THIS NOTICE when #165 lands.

package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var govVoteLog = seilog.NewLogger("seictl", "task", "gov-vote")

// GovVoteRequest is the typed parameter shape for the gov-vote task.
// Idempotency is intentionally NOT handled here — the chain itself
// overwrites duplicate votes (last-write-wins on the (proposalID,
// voter) storage key), so the caller's request is authoritative.
type GovVoteRequest struct {
	ChainID    string `json:"chainId"`
	KeyName    string `json:"keyName"`
	ProposalID uint64 `json:"proposalId"`
	Option     string `json:"option"` // yes | no | abstain | no_with_veto
	Memo       string `json:"memo,omitempty"`
	Fees       string `json:"fees"`
	Gas        uint64 `json:"gas"`
}

// GovVoter handles gov-vote tasks. It captures the ExecutionConfig at
// construction so the handler closure can reach the keyring and RPC
// client without going through engine.Engine.
type GovVoter struct {
	cfg engine.ExecutionConfig
}

// NewGovVoter creates a GovVoter bound to the given config.
func NewGovVoter(cfg engine.ExecutionConfig) *GovVoter {
	return &GovVoter{cfg: cfg}
}

// Handler returns the engine TaskHandler for gov-vote.
//
// Rehydration semantics: if the sidecar crashes after BroadcastSync
// succeeds but before the engine persists the task result, the
// rehydration path re-runs this handler. A second tx will sign at
// sequence+1 and broadcast; the chain's last-write-wins on
// (proposalID, voter) keeps governance state correct, and the operator
// pays fees twice. This is acceptable specifically because MsgVote is
// chain-idempotent. Future sign-tx handlers MUST evaluate per-Msg
// idempotency before copying this pattern — non-idempotent Msg types
// (MsgSend, MsgWithdrawDelegatorReward, etc.) would double-spend.
// Tracked in #174 for the next non-idempotent sign-tx PR.
//
// Stale-proposal handling: when params.ProposalID does not exist or is
// in a final state, CheckTx rejects with a non-zero code and we surface
// a Terminal error. We do NOT pre-check via chain query — that would
// add a TOCTOU window (proposal transitions between check and
// broadcast) and duplicate the chain's authoritative decision.
func (g *GovVoter) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, params GovVoteRequest) error {
		msg, err := buildVoteMsg(g.cfg, params)
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
		govVoteLog.Info("vote broadcast",
			"taskId", engine.TaskIDFromContext(ctx),
			"chainId", params.ChainID,
			"keyName", params.KeyName,
			"proposalId", params.ProposalID,
			"option", params.Option,
			"txHash", result.TxHash,
			"height", result.Height,
			"sequence", result.Sequence,
			"inclusionStatus", inclusionStatus)
		return nil
	})
}

// buildVoteMsg validates the request, resolves the voter address from
// the keyring, and constructs the MsgVote. Split out so tests can
// exercise the param-to-Msg translation without going through the full
// SignAndBroadcast path.
func buildVoteMsg(cfg engine.ExecutionConfig, params GovVoteRequest) (*govtypes.MsgVote, error) {
	if params.ProposalID == 0 {
		return nil, Terminal(errors.New("proposalId required (must be > 0)"))
	}
	option, err := ParseVoteOption(params.Option)
	if err != nil {
		return nil, Terminal(err)
	}
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
	return govtypes.NewMsgVote(info.GetAddress(), params.ProposalID, option), nil
}

// ParseVoteOption maps the wire-format option string onto the Cosmos
// gov v1beta1 VoteOption enum. Accepts both "no_with_veto" (canonical
// SDK form) and "no-with-veto" (kebab-case for operator ergonomics).
// Exported so the client SDK can reuse the same accepted set — keeping
// client-side fast-fail and server-side authority in lockstep.
func ParseVoteOption(s string) (govtypes.VoteOption, error) {
	switch strings.ToLower(s) {
	case "yes":
		return govtypes.OptionYes, nil
	case "no":
		return govtypes.OptionNo, nil
	case "abstain":
		return govtypes.OptionAbstain, nil
	case "no_with_veto", "no-with-veto":
		return govtypes.OptionNoWithVeto, nil
	default:
		return 0, fmt.Errorf("invalid vote option %q (allowed: yes | no | abstain | no_with_veto)", s)
	}
}
