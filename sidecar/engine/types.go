package engine

import (
	"context"
	"fmt"
	"time"
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
	TaskAssembleGenesisFork      TaskType = "assemble-genesis-fork"
	TaskExportState              TaskType = "export-state"
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
