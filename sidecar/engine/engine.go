package engine

import (
	"context"
	"errors"
	"fmt"
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
			_ = e.store.Save(&tr)
			continue
		}
		log.Info("rehydrating stale task", "type", tr.Type, "id", tr.ID)
		e.runTask(tr.ID, TaskType(tr.Type), handler, tr.Params, tr.SubmittedAt)
	}
}

// Submit starts a task in its own goroutine and returns its ID. When
// task.ID is set, it becomes the canonical identifier (enabling
// deterministic IDs from the controller). When empty, a random UUID is
// generated. If a task with the same ID already exists, the existing ID
// is returned without re-submitting.
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

	// Dedup check against the store.
	if existing, _ := e.store.Get(id); existing != nil {
		return id, nil
	}

	now := time.Now().UTC()

	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		SubmittedAt: now,
	}

	if err := e.store.Save(tr); err != nil {
		return "", fmt.Errorf("persist task: %w", err)
	}

	log.Info("task submitted", "type", task.Type, "id", id)
	e.runTask(id, task.Type, handler, task.Params, now)

	return id, nil
}

// runTask spawns a goroutine to run the handler and persist the result.
func (e *Engine) runTask(id string, taskType TaskType, handler TaskHandler, params map[string]any, submittedAt time.Time) {
	go func() {
		err := e.execute(e.ctx, taskType, handler, params)

		t := time.Now().UTC()
		tr := &TaskResult{
			ID:          id,
			Type:        string(taskType),
			Params:      params,
			SubmittedAt: submittedAt,
			CompletedAt: &t,
		}
		if err != nil {
			tr.Error = err.Error()
			tr.Status = TaskStatusFailed
			var taskErr *TaskError
			if errors.As(err, &taskErr) {
				tr.ErrorDetail = taskErr
			}
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
		log.Error("task failed", "type", taskType, "elapsed", time.Since(start).Round(time.Millisecond), "err", err)
		return err
	}
	log.Info("task completed", "type", taskType, "elapsed", time.Since(start).Round(time.Millisecond))
	if taskType == TaskMarkReady {
		e.ready.Store(true)
	}
	return nil
}

// Healthz returns true after the engine has been marked ready.
func (e *Engine) Healthz() bool {
	return e.ready.Load()
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
