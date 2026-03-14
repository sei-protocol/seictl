package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newTestEngine(t *testing.T, handlers map[TaskType]TaskHandler) *Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewEngine(ctx, handlers)
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

func waitForResult(t *testing.T, eng *Engine, id string) *TaskResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r := eng.GetResult(id); r != nil && r.CompletedAt != nil {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for result %s", id)
	return nil
}

func TestSubmitAccepts(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id, err := eng.Submit(Task{Type: TaskMarkReady})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
}

func TestSubmitRejectsUnknownType(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{})

	_, err := eng.Submit(Task{Type: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown task type")
	}
}

func TestSubmitRejectsBusy(t *testing.T) {
	blocked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocked
			return nil
		},
	})

	if _, err := eng.Submit(Task{Type: TaskConfigPatch}); err != nil {
		t.Fatalf("first submit failed: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	_, err := eng.Submit(Task{Type: TaskConfigPatch})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}

	close(blocked)
}

func TestMarkReadySetsHealthz(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})

	if eng.Healthz() {
		t.Fatal("healthz should be false before mark-ready")
	}

	_, _ = eng.Submit(Task{Type: TaskMarkReady})
	waitForHealthz(t, eng)

	if !eng.Healthz() {
		t.Fatal("healthz should be true after mark-ready")
	}
}

func TestHealthzMonotonicity(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskMarkReady:   func(_ context.Context, _ map[string]any) error { return nil },
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return context.DeadlineExceeded },
	})

	id, _ := eng.Submit(Task{Type: TaskMarkReady})
	waitForHealthz(t, eng)
	waitForResult(t, eng, id)

	_, _ = eng.Submit(Task{Type: TaskConfigPatch})
	waitForStatus(t, eng, "Ready")

	if !eng.Healthz() {
		t.Fatal("healthz should remain true after runtime failure")
	}
}

func TestStatusReflectsReady(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})

	if eng.Status().Status != "Initializing" {
		t.Fatalf("expected Initializing initially, got %q", eng.Status().Status)
	}

	_, _ = eng.Submit(Task{Type: TaskMarkReady})
	waitForStatus(t, eng, "Ready")

	if eng.Status().Status != "Ready" {
		t.Fatalf("expected Ready after mark-ready, got %q", eng.Status().Status)
	}
}

func TestStatusReflectsRunning(t *testing.T) {
	started := make(chan struct{})
	blocked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			close(started)
			<-blocked
			return nil
		},
	})

	_, _ = eng.Submit(Task{Type: TaskConfigPatch})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	if eng.Status().Status != "Running" {
		t.Fatalf("expected Running while task executes, got %q", eng.Status().Status)
	}

	close(blocked)
	waitForStatus(t, eng, "Initializing")
}

func TestGetResultReturnsCompleted(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	result := waitForResult(t, eng, id)

	if result.ID != id {
		t.Fatalf("expected ID %q, got %q", id, result.ID)
	}
	if result.Type != string(TaskConfigPatch) {
		t.Fatalf("expected type %q, got %q", TaskConfigPatch, result.Type)
	}
	if result.Error != "" {
		t.Fatalf("expected no error, got %q", result.Error)
	}
}

func TestGetResultReturnsFailure(t *testing.T) {
	handlerErr := errors.New("handler failed")
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return handlerErr },
	})

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	result := waitForResult(t, eng, id)

	if result.Error != handlerErr.Error() {
		t.Fatalf("expected error %q, got %q", handlerErr.Error(), result.Error)
	}
}

func TestRecentResultsBounded(t *testing.T) {
	calls := 0
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			calls++
			return nil
		},
	})

	for i := 0; i < 7; i++ {
		id, _ := eng.Submit(Task{Type: TaskConfigPatch})
		waitForResult(t, eng, id)
	}

	results := eng.RecentResults()
	if len(results) > maxResults {
		t.Fatalf("expected at most %d results, got %d", maxResults, len(results))
	}
}

func TestRemoveResult(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	waitForResult(t, eng, id)

	if !eng.RemoveResult(id) {
		t.Fatal("expected remove to return true")
	}
	if eng.RemoveResult(id) {
		t.Fatal("second remove should return false")
	}
	if eng.GetResult(id) != nil {
		t.Fatal("expected nil after removal")
	}
}

func TestSubmitScheduled(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id, err := eng.SubmitScheduled(Task{Type: TaskConfigPatch}, Schedule{Cron: "*/5 * * * *"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	result := eng.GetResult(id)
	if result == nil {
		t.Fatal("expected result for scheduled task")
	}
	if result.Schedule == nil || result.Schedule.Cron != "*/5 * * * *" {
		t.Fatalf("expected cron expression in schedule, got %+v", result.Schedule)
	}
	if result.NextRunAt == nil {
		t.Fatal("expected NextRunAt to be set")
	}
}

func TestSubmitScheduledEmptyCron(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	_, err := eng.SubmitScheduled(Task{Type: TaskConfigPatch}, Schedule{})
	if err == nil {
		t.Fatal("expected error for empty schedule")
	}
}

func TestRemoveScheduledTask(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id, _ := eng.SubmitScheduled(Task{Type: TaskConfigPatch}, Schedule{Cron: "*/5 * * * *"})

	if !eng.RemoveResult(id) {
		t.Fatal("expected remove to return true")
	}
	if eng.GetResult(id) != nil {
		t.Fatal("expected nil after removal")
	}
}

func TestEvalSchedulesFiresDueTasks(t *testing.T) {
	executed := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			executed <- struct{}{}
			return nil
		},
	})

	id, _ := eng.SubmitScheduled(Task{Type: TaskConfigPatch}, Schedule{Cron: "* * * * *"})

	eng.mu.Lock()
	past := time.Now().Add(-1 * time.Minute)
	eng.scheduled[id].NextRunAt = &past
	eng.mu.Unlock()

	eng.EvalSchedules()

	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected scheduled task to execute")
	}
}

func TestContextCancellationStopsEngine(t *testing.T) {
	var mu sync.Mutex
	executed := 0
	ctx, cancel := context.WithCancel(context.Background())
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			mu.Lock()
			executed++
			mu.Unlock()
			return nil
		},
	})

	_, _ = eng.Submit(Task{Type: TaskConfigPatch})
	time.Sleep(50 * time.Millisecond)
	cancel()

	mu.Lock()
	if executed != 1 {
		t.Fatalf("expected 1 execution, got %d", executed)
	}
	mu.Unlock()
}
