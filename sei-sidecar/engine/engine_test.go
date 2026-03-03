package engine

import (
	"context"
	"testing"
	"time"
)

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

func TestSubmitAccepts(t *testing.T) {
	eng := NewEngine(map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})

	if err := eng.Submit(Task{Type: TaskMarkReady}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestSubmitRejectsUnknownType(t *testing.T) {
	eng := NewEngine(map[TaskType]TaskHandler{})

	if err := eng.Submit(Task{Type: "nonexistent"}); err == nil {
		t.Fatal("expected error for unknown task type")
	}
}

func TestMarkReadySetsHealthz(t *testing.T) {
	eng := NewEngine(map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})

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
	eng := NewEngine(map[TaskType]TaskHandler{
		TaskMarkReady:   func(_ context.Context, _ map[string]any) error { return nil },
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error { return context.DeadlineExceeded },
	})

	eng.Submit(Task{Type: TaskMarkReady})
	waitForHealthz(t, eng)

	// Submit a failing task — healthz should remain true.
	eng.Submit(Task{Type: TaskUpdatePeers})
	time.Sleep(100 * time.Millisecond)

	if !eng.Healthz() {
		t.Fatal("healthz should remain true after runtime failure")
	}
}

func TestStatusReflectsReady(t *testing.T) {
	eng := NewEngine(map[TaskType]TaskHandler{
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})

	status := eng.Status()
	if status.Ready {
		t.Fatal("expected not ready initially")
	}

	eng.Submit(Task{Type: TaskMarkReady})
	waitForHealthz(t, eng)

	status = eng.Status()
	if !status.Ready {
		t.Fatal("expected ready after mark-ready")
	}
}

func TestEvalSchedulesSubmitsDueTasks(t *testing.T) {
	executed := make(chan struct{}, 1)
	eng := NewEngine(map[TaskType]TaskHandler{
		TaskUpdatePeers: func(_ context.Context, _ map[string]any) error {
			executed <- struct{}{}
			return nil
		},
	})

	sched, err := eng.AddSchedule(TaskUpdatePeers, nil, "* * * * *")
	if err != nil {
		t.Fatalf("add schedule failed: %v", err)
	}

	// Set NextRunAt to the past so it fires immediately.
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
