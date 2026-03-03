package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBusy is returned by Submit when a non-scheduled task is already running.
var ErrBusy = errors.New("a task is already running")

// taskEnvelope wraps a task with its resolved handler for the worker loop.
type taskEnvelope struct {
	taskType TaskType
	handler  TaskHandler
	params   map[string]any
}

// TaskResult records the outcome of the most recent non-scheduled task.
type TaskResult struct {
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Status   string      `json:"status"`
	LastTask *TaskResult `json:"lastTask,omitempty"`
}

// Engine is the task executor. Non-scheduled tasks are serialized through a
// channel with a TryLock gate: the lock is acquired by Submit and released by
// the worker after execution completes, ensuring exactly one task is in the
// system at a time. Scheduled tasks bypass this and run in their own goroutines.
type Engine struct {
	handlers  map[TaskType]TaskHandler
	scheduler *Scheduler
	ctx       context.Context
	cancel    context.CancelFunc
	taskCh    chan taskEnvelope
	taskMu    sync.Mutex
	running   atomic.Bool
	mu        sync.RWMutex
	ready     bool
	lastTask  *TaskResult
}

// NewEngine creates a new Engine and starts its worker loop. The provided
// context controls the lifetime of the worker and is passed to task handlers.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler) *Engine {
	ctx, cancel := context.WithCancel(ctx)
	e := &Engine{
		handlers:  handlers,
		scheduler: NewScheduler(),
		ctx:       ctx,
		cancel:    cancel,
		taskCh:    make(chan taskEnvelope, 1),
	}
	go e.loop()
	return e
}

// loop is the serial worker that processes non-scheduled tasks one at a time.
// After each task completes it stores the result and releases the task lock.
func (e *Engine) loop() {
	for env := range e.taskCh {
		e.running.Store(true)
		err := e.execute(env.taskType, env.handler, env.params)

		result := &TaskResult{Type: string(env.taskType)}
		if err != nil {
			result.Error = err.Error()
		}
		e.mu.Lock()
		e.lastTask = result
		e.mu.Unlock()

		e.running.Store(false)
		e.taskMu.Unlock()
	}
}

// Submit enqueues a non-scheduled task. It returns ErrBusy if another task is
// already in flight (the lock is held from submission until the worker finishes
// executing the task).
func (e *Engine) Submit(task Task) error {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return fmt.Errorf("unknown task type: %s", task.Type)
	}
	if !e.taskMu.TryLock() {
		return ErrBusy
	}
	e.taskCh <- taskEnvelope{taskType: task.Type, handler: handler, params: task.Params}
	return nil
}

// SubmitScheduled runs a task directly in a goroutine, bypassing the
// single-task lock. Use this for cron-triggered tasks that should not
// block or be blocked by non-scheduled work.
func (e *Engine) SubmitScheduled(task Task) error {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return fmt.Errorf("unknown task type: %s", task.Type)
	}
	go e.execute(task.Type, handler, task.Params)
	return nil
}

// execute runs a handler synchronously and logs the outcome. Returns the
// handler error (if any) so the caller can record it.
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

// Status returns a snapshot of the engine's current state, including the
// result of the most recent non-scheduled task.
func (e *Engine) Status() StatusResponse {
	if e.running.Load() {
		return StatusResponse{Status: "running"}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	status := "not_ready"
	if e.ready {
		status = "ready"
	}
	return StatusResponse{Status: status, LastTask: e.lastTask}
}

// Close shuts down the worker loop and cancels the engine context.
func (e *Engine) Close() {
	e.cancel()
	close(e.taskCh)
}

// AddSchedule creates a cron schedule for a task type.
func (e *Engine) AddSchedule(taskType TaskType, params map[string]any, cronExpr string) (*Schedule, error) {
	return e.scheduler.Add(taskType, params, cronExpr)
}

// RemoveSchedule deletes a schedule by ID. Returns true if found.
func (e *Engine) RemoveSchedule(id string) bool {
	return e.scheduler.Remove(id)
}

// ListSchedules returns all schedules.
func (e *Engine) ListSchedules() []Schedule {
	return e.scheduler.List()
}

// EvalSchedules evaluates due cron schedules and submits their tasks via
// SubmitScheduled so they bypass the single-task lock.
func (e *Engine) EvalSchedules() {
	now := time.Now()
	for _, d := range e.scheduler.Tick(now) {
		if err := e.SubmitScheduled(Task{Type: d.TaskType, Params: d.Params}); err != nil {
			log.Printf("[scheduler] failed to submit %s: %v", d.TaskType, err)
			continue
		}
		e.scheduler.ConfirmRun(d.ScheduleID, now)
	}
}
