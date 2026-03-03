package engine

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskID uniquely identifies a submitted task.
type TaskID = string

// TaskState represents the lifecycle state of a tracked task.
type TaskState string

const (
	TaskStatePending   TaskState = "pending"
	TaskStateRunning   TaskState = "running"
	TaskStateCompleted TaskState = "completed"
	TaskStateFailed    TaskState = "failed"
)

// TrackedTask records the full lifecycle of a submitted task.
type TrackedTask struct {
	ID          TaskID         `json:"id"`
	Type        TaskType       `json:"type"`
	Params      map[string]any `json:"params,omitempty"`
	State       TaskState      `json:"state"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	StartedAt   *time.Time     `json:"startedAt,omitempty"`
	CompletedAt *time.Time     `json:"completedAt,omitempty"`
}

const maxHistory = 200

// TaskTracker maintains a bounded history of task lifecycles.
type TaskTracker struct {
	mu      sync.Mutex
	tasks   map[TaskID]*TrackedTask
	history []TaskID // newest first
}

// NewTaskTracker creates an empty tracker.
func NewTaskTracker() *TaskTracker {
	return &TaskTracker{
		tasks: make(map[TaskID]*TrackedTask),
	}
}

// Create registers a new task in pending state and returns its ID.
func (t *TaskTracker) Create(taskType TaskType, params map[string]any) TaskID {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := uuid.New().String()
	now := time.Now()
	t.tasks[id] = &TrackedTask{
		ID:        id,
		Type:      taskType,
		Params:    params,
		State:     TaskStatePending,
		CreatedAt: now,
	}
	t.history = append([]TaskID{id}, t.history...)
	t.evict()
	return id
}

// MarkRunning transitions a task to the running state.
func (t *TaskTracker) MarkRunning(id TaskID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if task, ok := t.tasks[id]; ok {
		task.State = TaskStateRunning
		now := time.Now()
		task.StartedAt = &now
	}
}

// MarkCompleted transitions a task to the completed state.
func (t *TaskTracker) MarkCompleted(id TaskID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if task, ok := t.tasks[id]; ok {
		task.State = TaskStateCompleted
		now := time.Now()
		task.CompletedAt = &now
	}
}

// MarkFailed transitions a task to the failed state with an error message.
func (t *TaskTracker) MarkFailed(id TaskID, errMsg string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if task, ok := t.tasks[id]; ok {
		task.State = TaskStateFailed
		task.Error = errMsg
		now := time.Now()
		task.CompletedAt = &now
	}
}

// Get returns a copy of the tracked task, or nil if not found.
func (t *TaskTracker) Get(id TaskID) *TrackedTask {
	t.mu.Lock()
	defer t.mu.Unlock()

	task, ok := t.tasks[id]
	if !ok {
		return nil
	}
	cp := *task
	return &cp
}

// List returns tasks in newest-first order. If stateFilter is non-empty, only
// tasks matching that state are returned. limit <= 0 means no limit.
func (t *TaskTracker) List(stateFilter TaskState, limit int) []TrackedTask {
	t.mu.Lock()
	defer t.mu.Unlock()

	var result []TrackedTask
	for _, id := range t.history {
		task, ok := t.tasks[id]
		if !ok {
			continue
		}
		if stateFilter != "" && task.State != stateFilter {
			continue
		}
		cp := *task
		result = append(result, cp)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

// ActiveCount returns the number of tasks currently in the running state.
func (t *TaskTracker) ActiveCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	count := 0
	for _, task := range t.tasks {
		if task.State == TaskStateRunning {
			count++
		}
	}
	return count
}

// evict removes the oldest entries when history exceeds maxHistory.
// Caller must hold t.mu.
func (t *TaskTracker) evict() {
	for len(t.history) > maxHistory {
		oldest := t.history[len(t.history)-1]
		t.history = t.history[:len(t.history)-1]
		delete(t.tasks, oldest)
	}
}
