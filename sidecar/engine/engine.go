package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sei-protocol/seilog"
)

var log = seilog.NewLogger("seictl", "engine")

// ErrInvalidTaskID is returned when a caller-provided task ID is not a valid UUID.
var ErrInvalidTaskID = fmt.Errorf("task ID must be a valid UUID")

// validateTaskID checks that a non-empty ID string is a valid UUID.
func validateTaskID(id string) error {
	if id == "" {
		return nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidTaskID, id)
	}
	return nil
}

// Engine is the task executor. Every submitted task runs in its own
// goroutine. The store is the single source of truth for all task state.
// The engine context propagates to all handlers — on SIGTERM the
// context is cancelled and handlers observe ctx.Done() to stop gracefully.
type Engine struct {
	handlers map[TaskType]TaskHandler
	ctx      context.Context
	ready    atomic.Bool
	store    ResultStore
	mu       sync.Mutex

	// Config carries handler dependencies (keyring, etc.). Set once
	// during single-threaded startup before Submit is reachable; treated
	// as read-only thereafter. No synchronization.
	Config ExecutionConfig
}

// NewEngine creates a new Engine. The engine runs until ctx is cancelled.
// On startup, any tasks left in "running" state from a previous process
// are re-executed.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler, store ResultStore) *Engine {
	eng := &Engine{
		handlers: handlers,
		ctx:      ctx,
		store:    store,
	}
	eng.rehydrateStaleTasks()
	return eng
}

// rehydrateStaleTasks re-executes tasks that were left in "running"
// state by a previous process that exited before completing them.
// Run count is NOT incremented — rehydration is crash recovery of an
// incomplete run, not a new run.
func (e *Engine) rehydrateStaleTasks() {
	stale, err := e.store.ListStaleTasks()
	if err != nil {
		log.Error("failed to list stale tasks", "err", err)
		return
	}
	for _, tr := range stale {
		handler, ok := e.handlers[TaskType(tr.Type)]
		if !ok {
			log.Warn("stale task has no handler, marking failed", "type", tr.Type, "id", tr.ID)
			t := time.Now().UTC()
			tr.Status = TaskStatusFailed
			tr.Error = "no handler registered for task type"
			tr.CompletedAt = &t
			if err := e.store.Save(&tr); err != nil {
				log.Error("failed to persist stale task failure", "id", tr.ID, "err", err)
			}
			continue
		}
		log.Info("rehydrating stale task", "type", tr.Type, "id", tr.ID, "run", tr.Run)
		e.runTask(tr.ID, TaskType(tr.Type), handler, tr.Params, tr.SubmittedAt, tr.Run)
	}
}

// Submit starts a task in its own goroutine and returns its ID.
//
// The engine follows a cloud-API model for task lifecycle:
//   - If no task with this ID exists, create and execute it (run 1).
//   - If the task is running or completed, return its ID (idempotent no-op).
//   - If the task failed, re-execute it with an incremented run counter.
//
// The caller submits a stable key and the engine owns the execution lifecycle.
func (e *Engine) Submit(task Task) (string, error) {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return "", fmt.Errorf("unknown task type: %s", task.Type)
	}

	if err := validateTaskID(task.ID); err != nil {
		return "", err
	}

	id := task.ID
	if id == "" {
		id = uuid.New().String()
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	run := 1
	if existing, _ := e.store.Get(id); existing != nil {
		switch existing.Status {
		case TaskStatusRunning, TaskStatusCompleted:
			return id, nil
		case TaskStatusFailed:
			run = existing.Run + 1
		}
	}

	now := time.Now().UTC()
	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Run:         run,
		Params:      task.Params,
		SubmittedAt: now,
	}

	if err := e.store.Save(tr); err != nil {
		return "", fmt.Errorf("persist task: %w", err)
	}

	log.Info("task submitted", "type", task.Type, "id", id, "run", run)
	taskSubmissions.WithLabelValues(string(task.Type)).Inc()
	e.runTask(id, task.Type, handler, task.Params, now, run)

	return id, nil
}

// runTask spawns a goroutine to run the handler and persist the result.
func (e *Engine) runTask(id string, taskType TaskType, handler TaskHandler, params map[string]any, submittedAt time.Time, run int) {
	go func() {
		err := e.execute(e.ctx, taskType, handler, params)

		t := time.Now().UTC()
		tr := &TaskResult{
			ID:          id,
			Type:        string(taskType),
			Run:         run,
			Params:      params,
			SubmittedAt: submittedAt,
			CompletedAt: &t,
		}
		if err != nil {
			tr.Error = err.Error()
			tr.Status = TaskStatusFailed
		} else {
			tr.Status = TaskStatusCompleted
		}

		if storeErr := e.store.Save(tr); storeErr != nil {
			log.Error("failed to persist task result", "id", id, "err", storeErr)
		}
	}()
}

// execute runs a handler synchronously and logs the outcome.
func (e *Engine) execute(ctx context.Context, taskType TaskType, handler TaskHandler, params map[string]any) error {
	start := time.Now()
	if err := handler(ctx, params); err != nil {
		elapsed := time.Since(start)
		log.Error("task failed", "type", taskType, "elapsed", elapsed.Round(time.Millisecond), "err", err)
		taskDuration.WithLabelValues(string(taskType), "failed").Observe(elapsed.Seconds())
		taskFailures.WithLabelValues(string(taskType)).Inc()
		return err
	}
	elapsed := time.Since(start)
	log.Info("task completed", "type", taskType, "elapsed", elapsed.Round(time.Millisecond))
	taskDuration.WithLabelValues(string(taskType), "completed").Observe(elapsed.Seconds())
	if taskType == TaskMarkReady {
		e.ready.Store(true)
	}
	return nil
}

// Healthz returns true after the engine has been marked ready.
// Use as a readiness check.
func (e *Engine) Healthz() bool {
	return e.ready.Load()
}

// Livez returns nil when the engine's backing store is responsive.
// Use as a liveness check — a non-nil error means the process is wedged
// (e.g., SQLite WAL corruption, PVC read-only).
func (e *Engine) Livez() error {
	return e.store.Ping()
}

// Status returns the engine's current state.
func (e *Engine) Status() StatusResponse {
	status := "Initializing"
	if e.ready.Load() {
		status = "Ready"
	}
	return StatusResponse{Status: status}
}

// RecentResults returns the most recent task results across all states.
func (e *Engine) RecentResults() []TaskResult {
	results, err := e.store.List(100)
	if err != nil {
		log.Error("store.List failed", "err", err)
		return nil
	}
	return results
}

// GetResult returns a task by ID, or nil if not found.
func (e *Engine) GetResult(id string) *TaskResult {
	r, err := e.store.Get(id)
	if err != nil {
		log.Error("store.Get failed", "id", id, "err", err)
		return nil
	}
	return r
}

// RemoveResult removes a task by ID. Returns true if found.
func (e *Engine) RemoveResult(id string) bool {
	deleted, err := e.store.Delete(id)
	if err != nil {
		log.Error("store.Delete failed", "id", id, "err", err)
		return false
	}
	return deleted
}
