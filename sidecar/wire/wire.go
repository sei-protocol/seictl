// Package wire holds the light, dependency-free contract types shared between
// the sidecar server (tasks/engine) and its clients (the generated client, the
// controller). It imports no sei-chain: importing any symbol here must never
// drag the chain graph into a light consumer. Server-side packages re-export
// these (e.g. engine.TaskType aliases wire.TaskType) so their call sites are
// unchanged.
package wire

import (
	"fmt"
	"strings"
)

// TaskType identifies a task on the wire (the request/result `type` field).
type TaskType string

const (
	TaskSnapshotRestore          TaskType = "snapshot-restore"
	TaskConfigPatch              TaskType = "config-patch"
	TaskConfigApply              TaskType = "config-apply"
	TaskConfigValidate           TaskType = "config-validate"
	TaskConfigReload             TaskType = "config-reload"
	TaskMarkReady                TaskType = "mark-ready"
	TaskRestartSeid              TaskType = "restart-seid"
	TaskConfigureGenesis         TaskType = "configure-genesis"
	TaskConfigureStateSync       TaskType = "configure-state-sync"
	TaskSnapshotUpload           TaskType = "snapshot-upload"
	TaskResultExport             TaskType = "result-export"
	TaskAwaitCondition           TaskType = "await-condition"
	TaskGenerateIdentity         TaskType = "generate-identity"
	TaskGenerateGentx            TaskType = "generate-gentx"
	TaskUploadGenesisArtifacts   TaskType = "upload-genesis-artifacts"
	TaskAssembleAndUploadGenesis TaskType = "assemble-and-upload-genesis"
	TaskSetGenesisPeers          TaskType = "set-genesis-peers"
	TaskGovVote                  TaskType = "gov-vote"
	TaskGovSoftwareUpgrade       TaskType = "gov-software-upgrade"
	TaskGovParamChange           TaskType = "gov-param-change"
	TaskEvmLogicalDigest         TaskType = "evm-logical-digest"
)

// VoteOption mirrors cosmos gov v1beta1 VoteOption values so callers can parse
// and validate a vote string without importing sei-chain. The values are the
// stable protobuf enum numbers (guarded by a test against govtypes).
type VoteOption int

const (
	OptionEmpty      VoteOption = 0
	OptionYes        VoteOption = 1
	OptionAbstain    VoteOption = 2
	OptionNo         VoteOption = 3
	OptionNoWithVeto VoteOption = 4
)

// ParseVoteOption is the single accepted-option set shared by client and server.
func ParseVoteOption(s string) (VoteOption, error) {
	switch strings.ToLower(s) {
	case "yes":
		return OptionYes, nil
	case "no":
		return OptionNo, nil
	case "abstain":
		return OptionAbstain, nil
	case "no_with_veto", "no-with-veto":
		return OptionNoWithVeto, nil
	default:
		return 0, fmt.Errorf("invalid vote option %q (allowed: yes | no | abstain | no_with_veto)", s)
	}
}

// Inclusion outcomes carried on GovTxResult.InclusionStatus — the stable enum
// the controller keys its condition/reason and re-submit decision on.
const (
	InclusionCommittedOK     = "committed_ok"
	InclusionCommittedFailed = "committed_failed"
	InclusionPending         = "pending"
)

// GovTxResult is the structured result a gov sign-tx handler returns; the engine
// persists it on TaskResult.Result and the controller decodes it into the
// SeiNodeTask outputs. ProposalID is 0 for votes and not-yet-included submits.
type GovTxResult struct {
	TxHash          string `json:"txHash"`
	Height          int64  `json:"height,omitempty"`
	ProposalID      uint64 `json:"proposalId,omitempty"`
	Code            uint32 `json:"code,omitempty"`
	Codespace       string `json:"codespace,omitempty"`
	RawLog          string `json:"rawLog,omitempty"`
	InclusionStatus string `json:"inclusionStatus"`
}
