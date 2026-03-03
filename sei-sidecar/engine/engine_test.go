package engine

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"
)

func noopBlockHeight() (int64, error) { return 0, nil }

func immediateHandler(err error) TaskHandler {
	return func(_ context.Context, _ map[string]any) error { return err }
}

func drainWithTimeout(t *testing.T, eng *Engine) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		eng.DrainUpdates()
		if eng.Status().Phase != PhaseTaskRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for task to complete")
}

func TestSubmitAcceptsWhenIdle(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady: immediateHandler(nil),
	}, noopBlockHeight)

	id, err := eng.Submit(Task{Type: TaskMarkReady})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty task ID")
	}
}

func TestSubmitRejectsResourceConflict(t *testing.T) {
	blocker := make(chan struct{})
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocker
			return nil
		},
	}, noopBlockHeight)

	if _, err := eng.Submit(Task{Type: TaskConfigPatch}); err != nil {
		t.Fatalf("first submit should succeed: %v", err)
	}

	// Same task type means same resources — should conflict.
	_, err := eng.Submit(Task{Type: TaskConfigPatch})
	if err == nil {
		t.Fatal("second submit should fail due to resource conflict")
	}
	if !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("expected ErrResourceConflict, got %v", err)
	}
	close(blocker)
}

func TestBootstrapPhaseTransitionSuccess(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: immediateHandler(nil),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskConfigPatch})
	drainWithTimeout(t, eng)

	status := eng.Status()
	if status.Phase != PhaseTaskComplete {
		t.Fatalf("expected PhaseTaskComplete, got %s", status.Phase)
	}
	if !status.LastResult.Success {
		t.Fatal("expected success")
	}
}

func TestBootstrapPhaseTransitionFailure(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: immediateHandler(errors.New("boom")),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskConfigPatch})
	drainWithTimeout(t, eng)

	status := eng.Status()
	if status.Phase != PhaseTaskComplete {
		t.Fatalf("expected PhaseTaskComplete, got %s", status.Phase)
	}
	if status.LastResult.Success {
		t.Fatal("expected failure")
	}
}

func TestMarkReadyTransitionsToReady(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady: immediateHandler(nil),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	if eng.Status().Phase != PhaseReady {
		t.Fatalf("expected PhaseReady, got %s", eng.Status().Phase)
	}
	if !eng.Healthz() {
		t.Fatal("expected healthz true after mark-ready")
	}
}

func TestRuntimeTaskReturnsToReady(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady:   immediateHandler(nil),
		TaskUpdatePeers: immediateHandler(nil),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	eng.Submit(Task{Type: TaskUpdatePeers})
	drainWithTimeout(t, eng)

	if eng.Status().Phase != PhaseReady {
		t.Fatalf("expected PhaseReady after runtime task, got %s", eng.Status().Phase)
	}
}

func TestRuntimeTaskFailureReturnsToReady(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady:   immediateHandler(nil),
		TaskUpdatePeers: immediateHandler(errors.New("fail")),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	eng.Submit(Task{Type: TaskUpdatePeers})
	drainWithTimeout(t, eng)

	if eng.Status().Phase != PhaseReady {
		t.Fatalf("expected PhaseReady after runtime failure, got %s", eng.Status().Phase)
	}
}

func TestHealthzMonotonicity(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady:   immediateHandler(nil),
		TaskUpdatePeers: immediateHandler(errors.New("fail")),
	}, noopBlockHeight)

	if eng.Healthz() {
		t.Fatal("healthz should be false before mark-ready")
	}

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	if !eng.Healthz() {
		t.Fatal("healthz should be true after mark-ready")
	}

	eng.Submit(Task{Type: TaskUpdatePeers})
	drainWithTimeout(t, eng)

	if !eng.Healthz() {
		t.Fatal("healthz should remain true after runtime failure")
	}
}

func TestUnknownTaskTypeReportsError(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{}, noopBlockHeight)

	eng.Submit(Task{Type: TaskChangeMode})
	drainWithTimeout(t, eng)

	status := eng.Status()
	if status.LastResult.Success {
		t.Fatal("expected failure for unknown task type")
	}
	if status.LastResult.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestScheduleUpgradeSortedInsertion(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{}, noopBlockHeight)

	eng.ScheduleUpgrade(UpgradeTarget{Height: 300, Image: "img:3"})
	eng.ScheduleUpgrade(UpgradeTarget{Height: 100, Image: "img:1"})
	eng.ScheduleUpgrade(UpgradeTarget{Height: 200, Image: "img:2"})

	eng.mu.Lock()
	defer eng.mu.Unlock()

	if len(eng.pendingUpgrades) != 3 {
		t.Fatalf("expected 3 upgrades, got %d", len(eng.pendingUpgrades))
	}
	for i := 1; i < len(eng.pendingUpgrades); i++ {
		if eng.pendingUpgrades[i].Height < eng.pendingUpgrades[i-1].Height {
			t.Fatalf("upgrades not sorted: %v", eng.pendingUpgrades)
		}
	}
	if eng.pendingUpgrades[0].Height != 100 {
		t.Fatalf("expected first upgrade at height 100, got %d", eng.pendingUpgrades[0].Height)
	}
}

func TestScheduleUpgradeDoesNotChangePhase(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{}, noopBlockHeight)

	before := eng.Status().Phase
	eng.ScheduleUpgrade(UpgradeTarget{Height: 100, Image: "img:1"})
	after := eng.Status().Phase

	if before != after {
		t.Fatalf("phase changed from %s to %s", before, after)
	}
}

func TestCheckUpgradesFiresAtTargetHeight(t *testing.T) {
	var signalCalled bool
	origSignal := SignalSeidFn
	SignalSeidFn = func(_ syscall.Signal) { signalCalled = true }
	defer func() { SignalSeidFn = origSignal }()

	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady: immediateHandler(nil),
	}, func() (int64, error) { return 500, nil })

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	eng.ScheduleUpgrade(UpgradeTarget{Height: 500, Image: "img:new"})
	eng.CheckUpgrades()

	status := eng.Status()
	if status.Phase != PhaseUpgradeHalted {
		t.Fatalf("expected PhaseUpgradeHalted, got %s", status.Phase)
	}
	if status.UpgradeHeight != 500 {
		t.Fatalf("expected upgrade height 500, got %d", status.UpgradeHeight)
	}
	if status.UpgradeImage != "img:new" {
		t.Fatalf("expected upgrade image img:new, got %s", status.UpgradeImage)
	}
	if !signalCalled {
		t.Fatal("expected SIGTERM signal to be sent")
	}
}

func TestCheckUpgradesSkipsWhenNotReady(t *testing.T) {
	var signalCalled bool
	origSignal := SignalSeidFn
	SignalSeidFn = func(_ syscall.Signal) { signalCalled = true }
	defer func() { SignalSeidFn = origSignal }()

	eng := NewEngine("/tmp", map[TaskType]TaskHandler{}, func() (int64, error) { return 500, nil })

	eng.ScheduleUpgrade(UpgradeTarget{Height: 100, Image: "img:1"})
	eng.CheckUpgrades()

	if eng.Status().Phase == PhaseUpgradeHalted {
		t.Fatal("should not fire upgrade when not ready")
	}
	if signalCalled {
		t.Fatal("should not signal when not ready")
	}
}

func TestCheckUpgradesToleratesRPCError(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady: immediateHandler(nil),
	}, func() (int64, error) { return 0, errors.New("rpc down") })

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	eng.ScheduleUpgrade(UpgradeTarget{Height: 100, Image: "img:1"})
	eng.CheckUpgrades()

	if eng.Status().Phase != PhaseReady {
		t.Fatalf("expected PhaseReady after RPC error, got %s", eng.Status().Phase)
	}
	if eng.Status().PendingUpgrades != 1 {
		t.Fatal("pending upgrade should be retained after RPC error")
	}
}

func TestCheckUpgradesLowestHeightFirst(t *testing.T) {
	origSignal := SignalSeidFn
	SignalSeidFn = func(_ syscall.Signal) {}
	defer func() { SignalSeidFn = origSignal }()

	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady: immediateHandler(nil),
	}, func() (int64, error) { return 500, nil })

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	eng.ScheduleUpgrade(UpgradeTarget{Height: 300, Image: "img:3"})
	eng.ScheduleUpgrade(UpgradeTarget{Height: 100, Image: "img:1"})
	eng.ScheduleUpgrade(UpgradeTarget{Height: 200, Image: "img:2"})

	eng.CheckUpgrades()

	status := eng.Status()
	if status.UpgradeHeight != 100 {
		t.Fatalf("expected lowest height 100 to fire first, got %d", status.UpgradeHeight)
	}
	if status.PendingUpgrades != 2 {
		t.Fatalf("expected 2 remaining upgrades, got %d", status.PendingUpgrades)
	}
}

func TestSerialExecutionConcurrent(t *testing.T) {
	var running sync.WaitGroup
	running.Add(1)
	blocker := make(chan struct{})

	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			running.Done()
			<-blocker
			return nil
		},
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskConfigPatch})
	running.Wait()

	errs := make(chan error, 10)
	for range 10 {
		go func() {
			_, err := eng.Submit(Task{Type: TaskConfigPatch})
			errs <- err
		}()
	}

	rejected := 0
	for range 10 {
		if err := <-errs; err != nil {
			rejected++
		}
	}
	if rejected != 10 {
		t.Fatalf("expected all 10 concurrent submits rejected, got %d rejected", rejected)
	}
	close(blocker)
}

func TestCheckUpgradesDoesNotFireBelowHeight(t *testing.T) {
	origSignal := SignalSeidFn
	SignalSeidFn = func(_ syscall.Signal) {}
	defer func() { SignalSeidFn = origSignal }()

	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskMarkReady: immediateHandler(nil),
	}, func() (int64, error) { return 50, nil })

	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	eng.ScheduleUpgrade(UpgradeTarget{Height: 100, Image: "img:1"})
	eng.CheckUpgrades()

	if eng.Status().Phase != PhaseReady {
		t.Fatalf("should not fire upgrade below target height, got %s", eng.Status().Phase)
	}
}

func TestSubmitTracksTask(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: immediateHandler(nil),
	}, noopBlockHeight)

	id, err := eng.Submit(Task{Type: TaskConfigPatch, Params: map[string]any{"k": "v"}})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	task := eng.GetTask(id)
	if task == nil {
		t.Fatal("expected tracked task")
	}
	if task.Type != TaskConfigPatch {
		t.Fatalf("expected config-patch, got %s", task.Type)
	}
	if task.State != TaskStateRunning {
		t.Fatalf("expected running, got %s", task.State)
	}

	drainWithTimeout(t, eng)

	task = eng.GetTask(id)
	if task.State != TaskStateCompleted {
		t.Fatalf("expected completed, got %s", task.State)
	}
}

func TestSubmitTracksFailedTask(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: immediateHandler(errors.New("boom")),
	}, noopBlockHeight)

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	drainWithTimeout(t, eng)

	task := eng.GetTask(id)
	if task.State != TaskStateFailed {
		t.Fatalf("expected failed, got %s", task.State)
	}
	if task.Error != "boom" {
		t.Fatalf("expected error 'boom', got %q", task.Error)
	}
}

func TestListTasksReturnsHistory(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: immediateHandler(nil),
		TaskMarkReady:   immediateHandler(nil),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskConfigPatch})
	drainWithTimeout(t, eng)
	eng.Submit(Task{Type: TaskMarkReady})
	drainWithTimeout(t, eng)

	tasks := eng.ListTasks("", 0)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	// newest first
	if tasks[0].Type != TaskMarkReady {
		t.Fatalf("expected mark-ready first, got %s", tasks[0].Type)
	}
}

func TestActiveTasksInStatus(t *testing.T) {
	blocker := make(chan struct{})
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocker
			return nil
		},
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskConfigPatch})

	status := eng.Status()
	if status.ActiveTasks != 1 {
		t.Fatalf("expected 1 active task, got %d", status.ActiveTasks)
	}
	close(blocker)
}

func TestConcurrentDisjointResourcesAccepted(t *testing.T) {
	blockerGenesis := make(chan struct{})
	blockerSnapshot := make(chan struct{})

	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigureGenesis: func(_ context.Context, _ map[string]any) error {
			<-blockerGenesis
			return nil
		},
		TaskSnapshotUpload: func(_ context.Context, _ map[string]any) error {
			<-blockerSnapshot
			return nil
		},
	}, noopBlockHeight)

	// Genesis writes genesis.json; snapshot-upload reads snapshots/ + writes upload-state.json.
	// No overlap — both should be accepted.
	_, err1 := eng.Submit(Task{Type: TaskConfigureGenesis})
	_, err2 := eng.Submit(Task{Type: TaskSnapshotUpload})

	if err1 != nil {
		t.Fatalf("genesis submit should succeed: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("snapshot-upload submit should succeed: %v", err2)
	}

	status := eng.Status()
	if status.ActiveTasks != 2 {
		t.Fatalf("expected 2 active tasks, got %d", status.ActiveTasks)
	}

	close(blockerGenesis)
	close(blockerSnapshot)
}

func TestConcurrentOverlappingResourcesRejected(t *testing.T) {
	blocker := make(chan struct{})
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocker
			return nil
		},
		TaskUpdatePeers: immediateHandler(nil),
	}, noopBlockHeight)

	// config-patch writes config.toml; update-peers also writes config.toml.
	if _, err := eng.Submit(Task{Type: TaskConfigPatch}); err != nil {
		t.Fatalf("config-patch should succeed: %v", err)
	}
	if _, err := eng.Submit(Task{Type: TaskUpdatePeers}); err == nil {
		t.Fatal("update-peers should be rejected due to config.toml conflict")
	}
	close(blocker)
}

func TestResourceReleaseAllowsResubmit(t *testing.T) {
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskConfigPatch: immediateHandler(nil),
	}, noopBlockHeight)

	eng.Submit(Task{Type: TaskConfigPatch})
	drainWithTimeout(t, eng)

	// After completion, resources should be released.
	_, err := eng.Submit(Task{Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("resubmit after completion should succeed: %v", err)
	}
}

func TestEvalSchedulesSubmitsDueTasks(t *testing.T) {
	var executed bool
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error {
			executed = true
			return nil
		},
	}, noopBlockHeight)

	sched, err := eng.AddSchedule(TaskUpdatePeers, nil, "* * * * *", nil)
	if err != nil {
		t.Fatalf("add schedule failed: %v", err)
	}

	// Manually advance time past NextRunAt and call EvalSchedules.
	// We set NextRunAt to the past so it fires immediately.
	eng.scheduler.mu.Lock()
	past := time.Now().Add(-1 * time.Minute)
	eng.scheduler.schedules[sched.ID].NextRunAt = &past
	eng.scheduler.mu.Unlock()

	eng.EvalSchedules()
	drainWithTimeout(t, eng)

	if !executed {
		t.Fatal("expected scheduled task to execute")
	}

	// Schedule should have advanced.
	updated := eng.GetSchedule(sched.ID)
	if updated.LastRunAt == nil {
		t.Fatal("expected LastRunAt to be set")
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(*updated.LastRunAt) {
		t.Fatal("expected NextRunAt to advance past LastRunAt")
	}
}

func TestEvalSchedulesSkipsOnResourceConflict(t *testing.T) {
	blocker := make(chan struct{})
	eng := NewEngine("/tmp", map[TaskType]TaskHandler{
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error {
			<-blocker
			return nil
		},
	}, noopBlockHeight)

	// Submit a task that holds update-peers resources.
	eng.Submit(Task{Type: TaskUpdatePeers})

	sched, _ := eng.AddSchedule(TaskUpdatePeers, nil, "* * * * *", nil)
	eng.scheduler.mu.Lock()
	past := time.Now().Add(-1 * time.Minute)
	eng.scheduler.schedules[sched.ID].NextRunAt = &past
	eng.scheduler.mu.Unlock()

	eng.EvalSchedules()

	// Schedule should NOT have been confirmed (still at old NextRunAt).
	updated := eng.GetSchedule(sched.ID)
	if updated.LastRunAt != nil {
		t.Fatal("schedule should not be confirmed when resource conflict occurs")
	}

	close(blocker)
}
