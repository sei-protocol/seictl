package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newTestEngine(handlers map[TaskType]TaskHandler) *Engine {
	return NewEngine(context.Background(), handlers)
}

func waitForHealthz(t *testing.T, eng *Engine) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eng.Healthz() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for healthz")
}

func waitForStatus(t *testing.T, eng *Engine, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eng.Status().Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q, got %q", want, eng.Status().Status)
}

func waitForLastTask(t *testing.T, eng *Engine) *TaskResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := eng.Status()
		if s.LastTask != nil {
			return s.LastTask
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for lastTask")
	return nil
}

func TestSubmitAccepts(t *testing.T) {
	eng := newTestEngine(map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	defer eng.Close()

	if err := eng.Submit(Task{Type: TaskMarkReady}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestSubmitRejectsUnknownType(t *testing.T) {
	eng := newTestEngine(map[TaskType]TaskHandler{})
	defer eng.Close()

	if err := eng.Submit(Task{Type: "nonexistent"}); err == nil {
		t.Fatal("expected error for unknown task type")
	}
}

func TestSubmitRejectsBusy(t *testing.T) {
	blocked := make(chan struct{})
	eng := NewEngine(context.Background(), map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocked
			return nil
		},
	})
	defer eng.Close()

	if err := eng.Submit(Task{Type: TaskConfigPatch}); err != nil {
		t.Fatalf("first submit failed: %v", err)
	}
	// Give the worker time to pick up the task so it holds taskMu.
	time.Sleep(20 * time.Millisecond)

	// Second submit: taskMu is held by the worker → ErrBusy.
	err := eng.Submit(Task{Type: TaskConfigPatch})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}

	close(blocked)
}

func TestMarkReadySetsHealthz(t *testing.T) {
	eng := newTestEngine(map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	defer eng.Close()

	if eng.Healthz() {
		t.Fatal("healthz should be false before mark-ready")
	}

	eng.Submit(Task{Type: TaskMarkReady})
	waitForHealthz(t, eng)

	if !eng.Healthz() {
		t.Fatal("healthz should be true after mark-ready")
	}
}

func TestHealthzMonotonicity(t *testing.T) {
	eng := newTestEngine(map[TaskType]TaskHandler{
		TaskMarkReady:   func(_ context.Context, _ map[string]any) error { return nil },
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error { return context.DeadlineExceeded },
	})
	defer eng.Close()

	eng.Submit(Task{Type: TaskMarkReady})
	waitForHealthz(t, eng)

	// Wait for first task to fully release the lock before submitting again.
	waitForStatus(t, eng, "ready")

	eng.Submit(Task{Type: TaskUpdatePeers})
	waitForStatus(t, eng, "ready")

	if !eng.Healthz() {
		t.Fatal("healthz should remain true after runtime failure")
	}
}

func TestStatusReflectsReady(t *testing.T) {
	eng := newTestEngine(map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	defer eng.Close()

	status := eng.Status()
	if status.Status != "not_ready" {
		t.Fatalf("expected not_ready initially, got %q", status.Status)
	}

	eng.Submit(Task{Type: TaskMarkReady})
	waitForStatus(t, eng, "ready")

	status = eng.Status()
	if status.Status != "ready" {
		t.Fatalf("expected ready after mark-ready, got %q", status.Status)
	}
}

func TestStatusReflectsRunning(t *testing.T) {
	started := make(chan struct{})
	blocked := make(chan struct{})
	eng := NewEngine(context.Background(), map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			close(started)
			<-blocked
			return nil
		},
	})
	defer eng.Close()

	eng.Submit(Task{Type: TaskConfigPatch})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	status := eng.Status()
	if status.Status != "running" {
		t.Fatalf("expected running while task executes, got %q", status.Status)
	}

	close(blocked)
	waitForStatus(t, eng, "not_ready")
}

func TestStatusLastTask(t *testing.T) {
	eng := newTestEngine(map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	defer eng.Close()

	if s := eng.Status(); s.LastTask != nil {
		t.Fatalf("expected nil lastTask initially, got %+v", s.LastTask)
	}

	eng.Submit(Task{Type: TaskConfigPatch})
	result := waitForLastTask(t, eng)

	if result.Type != string(TaskConfigPatch) {
		t.Fatalf("expected lastTask.Type %q, got %q", TaskConfigPatch, result.Type)
	}
	if result.Error != "" {
		t.Fatalf("expected no error, got %q", result.Error)
	}
}

func TestLastTaskOnError(t *testing.T) {
	handlerErr := errors.New("handler failed")
	eng := newTestEngine(map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return handlerErr },
	})
	defer eng.Close()

	eng.Submit(Task{Type: TaskConfigPatch})
	result := waitForLastTask(t, eng)

	if result.Type != string(TaskConfigPatch) {
		t.Fatalf("expected lastTask.Type %q, got %q", TaskConfigPatch, result.Type)
	}
	if result.Error != handlerErr.Error() {
		t.Fatalf("expected error %q, got %q", handlerErr.Error(), result.Error)
	}
}

func TestClose(t *testing.T) {
	var mu sync.Mutex
	executed := 0
	eng := NewEngine(context.Background(), map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			mu.Lock()
			executed++
			mu.Unlock()
			return nil
		},
	})

	eng.Submit(Task{Type: TaskConfigPatch})
	time.Sleep(50 * time.Millisecond)
	eng.Close()

	mu.Lock()
	if executed != 1 {
		t.Fatalf("expected 1 execution, got %d", executed)
	}
	mu.Unlock()
}

func TestEvalSchedulesSubmitsDueTasks(t *testing.T) {
	executed := make(chan struct{}, 1)
	eng := NewEngine(context.Background(), map[TaskType]TaskHandler{
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error {
			executed <- struct{}{}
			return nil
		},
	})
	defer eng.Close()

	sched, err := eng.AddSchedule(TaskUpdatePeers, nil, "* * * * *")
	if err != nil {
		t.Fatalf("add schedule failed: %v", err)
	}

	eng.scheduler.mu.Lock()
	past := time.Now().Add(-1 * time.Minute)
	eng.scheduler.schedules[sched.ID].NextRunAt = &past
	eng.scheduler.mu.Unlock()

	eng.EvalSchedules()

	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected scheduled task to execute")
	}
}

func TestScheduledTaskBypassesChannel(t *testing.T) {
	blocked := make(chan struct{})
	scheduled := make(chan struct{}, 1)

	eng := NewEngine(context.Background(), map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocked
			return nil
		},
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error {
			scheduled <- struct{}{}
			return nil
		},
	})
	defer eng.Close()

	eng.Submit(Task{Type: TaskConfigPatch})
	time.Sleep(20 * time.Millisecond)

	eng.SubmitScheduled(Task{Type: TaskUpdatePeers})

	select {
	case <-scheduled:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled task should bypass the blocked channel")
	}

	close(blocked)
}
