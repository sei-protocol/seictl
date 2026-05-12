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
	TaskGovVote                  TaskType = "gov-vote"
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

type taskIDKey struct{}

// TaskIDFromContext returns the engine-assigned task ID for the current
// handler, or "" when ctx is not engine-produced. Sign-tx handlers use
// this to derive the per-task memo tag.
func TaskIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(taskIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithTaskID attaches a task ID to ctx for handler consumption. The engine
// calls this in runTask; tests use it to bypass Submit.
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

// ExecutionConfig carries process-wide deps the engine exposes to handlers.
// Fields are nil when the corresponding subsystem is not configured.
type ExecutionConfig struct {
	// Keyring is opened from SEI_KEYRING_BACKEND. Nil when unset;
	// sign-tx handlers report a clear error rather than panic.
	Keyring keyring.Keyring

	// RPC talks to the co-located seid CometBFT RPC. Sign-tx handlers
	// use it for the chain-confusion guard and inclusion polling.
	RPC *rpc.Client
}
