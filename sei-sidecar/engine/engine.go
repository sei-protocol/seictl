package engine

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Engine is the task executor. It runs tasks in goroutines and logs results.
type Engine struct {
	handlers  map[TaskType]TaskHandler
	scheduler *Scheduler
	mu        sync.Mutex
	ready     bool
}

// NewEngine creates a new Engine.
func NewEngine(handlers map[TaskType]TaskHandler) *Engine {
	return &Engine{
		handlers:  handlers,
		scheduler: NewScheduler(),
	}
}

// Submit runs a task asynchronously. Returns an error only if the task type
// is unknown. The task itself executes in a separate goroutine; its result
// is logged.
func (e *Engine) Submit(task Task) error {
	handler, ok := e.handlers[task.Type]
	if !ok {
		return fmt.Errorf("unknown task type: %s", task.Type)
	}
	go e.execute(task.Type, handler, task.Params)
	return nil
}

// execute runs a handler in its own goroutine, waits for the result on
// channels, and logs the outcome.
func (e *Engine) execute(taskType TaskType, handler TaskHandler, params map[string]any) {
	done := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		if err := handler(context.Background(), params); err != nil {
			errCh <- err
			return
		}
		close(done)
	}()

	select {
	case <-done:
		log.Printf("[%s] completed", taskType)
		if taskType == TaskMarkReady {
			e.mu.Lock()
			e.ready = true
			e.mu.Unlock()
		}
	case err := <-errCh:
		log.Printf("[%s] error: %v", taskType, err)
	}
}

// Healthz returns true after mark-ready has completed successfully.
func (e *Engine) Healthz() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ready
}

// Status returns a simple status snapshot.
func (e *Engine) Status() StatusResponse {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ready {
		return StatusResponse{Ready: true}
	}
	return StatusResponse{Ready: false}
}

// StatusResponse is the shape returned by the status endpoint.
type StatusResponse struct {
	Ready bool `json:"ready"`
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

// EvalSchedules evaluates due cron schedules and submits their tasks.
func (e *Engine) EvalSchedules() {
	now := time.Now()
	for _, d := range e.scheduler.Tick(now) {
		if err := e.Submit(Task{Type: d.TaskType, Params: d.Params}); err != nil {
			log.Printf("[scheduler] failed to submit %s: %v", d.TaskType, err)
			continue
		}
		e.scheduler.ConfirmRun(d.ScheduleID, now)
	}
}
