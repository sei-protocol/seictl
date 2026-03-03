package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"syscall"
	"time"
)

// ErrResourceConflict is returned by Submit when the requested task's resources
// overlap with a currently-running task.
var ErrResourceConflict = errors.New("resource conflict")

// taskCompletion is sent on the internal channel when a task goroutine finishes.
type taskCompletion struct {
	ID      TaskID
	Task    TaskType
	Success bool
	Error   string
}

// Engine is the concurrent task executor. Non-overlapping tasks run in parallel;
// resource conflicts are rejected with ErrResourceConflict.
//
// Scheduled upgrades are stored separately from the task executor. The
// POST /task handler for schedule-upgrade writes to pendingUpgrades and
// returns 202 immediately — it does NOT go through Submit/executeTask.
// A separate upgrade ticker goroutine calls CheckUpgrades(), which queries
// seid's block height lock-free, then acquires the mutex only for the
// state check and transition.
type Engine struct {
	homeDir         string
	handlers        map[TaskType]TaskHandler
	blockHeightFn   func() (int64, error)
	taskUpdate      chan taskCompletion
	tracker         *TaskTracker
	locker          *ResourceLocker
	scheduler       *Scheduler
	mu              sync.Mutex
	phase           Phase
	activeCount     int
	currentTask     *Task
	lastResult      *TaskResult
	ready           bool
	pendingUpgrades []UpgradeTarget
	upgradeTarget   *UpgradeTarget
}

// NewEngine creates a new Engine in the Initialized phase.
func NewEngine(homeDir string, handlers map[TaskType]TaskHandler, blockHeightFn func() (int64, error)) *Engine {
	return &Engine{
		homeDir:       homeDir,
		handlers:      handlers,
		blockHeightFn: blockHeightFn,
		taskUpdate:    make(chan taskCompletion, 16),
		tracker:       NewTaskTracker(),
		locker:        NewResourceLocker(),
		scheduler:     NewScheduler(),
		phase:         PhaseInitialized,
	}
}

// Submit enqueues a task for execution. Returns the task ID and nil on success,
// or empty string and an error if the task's resources conflict with running tasks.
func (e *Engine) Submit(task Task) (TaskID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	accesses := TaskResources[task.Type]
	if !e.locker.TryAcquire(accesses) {
		return "", fmt.Errorf("%w: cannot acquire resources for %s", ErrResourceConflict, task.Type)
	}

	id := e.tracker.Create(task.Type, task.Params)
	e.tracker.MarkRunning(id)

	e.activeCount++
	e.currentTask = &task
	e.phase = PhaseTaskRunning
	e.lastResult = nil

	go e.executeTask(id, task, accesses)
	return id, nil
}

// executeTask runs in its own goroutine without holding any lock.
func (e *Engine) executeTask(id TaskID, task Task, accesses []ResourceAccess) {
	defer e.locker.Release(accesses)

	handler, ok := e.handlers[task.Type]
	if !ok {
		e.taskUpdate <- taskCompletion{
			ID:      id,
			Task:    task.Type,
			Success: false,
			Error:   fmt.Sprintf("unknown task type: %s", task.Type),
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	err := handler(ctx, task.Params)

	tc := taskCompletion{ID: id, Task: task.Type, Success: err == nil}
	if err != nil {
		tc.Error = err.Error()
	}
	e.taskUpdate <- tc
}

// DrainUpdates checks the taskUpdate channel for completed results and
// updates cached status. Called by the drain ticker every 100ms.
// Drains ALL pending completions per tick, then updates phase once.
// Does NOT check pending upgrades — that is handled by CheckUpgrades.
func (e *Engine) DrainUpdates() {
	e.mu.Lock()
	defer e.mu.Unlock()

	drained := false
	for {
		select {
		case tc := <-e.taskUpdate:
			drained = true
			result := &TaskResult{
				Task:    tc.Task,
				Success: tc.Success,
				Error:   tc.Error,
			}
			e.lastResult = result
			e.activeCount--

			if tc.Success {
				e.tracker.MarkCompleted(tc.ID)
			} else {
				e.tracker.MarkFailed(tc.ID, tc.Error)
			}

			if tc.Task == TaskMarkReady && tc.Success {
				e.ready = true
			}
		default:
			goto done
		}
	}
done:
	if !drained {
		return
	}

	e.updatePhase()
}

// updatePhase sets the engine phase based on active task count and ready state.
// Caller must hold e.mu.
func (e *Engine) updatePhase() {
	if e.activeCount > 0 {
		e.phase = PhaseTaskRunning
		return
	}

	e.currentTask = nil

	if e.ready {
		e.phase = PhaseReady
		return
	}

	e.phase = PhaseTaskComplete
}

// Status returns a snapshot of the engine's current state.
func (e *Engine) Status() EngineStatus {
	e.mu.Lock()
	defer e.mu.Unlock()

	status := EngineStatus{
		Phase:           e.phase,
		CurrentTask:     e.currentTask,
		LastResult:      e.lastResult,
		ActiveTasks:     e.activeCount,
		PendingUpgrades: len(e.pendingUpgrades),
	}
	if e.upgradeTarget != nil {
		status.UpgradeHeight = e.upgradeTarget.Height
		status.UpgradeImage = e.upgradeTarget.Image
	}
	return status
}

// Healthz returns true only after mark-ready has completed.
func (e *Engine) Healthz() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ready
}

// GetTask returns a tracked task by ID, or nil if not found.
func (e *Engine) GetTask(id TaskID) *TrackedTask {
	return e.tracker.Get(id)
}

// ListTasks returns tracked tasks in newest-first order.
func (e *Engine) ListTasks(stateFilter TaskState, limit int) []TrackedTask {
	return e.tracker.List(stateFilter, limit)
}

// ScheduleUpgrade stores an upgrade target for monitoring by the upgrade ticker.
// Inserts in ascending height order so the lowest-height upgrade fires first.
func (e *Engine) ScheduleUpgrade(target UpgradeTarget) {
	e.mu.Lock()
	defer e.mu.Unlock()

	idx := sort.Search(len(e.pendingUpgrades), func(i int) bool {
		return e.pendingUpgrades[i].Height >= target.Height
	})
	e.pendingUpgrades = slices.Insert(e.pendingUpgrades, idx, target)
}

// CheckUpgrades queries seid's current block height WITHOUT holding the lock,
// then acquires the mutex to check pending upgrades.
func (e *Engine) CheckUpgrades() {
	currentHeight, err := e.blockHeightFn()
	if err != nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.ready || len(e.pendingUpgrades) == 0 {
		return
	}

	e.checkPendingUpgradesWithHeight(currentHeight)
}

// SignalSeidFn is the function used to signal the seid process.
// Replaceable for testing.
var SignalSeidFn = signalSeid

// checkPendingUpgradesWithHeight fires the lowest-height pending upgrade
// that has been reached. Caller must hold e.mu.
func (e *Engine) checkPendingUpgradesWithHeight(currentHeight int64) {
	for i, upgrade := range e.pendingUpgrades {
		if currentHeight >= upgrade.Height {
			SignalSeidFn(syscall.SIGTERM)
			e.phase = PhaseUpgradeHalted
			fired := e.pendingUpgrades[i]
			e.upgradeTarget = &fired
			e.pendingUpgrades = slices.Delete(e.pendingUpgrades, i, i+1)
			return
		}
	}
}

// AddSchedule creates a new time-based schedule.
func (e *Engine) AddSchedule(taskType TaskType, params map[string]any, cronExpr string, runAt *time.Time) (*Schedule, error) {
	return e.scheduler.Add(taskType, params, cronExpr, runAt)
}

// RemoveSchedule deletes a schedule by ID. Returns true if found.
func (e *Engine) RemoveSchedule(id ScheduleID) bool {
	return e.scheduler.Remove(id)
}

// GetSchedule returns a schedule by ID, or nil if not found.
func (e *Engine) GetSchedule(id ScheduleID) *Schedule {
	return e.scheduler.Get(id)
}

// ListSchedules returns all schedules.
func (e *Engine) ListSchedules() []Schedule {
	return e.scheduler.List()
}

// EvalSchedules evaluates due schedules and submits their tasks. Called by the
// scheduler ticker. If Submit fails (e.g. resource conflict), the schedule is
// not confirmed and will retry on the next tick.
func (e *Engine) EvalSchedules() {
	now := time.Now()
	due := e.scheduler.Tick(now)
	for _, d := range due {
		if _, err := e.Submit(Task{Type: d.TaskType, Params: d.Params}); err == nil {
			e.scheduler.ConfirmRun(d.ScheduleID, now)
		}
	}
}

// signalSeid finds the seid process and sends the specified signal.
// Placeholder — full implementation in tasks package (Task 3.6).
func signalSeid(_ syscall.Signal) {
	// Implemented by tasks.SignalSeid in the tasks package.
}
