package engine

import "context"

// Phase represents the sidecar's current lifecycle phase.
type Phase string

const (
	PhaseInitialized   Phase = "Initialized"
	PhaseTaskRunning   Phase = "TaskRunning"
	PhaseTaskComplete  Phase = "TaskComplete"
	PhaseReady         Phase = "Ready"
	PhaseUpgradeHalted Phase = "UpgradeHalted"
)

// TaskType identifies the kind of task to execute.
type TaskType string

const (
	TaskSnapshotRestore TaskType = "snapshot-restore"
	TaskDiscoverPeers   TaskType = "discover-peers"
	TaskConfigPatch     TaskType = "config-patch"
	TaskMarkReady       TaskType = "mark-ready"
	TaskUpdatePeers     TaskType = "update-peers"
	TaskScheduleUpgrade TaskType = "schedule-upgrade"
	TaskConfigureGenesis   TaskType = "configure-genesis"
	TaskConfigureStateSync TaskType = "configure-state-sync"
	TaskSnapshotUpload     TaskType = "snapshot-upload"
	TaskChangeMode         TaskType = "change-mode" // Reserved for Phase 2 — no handler registered
)

// Task is a unit of work submitted by the controller.
type Task struct {
	Type   TaskType       `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// TaskResult captures the outcome of a completed task.
type TaskResult struct {
	Task    TaskType `json:"task"`
	Success bool     `json:"success"`
	Error   string   `json:"error,omitempty"`
}

// UpgradeTarget holds a scheduled chain upgrade that the sidecar monitors.
type UpgradeTarget struct {
	Height int64  `json:"height"`
	Image  string `json:"image"`
}

// EngineStatus is a snapshot of the engine's current state, returned by Status().
type EngineStatus struct {
	Phase           Phase
	CurrentTask     *Task
	LastResult      *TaskResult
	ActiveTasks     int
	UpgradeHeight   int64
	UpgradeImage    string
	PendingUpgrades int
}

// TaskHandler executes a specific task type.
type TaskHandler func(ctx context.Context, params map[string]any) error
