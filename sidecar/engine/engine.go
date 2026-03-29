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

const maxResults = 10

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

// taskEntry tracks a running task and its cancel func.
type taskEntry struct {
	result *TaskResult
	cancel context.CancelFunc
}

// Engine is the task executor. Every submitted task runs in its own
// goroutine. Scheduled tasks are re-fired by EvalSchedules on their cron.
type Engine struct {
	handlers map[TaskType]TaskHandler
	ctx      context.Context
	mu       sync.RWMutex
	ready    bool

	// Currently running tasks.
	active map[string]*taskEntry

	// Persistent store for completed task results.
	store ResultStore

	// Registered scheduled tasks and overlap guard.
	scheduled    map[string]*TaskResult
	runningTasks map[string]struct{}
}

// NewEngine creates a new Engine. The engine runs until ctx is cancelled.
// The store is used to persist completed task results.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler, store ResultStore) *Engine {
	return &Engine{
		handlers:     handlers,
		ctx:          ctx,
		store:        store,
		active:       make(map[string]*taskEntry),
		scheduled:    make(map[string]*TaskResult),
		runningTasks: make(map[string]struct{}),
	}
}

// Submit starts a task in its own goroutine and returns its ID. When
// task.ID is set, it becomes the canonical identifier (enabling
// deterministic IDs from the controller). When empty, a random UUID is
// generated. If a task with the same ID already exists (active or
// completed), the existing ID is returned without re-submitting.
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
	if existing := e.getResultLocked(id); existing != nil {
		e.mu.Unlock()
		return id, nil
	}

	now := time.Now()
	taskCtx, cancel := context.WithCancel(e.ctx)

	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		SubmittedAt: now,
	}

	e.active[id] = &taskEntry{result: tr, cancel: cancel}
	e.mu.Unlock()

	log.Info("task submitted", "type", task.Type, "id", id)

	go func() {
		err := e.execute(taskCtx, task.Type, handler, task.Params)

		e.mu.Lock()
		entry, ok := e.active[id]
		if !ok {
			e.mu.Unlock()
			log.Error("task completed but was removed while running", "type", task.Type, "id", id)
			return
		}
		t := time.Now()
		entry.result.CompletedAt = &t
		if err != nil {
			entry.result.Error = err.Error()
			entry.result.Status = TaskStatusFailed
		} else {
			entry.result.Status = TaskStatusCompleted
		}
		result := *entry.result
		delete(e.active, id)
		e.mu.Unlock()

		if storeErr := e.store.Save(&result); storeErr != nil {
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

	now := time.Now()
	next, err := nextCronTime(sched.Cron, now)
	if err != nil {
		return "", fmt.Errorf("invalid cron %q: %w", sched.Cron, err)
	}

	id := task.ID
	if id == "" {
		id = uuid.New().String()
	}

	e.mu.Lock()
	if existing := e.getResultLocked(id); existing != nil {
		e.mu.Unlock()
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

	e.scheduled[id] = tr
	e.mu.Unlock()

	log.Info("scheduled task registered", "type", task.Type, "id", id, "cron", sched.Cron, "next-run", next.Format(time.RFC3339))
	return id, nil
}

// EvalSchedules checks all scheduled tasks and fires any that are due.
func (e *Engine) EvalSchedules() {
	now := time.Now()

	e.mu.RLock()
	var due []*TaskResult
	for _, tr := range e.scheduled {
		if tr.NextRunAt != nil && !now.Before(*tr.NextRunAt) {
			due = append(due, tr)
		}
	}
	e.mu.RUnlock()

	for _, tr := range due {
		handler, ok := e.handlers[TaskType(tr.Type)]
		if !ok {
			log.Warn("scheduled task has no handler, removing", "type", tr.Type, "id", tr.ID)
			e.mu.Lock()
			delete(e.scheduled, tr.ID)
			e.mu.Unlock()
			continue
		}

		e.mu.Lock()
		if _, alreadyRunning := e.runningTasks[tr.ID]; alreadyRunning {
			e.mu.Unlock()
			log.Debug("scheduled task still running, skipping", "type", tr.Type, "id", tr.ID)
			continue
		}
		e.runningTasks[tr.ID] = struct{}{}

		if tr.Schedule != nil && tr.Schedule.Cron != "" {
			if next, cronErr := nextCronTime(tr.Schedule.Cron, now); cronErr == nil {
				tr.NextRunAt = &next
				log.Debug("scheduled task advancing", "type", tr.Type, "id", tr.ID, "next-run", next.Format(time.RFC3339))
			}
		}
		e.mu.Unlock()

		log.Info("executing scheduled task", "type", tr.Type, "id", tr.ID)
		go func(tr *TaskResult, h TaskHandler) {
			err := e.execute(e.ctx, TaskType(tr.Type), h, tr.Params)

			e.mu.Lock()
			defer e.mu.Unlock()
			delete(e.runningTasks, tr.ID)
			s, ok := e.scheduled[tr.ID]
			if !ok {
				return
			}
			t := time.Now()
			s.CompletedAt = &t
			s.Error = ""
			if err != nil {
				s.Error = err.Error()
				s.Status = TaskStatusFailed
			} else {
				s.Status = TaskStatusCompleted
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

// RecentResults returns the most recent task results, combining active
// tasks, completed results from the store, and scheduled tasks.
func (e *Engine) RecentResults() []TaskResult {
	e.mu.RLock()
	all := make([]TaskResult, 0, len(e.active)+len(e.scheduled)+maxResults)
	for _, a := range e.active {
		all = append(all, *a.result)
	}
	for _, s := range e.scheduled {
		all = append(all, *s)
	}
	e.mu.RUnlock()

	completed, err := e.store.List(maxResults)
	if err != nil {
		log.Error("store.List failed", "err", err)
	} else {
		all = append(all, completed...)
	}

	if len(all) > maxResults {
		all = all[:maxResults]
	}
	return all
}

// GetResult returns a task by ID (active, completed, or scheduled), or nil.
func (e *Engine) GetResult(id string) *TaskResult {
	e.mu.RLock()
	if a, ok := e.active[id]; ok {
		cp := *a.result
		e.mu.RUnlock()
		return &cp
	}
	if s, ok := e.scheduled[id]; ok {
		cp := *s
		e.mu.RUnlock()
		return &cp
	}
	e.mu.RUnlock()

	r, err := e.store.Get(id)
	if err != nil {
		log.Error("store.Get failed", "id", id, "err", err)
		return nil
	}
	return r
}

// getResultLocked returns a task by ID without acquiring the lock.
// Caller must hold at least a read lock. Falls through to the store
// for completed results (fast PK lookup, acceptable under lock).
func (e *Engine) getResultLocked(id string) *TaskResult {
	if a, ok := e.active[id]; ok {
		cp := *a.result
		return &cp
	}
	if s, ok := e.scheduled[id]; ok {
		cp := *s
		return &cp
	}
	r, err := e.store.Get(id)
	if err != nil {
		log.Error("store.Get failed", "id", id, "err", err)
		return nil
	}
	return r
}

// RemoveResult removes a task by ID. For active tasks this cancels the
// goroutine's context. For scheduled tasks this stops the schedule.
// Returns true if found.
func (e *Engine) RemoveResult(id string) bool {
	e.mu.Lock()
	if a, ok := e.active[id]; ok {
		a.cancel()
		delete(e.active, id)
		e.mu.Unlock()
		return true
	}
	if _, ok := e.scheduled[id]; ok {
		delete(e.scheduled, id)
		delete(e.runningTasks, id)
		e.mu.Unlock()
		return true
	}
	e.mu.Unlock()

	deleted, err := e.store.Delete(id)
	if err != nil {
		log.Error("store.Delete failed", "id", id, "err", err)
		return false
	}
	return deleted
}
