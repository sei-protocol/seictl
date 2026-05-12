// SECURITY POSTURE NOTE — sei-protocol/seictl#163 / #165
//
// This handler accepts sign-and-broadcast requests over the sidecar's HTTP
// API, which is unauthenticated in Phase 1-3 of the governance-flow workstream.
// The sidecar binds 0.0.0.0:7777 in Phase 1-3; any caller with network reach
// to that port can submit governance txs as the validator's operator account
// by POSTing {type, params} to /v0/tasks. This is comparable to the
// seienv+SSH status-quo trust scope (anyone with the SSH key has equivalent
// power) but the K8s network blast radius is wider.
//
// Phase 4 (#165) fronts the sidecar with a kube-rbac-proxy sidecar container
// that terminates TLS on 0.0.0.0:8443, runs TokenReview + a single coarse
// SubjectAccessReview (create seinodetasks.sei.io) against the cluster API
// server regardless of task type, and proxies to the sidecar bound on
// 127.0.0.1:7777. The sidecar then trusts X-Remote-User on the loopback ingress.
// REMOVE THIS NOTICE when #165 lands.

package tasks

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govutils "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/client/utils"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
)

// Defaults match docs/design/in-pod-governance-signing.md, Component C,
// "Default gas/fees". MsgVote needs ~200k gas; at the 0.02usei min-gas-
// price config in the Sei mainnet this is 4000usei.
const (
	DefaultGovVoteFees = "4000usei"
	DefaultGovVoteGas  = uint64(200_000)
	defaultGovVoteMemo = "seictl-sidecar"
)

// GovVoteParams is the typed wire format for a gov-vote task. The field
// tags match the body the controller / CLI submits — keep these stable
// for clients.
type GovVoteParams struct {
	ChainID    string `json:"chainId"`
	KeyName    string `json:"keyName"`
	ProposalID uint64 `json:"proposalId"`
	Option     string `json:"option"` // "yes" | "no" | "abstain" | "no_with_veto"
	Fees       string `json:"fees,omitempty"`
	Gas        uint64 `json:"gas,omitempty"`
	Memo       string `json:"memo,omitempty"`
}

// GovVoteResult extends SignAndBroadcastResult with the per-task fields
// the operator wants surfaced for vote audits.
type GovVoteResult struct {
	SignAndBroadcastResult
	MsgType    string `json:"msgType"`
	ProposalID uint64 `json:"proposalId"`
	Option     string `json:"option"`
}

// GovVoteHandler returns a TaskHandler for the gov-vote task type.
// The handler reads ExecutionConfig at execute-time (rather than capturing
// it at construction) so that engine.RehydrateStaleTasks sees the latest
// process-wide deps after serve.go finishes wiring.
//
// We require the engine to expose ExecutionConfig to the handler; the
// existing eng.Config field is populated before RehydrateStaleTasks runs
// (see serve.go), so the closure here can resolve to a non-nil value at
// the time it is invoked.
func GovVoteHandler(cfgFn func() engine.ExecutionConfig) engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, p GovVoteParams) error {
		_, err := runGovVote(ctx, cfgFn(), p)
		return err
	})
}

// runGovVote is the testable inner body: it returns the GovVoteResult so
// unit tests can assert on the wire-shape without recovering JSON from
// the result store. The wrapping TaskHandler in GovVoteHandler discards
// it because the engine has no per-task structured-result channel yet;
// when the store is extended to carry one this is the call-site that
// will write to it.
func runGovVote(ctx context.Context, cfg engine.ExecutionConfig, p GovVoteParams) (*GovVoteResult, error) {
	if err := validateGovVoteParams(p); err != nil {
		return nil, Terminal(err)
	}

	optionEnum, err := govtypes.VoteOptionFromString(govutils.NormalizeVoteOption(p.Option))
	if err != nil {
		return nil, Terminal(fmt.Errorf("invalid vote option %q: %w", p.Option, err))
	}

	// Resolve the signer's address up front so we fail Terminal on a
	// missing keyring entry BEFORE we hit the network for chain-confusion
	// checks. (SignAndBroadcast re-resolves; this early call gives the
	// operator a clearer error and keeps the chain-id guard from doing
	// network work on a doomed request.)
	if cfg.Keyring == nil {
		return nil, Terminal(errors.New("keyring not configured: set SEI_KEYRING_BACKEND/SEI_KEYRING_PASSPHRASE on the sidecar"))
	}
	info, err := cfg.Keyring.Key(p.KeyName)
	if err != nil {
		return nil, Terminal(fmt.Errorf("keyring entry %q: %w", p.KeyName, err))
	}

	msg := govtypes.NewMsgVote(info.GetAddress(), p.ProposalID, optionEnum)
	// ValidateBasic catches malformed addresses (defense-in-depth — the
	// keyring already produced this address) and rejects OptionEmpty.
	if vbErr := msg.ValidateBasic(); vbErr != nil {
		return nil, Terminal(fmt.Errorf("MsgVote ValidateBasic: %w", vbErr))
	}

	in := SignAndBroadcastInput{
		ChainID: p.ChainID,
		KeyName: p.KeyName,
		Msg:     msg,
		Fees:    defaultString(p.Fees, DefaultGovVoteFees),
		Gas:     defaultUint64(p.Gas, DefaultGovVoteGas),
		Memo:    defaultString(p.Memo, defaultGovVoteMemo),
		TaskID:  engine.TaskIDFromContext(ctx),
	}

	res, err := SignAndBroadcast(ctx, cfg, in)
	if err != nil {
		return nil, err
	}
	return &GovVoteResult{
		SignAndBroadcastResult: *res,
		MsgType:                sdk.MsgTypeURL(msg),
		ProposalID:             p.ProposalID,
		Option:                 govutils.NormalizeVoteOption(p.Option),
	}, nil
}

// validateGovVoteParams enforces the shape that downstream layers also
// re-validate (Terminal-wrapping happens at the call site). Keep checks
// here CHEAP and DETERMINISTIC — no network, no keyring.
func validateGovVoteParams(p GovVoteParams) error {
	if p.ChainID == "" {
		return errors.New("gov-vote: chainId required")
	}
	if p.KeyName == "" {
		return errors.New("gov-vote: keyName required")
	}
	if p.ProposalID == 0 {
		return errors.New("gov-vote: proposalId required (must be > 0)")
	}
	if p.Option == "" {
		return errors.New("gov-vote: option required")
	}
	return nil
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func defaultUint64(v, fallback uint64) uint64 {
	if v == 0 {
		return fallback
	}
	return v
}
