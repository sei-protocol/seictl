package engine

import "context"

// TaskType identifies the kind of task to execute.
type TaskType string

const (
	TaskSnapshotRestore    TaskType = "snapshot-restore"
	TaskDiscoverPeers      TaskType = "discover-peers"
	TaskConfigPatch        TaskType = "config-patch"
	TaskMarkReady          TaskType = "mark-ready"
	TaskUpdatePeers        TaskType = "update-peers"
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
