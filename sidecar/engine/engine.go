package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sei-protocol/seilog"
)

var log = seilog.NewLogger("seictl", "engine")

const maxResults = 10

// ErrBusy is returned by Submit when a task is already running.
var ErrBusy = errors.New("a task is already running")

// taskEnvelope wraps a task with its resolved handler for the worker loop.
type taskEnvelope struct {
	id          string
	task        Task
	handler     TaskHandler
	submittedAt time.Time
}

// Engine is the task executor. One-shot tasks are serialized through a channel
// with a TryLock gate. Scheduled tasks run in their own goroutines when their
// cron fires. Both share the unified TaskResult model.
type Engine struct {
	handlers  map[TaskType]TaskHandler
	ctx       context.Context
	taskCh    chan taskEnvelope
	taskMu    sync.Mutex
	running   atomic.Bool
	mu        sync.RWMutex
	ready     bool
	inflight  *TaskResult
	results   []*TaskResult
	scheduled map[string]*TaskResult
}

// NewEngine creates a new Engine and starts its worker loop. The engine
// runs until ctx is cancelled.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler) *Engine {
	e := &Engine{
		handlers:  handlers,
		ctx:       ctx,
		taskCh:    make(chan taskEnvelope, 1),
		scheduled: make(map[string]*TaskResult),
	}
	go e.loop()
	return e
}

// loop is the serial worker that processes one-shot tasks one at a time.
func (e *Engine) loop() {
	for e.ctx.Err() == nil {
		select {
		case <-e.ctx.Done():
			return
		case env := <-e.taskCh:
			e.running.Store(true)
			err := e.execute(env.task.Type, env.handler, env.task.Params)

			now := time.Now()
			e.mu.Lock()
			if e.inflight != nil {
				e.inflight.CompletedAt = &now
				if err != nil {
					e.inflight.Error = err.Error()
					e.inflight.Status = TaskStatusFailed
				} else {
					e.inflight.Status = TaskStatusCompleted
				}
				e.results = append(e.results, e.inflight)
				if len(e.results) > maxResults {
					e.results = e.results[len(e.results)-maxResults:]
				}
				e.inflight = nil
			}
			e.mu.Unlock()

			e.running.Store(false)
			e.taskMu.Unlock()
		}
	}
}

// Submit enqueues a one-shot task and returns the assigned UUID.
func (e *Engine) Submit(task Task) (string, error) {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return "", fmt.Errorf("unknown task type: %s", task.Type)
	}
	if !e.taskMu.TryLock() {
		log.Debug("task rejected, engine busy", "type", task.Type)
		return "", ErrBusy
	}
	id := uuid.New().String()
	now := time.Now()

	e.mu.Lock()
	e.inflight = &TaskResult{
		ID:          id,
		Type:        string(task.Type),
		Status:      TaskStatusRunning,
		Params:      task.Params,
		SubmittedAt: now,
	}
	e.mu.Unlock()

	log.Info("task submitted", "type", task.Type, "id", id)
	e.taskCh <- taskEnvelope{id: id, task: task, handler: handler, submittedAt: now}
	return id, nil
}

// SubmitScheduled creates a scheduled task. Returns the task ID. Currently
// only cron schedules are supported; block-height scheduling is reserved for
// future use. The schedule is evaluated by EvalSchedules.
func (e *Engine) SubmitScheduled(task Task, sched Schedule) (string, error) {
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
// Scheduled tasks run in their own goroutines, bypassing the one-shot lock.
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

		// Advance NextRunAt before launching so a concurrent EvalSchedules
		// call won't fire the same task again while it's still running.
		e.mu.Lock()
		if tr.Schedule != nil && tr.Schedule.Cron != "" {
			if next, cronErr := nextCronTime(tr.Schedule.Cron, now); cronErr == nil {
				tr.NextRunAt = &next
				log.Debug("scheduled task advancing", "type", tr.Type, "id", tr.ID, "next-run", next.Format(time.RFC3339))
			}
		}
		e.mu.Unlock()

		log.Info("executing scheduled task", "type", tr.Type, "id", tr.ID)
		go func(tr *TaskResult, h TaskHandler) {
			err := e.execute(TaskType(tr.Type), h, tr.Params)

			e.mu.Lock()
			defer e.mu.Unlock()
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
func (e *Engine) execute(taskType TaskType, handler TaskHandler, params map[string]any) error {
	start := time.Now()
	if err := handler(e.ctx, params); err != nil {
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
	if e.running.Load() {
		return StatusResponse{Status: "Running"}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	status := "Initializing"
	if e.ready {
		status = "Ready"
	}
	return StatusResponse{Status: status}
}

// RecentResults returns the most recent task results (newest first),
// combining in-flight, completed one-shot results, and active scheduled tasks.
func (e *Engine) RecentResults() []TaskResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cap := len(e.results) + len(e.scheduled) + 1
	all := make([]TaskResult, 0, cap)
	if e.inflight != nil {
		all = append(all, *e.inflight)
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

// GetResult returns a task by ID (in-flight, completed, or scheduled), or nil.
func (e *Engine) GetResult(id string) *TaskResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.inflight != nil && e.inflight.ID == id {
		cp := *e.inflight
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

// RemoveResult removes a task by ID. For scheduled tasks this stops the
// schedule. Returns true if found.
func (e *Engine) RemoveResult(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

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
