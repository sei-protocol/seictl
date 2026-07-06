// Package tasks — gov completion contract.
//
// The gov sign-tx handlers classify a broadcast into one of three outcomes and
// surface a structured GovTxResult over the task-result channel so the
// controller can tell submitted-and-committed from failed from
// inclusion-undetermined — rather than inferring success from a bare "task
// Complete". Applied uniformly to the fresh-sign, adopt-found, and
// adopt-rebroadcast paths via classifyGovResult.
package tasks

import (
	"fmt"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"
)

// Inclusion outcomes carried on GovTxResult.InclusionStatus. Stable strings —
// the controller keys its condition/reason and re-submit decision on them.
const (
	InclusionCommittedOK     = "committed_ok"
	InclusionCommittedFailed = "committed_failed"
	InclusionPending         = "pending"
)

// GovTxResult is the structured result a gov sign-tx handler returns; the
// engine persists it on TaskResult.Result and the controller decodes it into
// the SeiNodeTask outputs. ProposalID is 0 for votes and for not-yet-included
// submits.
type GovTxResult struct {
	TxHash          string `json:"txHash"`
	Height          int64  `json:"height,omitempty"`
	ProposalID      uint64 `json:"proposalId,omitempty"`
	Code            uint32 `json:"code,omitempty"`
	Codespace       string `json:"codespace,omitempty"`
	RawLog          string `json:"rawLog,omitempty"`
	InclusionStatus string `json:"inclusionStatus"`
}

var txBroadcastTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "seictl_tx_broadcast_total",
		Help: "Sign-tx broadcasts by task type and inclusion outcome.",
	},
	[]string{"type", "outcome"},
)

func init() { prometheus.MustRegister(txBroadcastTotal) }

// classifyGovResult maps a broadcast result to the gov completion contract and
// records the outcome metric. Returns (result, err):
//   - committed_ok  (included, code 0) → (result, nil)         → task Completed
//   - committed_failed (included, code≠0) → (result, Terminal) → task Failed, terminal
//   - pending (inclusion undetermined) → (result, non-terminal) → task Failed;
//     the controller re-submits (same task ID → re-run → marker re-check)
//
// The result is returned on every path so the controller reads txHash /
// inclusionStatus even on the Failed paths (the engine stamps it regardless).
func classifyGovResult(taskType string, r *SignAndBroadcastResult) (*GovTxResult, error) {
	out := &GovTxResult{
		TxHash:     r.TxHash,
		Height:     r.Height,
		ProposalID: r.ProposalID,
		Code:       r.Code,
		Codespace:  r.Codespace,
		RawLog:     r.RawLog,
	}
	switch {
	case r.IncludedAt == nil:
		out.InclusionStatus = InclusionPending
		txBroadcastTotal.WithLabelValues(taskType, InclusionPending).Inc()
		return out, fmt.Errorf("tx %s inclusion undetermined; re-check pending", r.TxHash)
	case r.Code != 0:
		out.InclusionStatus = InclusionCommittedFailed
		txBroadcastTotal.WithLabelValues(taskType, InclusionCommittedFailed).Inc()
		return out, Terminal(fmt.Errorf("tx %s committed but failed: code=%d codespace=%q log=%s",
			r.TxHash, r.Code, r.Codespace, r.RawLog))
	default:
		out.InclusionStatus = InclusionCommittedOK
		txBroadcastTotal.WithLabelValues(taskType, InclusionCommittedOK).Inc()
		return out, nil
	}
}

// parseProposalID extracts the proposal_id from a committed tx's
// submit_proposal event, or 0 if absent (votes, non-gov txs, or a not-yet-
// included tx whose Logs are empty).
func parseProposalID(resp *sdk.TxResponse) uint64 {
	for _, msgLog := range resp.Logs {
		for _, ev := range msgLog.Events {
			if ev.Type != govtypes.EventTypeSubmitProposal {
				continue
			}
			for _, attr := range ev.Attributes {
				if attr.Key == govtypes.AttributeKeyProposalID {
					if id, err := strconv.ParseUint(attr.Value, 10, 64); err == nil {
						return id
					}
				}
			}
		}
	}
	return 0
}
