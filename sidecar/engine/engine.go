package engine

import (
	"context"
	"fmt"
	"sync"
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
// goroutine. Scheduled tasks are re-fired by EvalSchedules on their cron.
//
// The store is the single source of truth for all task state. Only
// cancel funcs and the scheduled-task overlap guard are held in memory
// because they represent process-level goroutine state that cannot be
// serialized.
type Engine struct {
	handlers map[TaskType]TaskHandler
	ctx      context.Context
	mu       sync.RWMutex
	ready    bool

	// Persistent store — source of truth for all task results.
	store ResultStore

	// Cancel funcs for currently executing tasks. Keyed by task ID.
	// These cannot be serialized into the store.
	cancels map[string]context.CancelFunc

	// Overlap guard: prevents a scheduled task from being fired again
	// while a previous execution is still running.
	runningTasks map[string]struct{}

	// wg tracks in-flight task goroutines so they can be drained
	// before the store is closed on shutdown.
	wg sync.WaitGroup
}

// NewEngine creates a new Engine. The engine runs until ctx is cancelled.
// On startup, any one-shot tasks left in "running" state from a previous
// crash are marked as failed.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler, store ResultStore) *Engine {
	if err := store.RecoverStaleTasks(); err != nil {
		log.Error("failed to recover stale tasks", "err", err)
	}
	return &Engine{
		handlers:     handlers,
		ctx:          ctx,
		store:        store,
		cancels:      make(map[string]context.CancelFunc),
		runningTasks: make(map[string]struct{}),
	}
}

// Wait blocks until all in-flight task goroutines have completed.
// Call this after cancelling the engine's context and before closing
// the store to ensure all final writes land.
func (e *Engine) Wait() {
	e.wg.Wait()
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

	e.mu.Lock()
	// Dedup: check in-memory cancels (active) then store (completed).
	if _, running := e.cancels[id]; running {
		e.mu.Unlock()
		return id, nil
	}
	if existing, _ := e.store.Get(id); existing != nil {
		e.mu.Unlock()
		return id, nil
	}

	now := time.Now().UTC()
	taskCtx, cancel := context.WithCancel(e.ctx)

	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		SubmittedAt: now,
	}

	if err := e.store.Save(tr); err != nil {
		cancel()
		e.mu.Unlock()
		return "", fmt.Errorf("persist task: %w", err)
	}
	e.cancels[id] = cancel
	e.wg.Add(1)
	e.mu.Unlock()

	log.Info("task submitted", "type", task.Type, "id", id)

	go func() {
		defer e.wg.Done()
		err := e.execute(taskCtx, task.Type, handler, task.Params)

		e.mu.Lock()
		_, stillActive := e.cancels[id]
		if !stillActive {
			// Task was removed via RemoveResult while running; skip save.
			e.mu.Unlock()
			return
		}
		delete(e.cancels, id)
		e.mu.Unlock()

		t := time.Now().UTC()
		tr := &TaskResult{
			ID:          id,
			Type:        string(task.Type),
			Params:      task.Params,
			SubmittedAt: now,
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

	return id, nil
}

// SubmitScheduled creates a scheduled task. The schedule is evaluated by
// EvalSchedules. Only cron schedules are supported today. When task.ID is
// set, it becomes the canonical identifier. If a task with the same ID
// already exists, the existing ID is returned without re-registering.
func (e *Engine) SubmitScheduled(task Task, sched ScheduleConfig) (string, error) {
	if _, ok := e.handlers[task.Type]; !ok {
		return "", fmt.Errorf("unknown task type: %s", task.Type)
	}
	if sched.Cron == "" {
		return "", fmt.Errorf("schedule must specify a cron expression")
	}

	if err := validateTaskID(task.ID); err != nil {
		return "", err
	}

	now := time.Now().UTC()
	next, err := nextCronTime(sched.Cron, now)
	if err != nil {
		return "", fmt.Errorf("invalid cron %q: %w", sched.Cron, err)
	}

	id := task.ID
	if id == "" {
		id = uuid.New().String()
	}

	// Dedup check.
	if existing, _ := e.store.Get(id); existing != nil {
		return id, nil
	}

	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		Schedule:    &sched,
		SubmittedAt: now,
		NextRunAt:   &next,
	}

	if err := e.store.Save(tr); err != nil {
		return "", fmt.Errorf("persist scheduled task: %w", err)
	}

	log.Info("scheduled task registered", "type", task.Type, "id", id, "cron", sched.Cron, "next-run", next.Format(time.RFC3339))
	return id, nil
}

// EvalSchedules checks all scheduled tasks and fires any that are due.
func (e *Engine) EvalSchedules() {
	now := time.Now().UTC()

	due, err := e.store.ListScheduled(now)
	if err != nil {
		log.Error("store.ListScheduled failed", "err", err)
		return
	}

	for i := range due {
		tr := due[i]
		handler, ok := e.handlers[TaskType(tr.Type)]
		if !ok {
			log.Warn("scheduled task has no handler, removing", "type", tr.Type, "id", tr.ID)
			e.store.Delete(tr.ID)
			continue
		}

		e.mu.Lock()
		if _, alreadyRunning := e.runningTasks[tr.ID]; alreadyRunning {
			e.mu.Unlock()
			log.Debug("scheduled task still running, skipping", "type", tr.Type, "id", tr.ID)
			continue
		}
		e.runningTasks[tr.ID] = struct{}{}
		e.wg.Add(1)
		e.mu.Unlock()

		// Advance next run time.
		if tr.Schedule != nil && tr.Schedule.Cron != "" {
			if next, cronErr := nextCronTime(tr.Schedule.Cron, now); cronErr == nil {
				tr.NextRunAt = &next
				log.Debug("scheduled task advancing", "type", tr.Type, "id", tr.ID, "next-run", next.Format(time.RFC3339))
			}
		}

		log.Info("executing scheduled task", "type", tr.Type, "id", tr.ID)
		go func(tr TaskResult, h TaskHandler) {
			defer e.wg.Done()
			execErr := e.execute(e.ctx, TaskType(tr.Type), h, tr.Params)

			e.mu.Lock()
			delete(e.runningTasks, tr.ID)
			e.mu.Unlock()

			t := time.Now().UTC()
			tr.CompletedAt = &t
			tr.Error = ""
			if execErr != nil {
				tr.Error = execErr.Error()
				tr.Status = TaskStatusFailed
			} else {
				tr.Status = TaskStatusCompleted
			}

			if storeErr := e.store.Save(&tr); storeErr != nil {
				log.Error("failed to persist scheduled task result", "id", tr.ID, "err", storeErr)
			}
		}(tr, handler)
	}
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
		e.mu.Lock()
		e.ready = true
		e.mu.Unlock()
	}
	return nil
}

// Healthz returns true after the engine has been marked ready.
func (e *Engine) Healthz() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ready
}

// Status returns the engine's current state.
func (e *Engine) Status() StatusResponse {
	e.mu.RLock()
	defer e.mu.RUnlock()
	status := "Initializing"
	if e.ready {
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

// RemoveResult removes a task by ID. For active tasks this cancels the
// goroutine's context. Returns true if found.
func (e *Engine) RemoveResult(id string) bool {
	e.mu.Lock()
	if cancel, ok := e.cancels[id]; ok {
		cancel()
		delete(e.cancels, id)
	}
	delete(e.runningTasks, id)
	e.mu.Unlock()

	deleted, err := e.store.Delete(id)
	if err != nil {
		log.Error("store.Delete failed", "id", id, "err", err)
		return false
	}
	return deleted
}
