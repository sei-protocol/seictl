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

// taskEntry tracks a running task and its cancel func.
type taskEntry struct {
	result *TaskResult
	cancel context.CancelFunc
}

// Engine is the task executor. Every submitted task runs in its own
// goroutine. Scheduled tasks are re-fired by EvalSchedules on their cron.
// There is no serial pipeline — concurrency control is the caller's
// responsibility (the controller sequences bootstrap tasks via its plan).
type Engine struct {
	handlers map[TaskType]TaskHandler
	ctx      context.Context
	mu       sync.RWMutex
	ready    bool

	// Currently running tasks (one-shot and long-running).
	active map[string]*taskEntry

	// Completed one-shot results (ring buffer, newest last).
	results []*TaskResult

	// Registered scheduled tasks and overlap guard.
	scheduled    map[string]*TaskResult
	runningTasks map[string]struct{}
}

// NewEngine creates a new Engine. The engine runs until ctx is cancelled.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler) *Engine {
	return &Engine{
		handlers:     handlers,
		ctx:          ctx,
		active:       make(map[string]*taskEntry),
		scheduled:    make(map[string]*TaskResult),
		runningTasks: make(map[string]struct{}),
	}
}

// Submit starts a task in its own goroutine and returns the assigned UUID.
func (e *Engine) Submit(task Task) (string, error) {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return "", fmt.Errorf("unknown task type: %s", task.Type)
	}

	id := uuid.New().String()
	now := time.Now()
	taskCtx, cancel := context.WithCancel(e.ctx)

	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		SubmittedAt: now,
	}

	e.mu.Lock()
	e.active[id] = &taskEntry{result: tr, cancel: cancel}
	e.mu.Unlock()

	log.Info("task submitted", "type", task.Type, "id", id)

	go func() {
		err := e.execute(taskCtx, task.Type, handler, task.Params)

		e.mu.Lock()
		defer e.mu.Unlock()
		entry, ok := e.active[id]
		if !ok {
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
		e.results = append(e.results, entry.result)
		if len(e.results) > maxResults {
			e.results = e.results[len(e.results)-maxResults:]
		}
		delete(e.active, id)
	}()

	return id, nil
}

// SubmitScheduled creates a scheduled task. The schedule is evaluated by
// EvalSchedules. Only cron schedules are supported today.
func (e *Engine) SubmitScheduled(task Task, sched ScheduleConfig) (string, error) {
	if _, ok := e.handlers[task.Type]; !ok {
		return "", fmt.Errorf("unknown task type: %s", task.Type)
	}
	if sched.Cron == "" {
		return "", fmt.Errorf("schedule must specify a cron expression")
	}

	now := time.Now()
	next, _ := nextCronTime(sched.Cron, now)
	id := uuid.New().String()

	tr := &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		Schedule:    &sched,
		SubmittedAt: now,
		NextRunAt:   &next,
	}

	e.mu.Lock()
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
// tasks, completed results, and scheduled tasks.
func (e *Engine) RecentResults() []TaskResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cap := len(e.active) + len(e.results) + len(e.scheduled)
	all := make([]TaskResult, 0, cap)
	for _, a := range e.active {
		all = append(all, *a.result)
	}
	for i := len(e.results) - 1; i >= 0; i-- {
		all = append(all, *e.results[i])
	}
	for _, s := range e.scheduled {
		all = append(all, *s)
	}

	if len(all) > maxResults {
		all = all[:maxResults]
	}
	return all
}

// GetResult returns a task by ID (active, completed, or scheduled), or nil.
func (e *Engine) GetResult(id string) *TaskResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if a, ok := e.active[id]; ok {
		cp := *a.result
		return &cp
	}
	if s, ok := e.scheduled[id]; ok {
		cp := *s
		return &cp
	}
	for _, r := range e.results {
		if r.ID == id {
			cp := *r
			return &cp
		}
	}
	return nil
}

// RemoveResult removes a task by ID. For active tasks this cancels the
// goroutine's context. For scheduled tasks this stops the schedule.
// Returns true if found.
func (e *Engine) RemoveResult(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if a, ok := e.active[id]; ok {
		a.cancel()
		delete(e.active, id)
		return true
	}
	if _, ok := e.scheduled[id]; ok {
		delete(e.scheduled, id)
		return true
	}
	for i, r := range e.results {
		if r.ID == id {
			e.results = append(e.results[:i], e.results[i+1:]...)
			return true
		}
	}
	return false
}
