package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
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

	// Config is set once during single-threaded startup before Submit
	// is reachable; read-only thereafter. No synchronization.
	Config ExecutionConfig
}

// NewEngine creates a new Engine. The engine runs until ctx is cancelled.
// Callers MUST install handler dependencies on e.Config before
// RehydrateStaleTasks, else a rehydrated handler races with the write.
func NewEngine(ctx context.Context, handlers map[TaskType]TaskHandler, store ResultStore) *Engine {
	return &Engine{
		handlers: handlers,
		ctx:      ctx,
		store:    store,
	}
}

// RehydrateStaleTasks re-executes tasks left in "running" state by a
// previous process that exited before completing them. Run count is
// NOT incremented — rehydration is crash recovery of an incomplete
// run, not a new run. Must be called only after Config is installed.
func (e *Engine) RehydrateStaleTasks() {
	stale, err := e.store.ListStaleTasks()
	if err != nil {
		log.Error("failed to list stale tasks", "err", err)
		return
	}

	// A node hold must win crash recovery deterministically. Run any stale
	// mark-not-ready SYNCHRONOUSLY FIRST: its handler purges stranded mark-ready
	// rows from the store and its completion hook flips readiness false. This
	// removes the Store(true)/Store(false) race that goroutine dispatch would
	// otherwise create between a stranded mark-ready and a stranded
	// mark-not-ready — a race that could transiently release a held seid.
	//
	// CONTRACT: a hold handler run here must stay bounded (a single store op).
	// This executes before the HTTP server's ListenAndServe, so livez does not
	// serve yet — a slow handler here silently delays startup with no signal.
	holdSeen := false
	for _, tr := range stale {
		if TaskType(tr.Type) != TaskMarkNotReady {
			continue
		}
		holdSeen = true
		if handler, ok := e.resolveStaleHandler(tr); ok {
			log.Info("rehydrating hold synchronously before other tasks",
				"type", tr.Type, "id", tr.ID, "run", tr.Run)
			e.runTaskSync(tr.ID, TaskType(tr.Type), handler, tr.Params, tr.SubmittedAt, tr.Run)
		}
	}

	// Re-read the source of truth after the purge so the mark-ready rows it
	// deleted (and the now-terminal mark-not-ready) are not dispatched.
	if holdSeen {
		stale, err = e.store.ListStaleTasks()
		if err != nil {
			log.Error("failed to re-list stale tasks after hold", "err", err)
			return
		}
	}

	for _, tr := range stale {
		switch TaskType(tr.Type) {
		case TaskMarkNotReady:
			continue // handled synchronously above
		case TaskMarkReady:
			// Fail closed: if a hold was in flight at the crash, never let a
			// stranded mark-ready from the same era release it — even if the
			// synchronous purge above could not delete this row (a broken
			// store). The row is left running rather than dispatched.
			if holdSeen {
				log.Warn("skipping stranded mark-ready while a hold was recovering", "id", tr.ID)
				continue
			}
		}
		if handler, ok := e.resolveStaleHandler(tr); ok {
			log.Info("rehydrating stale task", "type", tr.Type, "id", tr.ID, "run", tr.Run)
			e.runTask(tr.ID, TaskType(tr.Type), handler, tr.Params, tr.SubmittedAt, tr.Run)
		}
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
	go e.runTaskSync(id, taskType, handler, params, submittedAt, run)
}

// runTaskSync runs the handler and persists the result, blocking until done.
// runTask wraps it in a goroutine; RehydrateStaleTasks calls it directly for a
// stale mark-not-ready that must complete (purge + readiness flip) before any
// other stale task is dispatched.
func (e *Engine) runTaskSync(id string, taskType TaskType, handler TaskHandler, params map[string]any, submittedAt time.Time, run int) {
	// Thread the task id through ctx for handlers that need it (e.g.
	// sign-tx memo tagging).
	ctx := WithTaskID(e.ctx, id)
	result, err := e.executeRecovered(ctx, taskType, handler, params)

	// Engine shutdown truncated the task — leave it 'running' so
	// RehydrateStaleTasks resumes it on restart, rather than persisting a
	// spurious Failed (which would strand an in-flight sign-tx). Safe: this
	// path uses e.ctx directly (no per-task deadline), so context.Canceled
	// here is only shutdown.
	if errors.Is(err, context.Canceled) {
		log.Info("task truncated by shutdown; left running for rehydration",
			"type", taskType, "id", id, "run", run)
		return
	}

	t := time.Now().UTC()
	tr := &TaskResult{
		ID:          id,
		Type:        string(taskType),
		Run:         run,
		Params:      params,
		SubmittedAt: submittedAt,
		CompletedAt: &t,
	}
	// Stamp on both paths — a failed run may still carry a result (e.g. a
	// tx hash); a panic yields nil, so nothing partial is stamped.
	tr.Result = result
	if err != nil {
		tr.Error = err.Error()
		tr.Status = TaskStatusFailed
	} else {
		tr.Status = TaskStatusCompleted
	}

	if storeErr := e.store.Save(tr); storeErr != nil {
		log.Error("failed to persist task result", "id", id, "err", storeErr)
	}
}

// resolveStaleHandler returns the handler for a stale task. When no handler is
// registered it marks the task failed, persists that, and returns ok=false.
func (e *Engine) resolveStaleHandler(tr TaskResult) (TaskHandler, bool) {
	handler, ok := e.handlers[TaskType(tr.Type)]
	if ok {
		return handler, true
	}
	log.Warn("stale task has no handler, marking failed", "type", tr.Type, "id", tr.ID)
	t := time.Now().UTC()
	tr.Status = TaskStatusFailed
	tr.Error = "no handler registered for task type"
	tr.CompletedAt = &t
	if err := e.store.Save(&tr); err != nil {
		log.Error("failed to persist stale task failure", "id", tr.ID, "err", err)
	}
	return nil, false
}

// executeRecovered runs execute under a recover so a handler panic becomes a
// failed TaskResult instead of taking down the shared sidecar process. It
// guards only the handler goroutine; a task that spawns its own goroutines must
// recover within them (e.g. s3.streamGzip's writer).
func (e *Engine) executeRecovered(ctx context.Context, taskType TaskType, handler TaskHandler, params map[string]any) (result json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("task handler panicked", "type", taskType, "panic", r, "stack", string(debug.Stack()))
			taskPanics.WithLabelValues(string(taskType)).Inc()
			taskFailures.WithLabelValues(string(taskType)).Inc()
			err = fmt.Errorf("task handler panicked: %v", r)
		}
	}()
	return e.execute(ctx, taskType, handler, params)
}

// execute runs a handler synchronously and logs the outcome.
func (e *Engine) execute(ctx context.Context, taskType TaskType, handler TaskHandler, params map[string]any) (json.RawMessage, error) {
	start := time.Now()
	result, err := handler(ctx, params)
	if err != nil {
		elapsed := time.Since(start)
		log.Error("task failed", "type", taskType, "elapsed", elapsed.Round(time.Millisecond), "err", err)
		taskDuration.WithLabelValues(string(taskType), "failed").Observe(elapsed.Seconds())
		taskFailures.WithLabelValues(string(taskType)).Inc()
		return result, err
	}
	elapsed := time.Since(start)
	log.Info("task completed", "type", taskType, "elapsed", elapsed.Round(time.Millisecond))
	taskDuration.WithLabelValues(string(taskType), "completed").Observe(elapsed.Seconds())
	// These two hooks are the only writers of e.ready: mark-ready flips it true,
	// mark-not-ready flips it false to re-arm the start gate for a node hold.
	// The flip happens only on handler success — a failed mark-not-ready purge
	// leaves readiness untouched (fail-safe: nothing half-held).
	//
	// Crash-recovery ordering is enforced in RehydrateStaleTasks, which runs a
	// stranded mark-not-ready synchronously (purging stranded mark-ready rows)
	// before dispatching anything else, so no Store(true)/Store(false) race can
	// transiently release a hold. The one interlock this engine does NOT own: on
	// the live path, a controller-submitted mark-ready concurrent with an active
	// hold — that mutual exclusion is the controller's (reapproval suppression
	// keyed on adoptedWorkflow), not enforced here.
	switch taskType {
	case TaskMarkReady:
		e.ready.Store(true)
	case TaskMarkNotReady:
		e.ready.Store(false)
	}
	return result, nil
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
