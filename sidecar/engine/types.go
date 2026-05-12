package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/sei-protocol/sei-chain/sei-cosmos/crypto/keyring"

	"github.com/sei-protocol/seictl/sidecar/rpc"
)

// TaskType identifies the kind of task to execute.
type TaskType string

const (
	TaskSnapshotRestore          TaskType = "snapshot-restore"
	TaskDiscoverPeers            TaskType = "discover-peers"
	TaskConfigPatch              TaskType = "config-patch"
	TaskConfigApply              TaskType = "config-apply"
	TaskConfigValidate           TaskType = "config-validate"
	TaskConfigReload             TaskType = "config-reload"
	TaskMarkReady                TaskType = "mark-ready"
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

	// TaskGovVote signs and broadcasts a cosmos-sdk gov v1beta1 MsgVote.
	TaskGovVote TaskType = "gov-vote"
)

// Task is a unit of work submitted by the controller. When ID is set, the
// engine uses it as the canonical task identifier (enabling deterministic
// IDs from the controller). When empty, the engine generates a random UUID.
type Task struct {
	ID     string         `json:"id,omitempty"`
	Type   TaskType       `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// TaskHandler executes a specific task type. Handlers MUST be idempotent:
// the engine may re-execute a handler after a crash recovery.
type TaskHandler func(ctx context.Context, params map[string]any) error

// taskIDKey is the context key under which the engine threads the
// current task ID into the handler context. Unexported so callers
// must go through TaskIDFromContext / withTaskID.
type taskIDKey struct{}

// TaskIDFromContext returns the engine-assigned task ID for the
// currently-executing handler, or the empty string when the context
// was not produced by the engine. Sign-tx handlers need this for the
// idempotency-checkpoint key.
func TaskIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(taskIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithTaskID attaches a task ID to ctx for handler consumption. The
// engine calls this in runTask; tests use it directly to exercise
// handlers without going through Submit.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey{}, id)
}

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

// TaskError is a structured error that includes operator-actionable context.
// Task handlers return this to provide rich error detail beyond a plain string.
type TaskError struct {
	Task      string `json:"task"`
	Operation string `json:"operation"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
	Cause     string `json:"cause,omitempty"`
}

func (e *TaskError) Error() string {
	s := fmt.Sprintf("%s: %s: %s", e.Task, e.Operation, e.Message)
	if e.Hint != "" {
		s += fmt.Sprintf(" [hint: %s]", e.Hint)
	}
	return s
}

// TaskResult records a task and its outcome.
type TaskResult struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Status      TaskStatus     `json:"status"`
	Run         int            `json:"run"`
	Params      map[string]any `json:"params,omitempty"`
	Error       string         `json:"error,omitempty"`
	SubmittedAt time.Time      `json:"submittedAt"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Status string `json:"status"`
}

// ExecutionConfig carries process-wide dependencies that the engine
// makes available to task handlers. Fields are nil when the
// corresponding subsystem is not configured.
type ExecutionConfig struct {
	// Keyring is the cosmos-sdk keyring opened from SEI_KEYRING_BACKEND.
	// Nil when the sidecar is started without keyring configuration —
	// sign-tx handlers must report a clear error in that case rather
	// than panic.
	Keyring keyring.Keyring

	// RPC talks to the co-located seid CometBFT RPC. Sign-tx handlers
	// use it for the chain-confusion guard (/status.NodeInfo.Network)
	// and for inclusion polling (/tx?hash=...). Nil only in unit tests
	// that never reach the chain.
	RPC *rpc.Client

	// Checkpoints persists the {taskID -> txHash} mapping that sign-tx
	// handlers write before BroadcastTxSync. See SignTxCheckpoint.
	Checkpoints CheckpointStore
}

// SignTxCheckpoint is the durable record a sign-tx handler writes
// BEFORE BroadcastTxSync. On engine restart or retry, the handler
// looks up the checkpoint, queries the chain for the persisted tx
// hash, and either returns the on-chain result (success) or — if
// the sequence has not advanced — safely re-signs and re-broadcasts.
// This is the load-bearing piece of the sign-tx idempotency contract.
type SignTxCheckpoint struct {
	TaskID        string    `json:"taskId"`
	TxHash        string    `json:"txHash"`
	Sequence      uint64    `json:"sequence"`
	AccountNumber uint64    `json:"accountNumber"`
	ChainID       string    `json:"chainId"`
	CreatedAt     time.Time `json:"createdAt"`
}

// CheckpointStore persists sign-tx checkpoints. It is intentionally a
// separate interface from ResultStore so the engine's TaskResult write
// path (which overwrites the row on completion) cannot clobber the
// checkpoint mid-flight. Implementations must be safe for concurrent use.
type CheckpointStore interface {
	// SaveCheckpoint upserts a checkpoint keyed by TaskID.
	SaveCheckpoint(c *SignTxCheckpoint) error

	// LoadCheckpoint returns (nil, nil) when none exists.
	LoadCheckpoint(taskID string) (*SignTxCheckpoint, error)

	// DeleteCheckpoint removes the row. No-op when absent.
	DeleteCheckpoint(taskID string) error
}
