package engine

import (
	"context"
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
	TaskExportState              TaskType = "export-state"
	TaskAssembleForkGenesis      TaskType = "assemble-fork-genesis"
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

// TaskResult records a task and its outcome.
type TaskResult struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Status      TaskStatus     `json:"status"`
	Params      map[string]any `json:"params,omitempty"`
	Error       string         `json:"error,omitempty"`
	SubmittedAt time.Time      `json:"submittedAt"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Status string `json:"status"`
}
