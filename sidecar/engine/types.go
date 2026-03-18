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
	TaskResultExport       TaskType = "result-export"
	TaskAwaitCondition     TaskType = "await-condition"
)

// Task is a unit of work submitted by the controller.
type Task struct {
	Type   TaskType       `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// TaskHandler executes a specific task type.
type TaskHandler func(ctx context.Context, params map[string]any) error

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

// ScheduleConfig triggers a task on a recurring basis. Cron is supported
// today; BlockHeight is reserved for future use.
type ScheduleConfig struct {
	Cron        string `json:"cron,omitempty"`
	BlockHeight *int64 `json:"blockHeight,omitempty"`
}

// TaskResult records a task and its outcome. All tasks share this model.
// When Schedule is non-nil the engine re-executes the task on its cron.
type TaskResult struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Status      TaskStatus      `json:"status"`
	Params      map[string]any  `json:"params,omitempty"`
	Schedule    *ScheduleConfig `json:"schedule,omitempty"`
	Error       string          `json:"error,omitempty"`
	SubmittedAt time.Time       `json:"submittedAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
	NextRunAt   *time.Time      `json:"nextRunAt,omitempty"`
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Status string `json:"status"`
}
