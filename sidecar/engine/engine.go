package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const maxResults = 5

// ErrBusy is returned by Submit when a task is already running.
var ErrBusy = errors.New("a task is already running")

// taskEnvelope wraps a task with its resolved handler for the worker loop.
type taskEnvelope struct {
	id      string
	task    Task
	handler TaskHandler
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
	for {
		select {
		case <-e.ctx.Done():
			return
		case env := <-e.taskCh:
			e.running.Store(true)
			err := e.execute(env.task.Type, env.handler, env.task.Params)

			now := time.Now()
			result := &TaskResult{
				ID:          env.id,
				Type:        string(env.task.Type),
				Params:      env.task.Params,
				SubmittedAt: now,
				CompletedAt: &now,
			}
			if err != nil {
				result.Error = err.Error()
			}

			e.mu.Lock()
			e.results = append(e.results, result)
			if len(e.results) > maxResults {
				e.results = e.results[len(e.results)-maxResults:]
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
		return "", ErrBusy
	}
	id := uuid.New().String()
	e.taskCh <- taskEnvelope{id: id, task: task, handler: handler}
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
		Params:      task.Params,
		Schedule:    &sched,
		SubmittedAt: now,
		NextRunAt:   &next,
	}

	e.mu.Lock()
	e.scheduled[id] = tr
	e.mu.Unlock()

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
			log.Printf("[%s] scheduled task %s has no registered handler, removing", tr.Type, tr.ID)
			e.mu.Lock()
			delete(e.scheduled, tr.ID)
			e.mu.Unlock()
			continue
		}
		go func(tr *TaskResult, h TaskHandler) {
			err := e.execute(TaskType(tr.Type), h, tr.Params)

			e.mu.Lock()
			defer e.mu.Unlock()
			s, ok := e.scheduled[tr.ID]
			if !ok {
				return
			}
			t := now
			s.CompletedAt = &t
			s.Error = ""
			if err != nil {
				s.Error = err.Error()
			}
			if s.Schedule != nil && s.Schedule.Cron != "" {
				if next, cronErr := nextCronTime(s.Schedule.Cron, now); cronErr == nil {
					s.NextRunAt = &next
				}
			}
		}(tr, handler)
	}
}

// execute runs a handler synchronously and logs the outcome.
func (e *Engine) execute(taskType TaskType, handler TaskHandler, params map[string]any) error {
	if err := handler(e.ctx, params); err != nil {
		log.Printf("[%s] error: %v", taskType, err)
		return err
	}
	log.Printf("[%s] completed", taskType)
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
// combining completed one-shot results and active scheduled tasks.
func (e *Engine) RecentResults() []TaskResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	all := make([]TaskResult, 0, len(e.results)+len(e.scheduled))
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

// GetResult returns a task by ID (one-shot result or scheduled), or nil.
func (e *Engine) GetResult(id string) *TaskResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

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
