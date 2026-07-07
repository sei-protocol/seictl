// Package tasks — gov completion contract. Gov sign-tx handlers surface a
// structured GovTxResult (committed_ok / committed_failed / pending) so the
// controller isn't left inferring success from a bare "task Complete".
package tasks

import (
	"fmt"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/wire"
)

var txBroadcastTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "seictl_tx_broadcast_total",
		Help: "Gov sign-tx broadcasts by task type and inclusion outcome.",
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
func classifyGovResult(taskType engine.TaskType, r *SignAndBroadcastResult) (*wire.GovTxResult, error) {
	out := &wire.GovTxResult{
		TxHash:     r.TxHash,
		Height:     r.Height,
		ProposalID: r.ProposalID,
		Code:       r.Code,
		Codespace:  r.Codespace,
		RawLog:     r.RawLog,
	}
	switch {
	case r.IncludedAt == nil:
		out.InclusionStatus = wire.InclusionPending
		txBroadcastTotal.WithLabelValues(string(taskType), wire.InclusionPending).Inc()
		return out, fmt.Errorf("tx %s inclusion undetermined; re-check pending", r.TxHash)
	case r.Code != 0:
		out.InclusionStatus = wire.InclusionCommittedFailed
		txBroadcastTotal.WithLabelValues(string(taskType), wire.InclusionCommittedFailed).Inc()
		return out, Terminal(fmt.Errorf("tx %s committed but failed: code=%d codespace=%q log=%s",
			r.TxHash, r.Code, r.Codespace, r.RawLog))
	default:
		out.InclusionStatus = wire.InclusionCommittedOK
		txBroadcastTotal.WithLabelValues(string(taskType), wire.InclusionCommittedOK).Inc()
		return out, nil
	}
}

// parseProposalID extracts the proposal_id from a committed tx's
// submit_proposal event, or 0 if absent (votes, non-gov txs, or a not-yet-
// included tx). It checks both event representations a /tx response may carry:
// the parsed string events under Logs, and the raw ABCI events (byte-keyed)
// under Events — which one is populated depends on the SDK version.
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
	for _, ev := range resp.Events {
		if ev.Type != govtypes.EventTypeSubmitProposal {
			continue
		}
		for _, attr := range ev.Attributes {
			if string(attr.Key) == govtypes.AttributeKeyProposalID {
				if id, err := strconv.ParseUint(string(attr.Value), 10, 64); err == nil {
					return id
				}
			}
		}
	}
	return 0
}
