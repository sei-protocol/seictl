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

// DefaultSubmitTimeout is how long Submit waits to enqueue a task before
// returning ErrBusy.
const DefaultSubmitTimeout = 5 * time.Second

// ErrBusy is returned by Submit when a non-scheduled task is already queued
// or running.
var ErrBusy = errors.New("a task is already running")

// taskEnvelope wraps a task with its resolved handler for the worker loop.
type taskEnvelope struct {
	taskType TaskType
	handler  TaskHandler
	params   map[string]any
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Status string `json:"status"`
}

// Engine is the task executor. Non-scheduled tasks are serialized through a
// buffered channel so that at most one runs at a time. Scheduled tasks bypass
// the channel and run in their own goroutines.
type Engine struct {
	handlers       map[TaskType]TaskHandler
	scheduler      *Scheduler
	ctx            context.Context
	cancel         context.CancelFunc
	taskCh         chan taskEnvelope
	running        atomic.Bool
	mu             sync.RWMutex
	ready          bool
	SubmitTimeout  time.Duration
	OnTaskComplete func(TaskType)
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
func (e *Engine) loop() {
	for env := range e.taskCh {
		e.running.Store(true)
		e.execute(env.taskType, env.handler, env.params)
		e.running.Store(false)
	}
}

// Submit enqueues a non-scheduled task. It returns ErrBusy if the task cannot
// be enqueued within DefaultSubmitTimeout (i.e. another task is already queued
// or running).
func (e *Engine) Submit(task Task) error {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return fmt.Errorf("unknown task type: %s", task.Type)
	}
	timeout := e.SubmitTimeout
	if timeout == 0 {
		timeout = DefaultSubmitTimeout
	}
	select {
	case e.taskCh <- taskEnvelope{taskType: task.Type, handler: handler, params: task.Params}:
		return nil
	case <-time.After(timeout):
		return ErrBusy
	}
}

// SubmitScheduled runs a task directly in a goroutine, bypassing the
// single-task channel. Use this for cron-triggered tasks that should not
// block or be blocked by non-scheduled work.
func (e *Engine) SubmitScheduled(task Task) error {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return fmt.Errorf("unknown task type: %s", task.Type)
	}
	go e.execute(task.Type, handler, task.Params)
	return nil
}

// execute runs a handler synchronously and logs the outcome.
func (e *Engine) execute(taskType TaskType, handler TaskHandler, params map[string]any) {
	if err := handler(e.ctx, params); err != nil {
		log.Printf("[%s] error: %v", taskType, err)
		return
	}
	log.Printf("[%s] completed", taskType)
	if e.OnTaskComplete != nil {
		e.OnTaskComplete(taskType)
	}
}

// SetReady marks the engine as ready. Intended to be called from an
// OnTaskComplete callback.
func (e *Engine) SetReady() {
	e.mu.Lock()
	e.ready = true
	e.mu.Unlock()
}

// Healthz returns true after the engine has been marked ready.
func (e *Engine) Healthz() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ready
}

// Status returns a snapshot of the engine's current state.
func (e *Engine) Status() StatusResponse {
	if e.running.Load() || len(e.taskCh) > 0 {
		return StatusResponse{Status: "running"}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.ready {
		return StatusResponse{Status: "ready"}
	}
	return StatusResponse{Status: "not_ready"}
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
// SubmitScheduled so they bypass the single-task channel.
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
