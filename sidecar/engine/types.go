package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sei-protocol/sei-chain/sei-cosmos/crypto/keyring"

	"github.com/sei-protocol/seictl/sidecar/rpc"
)

// TaskType identifies the kind of task to execute.
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

// resultSink is the mutable slot a handler writes its structured result
// into. The engine threads a sink through ctx (alongside the task ID) so a
// handler can emit an in-band result without changing the TaskHandler
// signature. The engine reads sink.payload only after the handler returns,
// on the same goroutine — no concurrent access, no lock needed.
type resultSink struct {
	payload json.RawMessage
}

type resultSinkKey struct{}

// withResultSink attaches a result sink to ctx. The engine calls this in
// runTask; tests that exercise SetTaskResult directly use it too.
func withResultSink(ctx context.Context, sink *resultSink) context.Context {
	return context.WithValue(ctx, resultSinkKey{}, sink)
}

// SetTaskResult records a handler's structured result for the current task.
// It is captured by the engine and persisted on the TaskResult's Result
// field, then surfaced over the trusted controller↔sidecar task-result
// channel (GET /v0/tasks/{id}). Handlers that emit no result behave exactly
// as before. A no-op when ctx carries no sink (e.g. a handler invoked
// outside the engine in a unit test).
func SetTaskResult(ctx context.Context, result json.RawMessage) {
	if sink, ok := ctx.Value(resultSinkKey{}).(*resultSink); ok && sink != nil {
		sink.payload = result
	}
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
//
// Result carries a handler's structured output (e.g. assemble-genesis emits
// {"genesisHash":"<bare-hex>"}). It is optional and additive: handlers that
// emit nothing leave it nil and it is omitted from the wire, so the
// currently-deployed controller is unaffected. This in-band channel — read
// by the controller over the trusted GET /v0/tasks/{id} path — is the
// authenticated alternative to publishing results through attacker-writable
// shared storage.
type TaskResult struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Status      TaskStatus      `json:"status"`
	Run         int             `json:"run"`
	Params      map[string]any  `json:"params,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	SubmittedAt time.Time       `json:"submittedAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
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
