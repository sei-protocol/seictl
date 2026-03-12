package engine

import (
	"context"
	"time"
)

// TaskType identifies the kind of task to execute.
type TaskType string

const (
	TaskSnapshotRestore    TaskType = "snapshot-restore"
	TaskDiscoverPeers      TaskType = "discover-peers"
	TaskConfigPatch        TaskType = "config-patch"
	TaskConfigApply        TaskType = "config-apply"
	TaskConfigValidate     TaskType = "config-validate"
	TaskConfigReload       TaskType = "config-reload"
	TaskMarkReady          TaskType = "mark-ready"
	TaskConfigureGenesis   TaskType = "configure-genesis"
	TaskConfigureStateSync TaskType = "configure-state-sync"
	TaskSnapshotUpload     TaskType = "snapshot-upload"
)

// Task is a unit of work submitted by the controller.
type Task struct {
	Type   TaskType       `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// TaskHandler executes a specific task type.
type TaskHandler func(ctx context.Context, params map[string]any) error

// Schedule defines when a task should recur. Exactly one field should be set.
// Cron is supported today; BlockHeight is reserved for future use.
type Schedule struct {
	Cron        string `json:"cron,omitempty"`
	BlockHeight *int64 `json:"blockHeight,omitempty"`
}

// TaskResult records a task and its outcome. Both one-shot and scheduled tasks
// share this model. Scheduled tasks persist until deleted; one-shot results
// are kept in a bounded history.
type TaskResult struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Params      map[string]any `json:"params,omitempty"`
	Schedule    *Schedule      `json:"schedule,omitempty"`
	Error       string         `json:"error,omitempty"`
	SubmittedAt time.Time      `json:"submittedAt"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
	NextRunAt   *time.Time     `json:"nextRunAt,omitempty"`
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Status string `json:"status"`
}
