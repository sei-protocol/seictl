package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestEngine(t *testing.T, handlers map[TaskType]TaskHandler) *Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewEngine(ctx, handlers, store)
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

// --- Submit tests ---

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

func TestSubmitCallerProvidedID(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	const customID = "aaaaaaaa-1111-2222-3333-444444444444"
	id, err := eng.Submit(Task{ID: customID, Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if id != customID {
		t.Fatalf("expected ID %q, got %q", customID, id)
	}

	result := waitForResult(t, eng, id)
	if result.ID != customID {
		t.Fatalf("result ID = %q, want %q", result.ID, customID)
	}
}

func TestSubmitInvalidIDReturnsTypedError(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	_, err := eng.Submit(Task{ID: "not-a-uuid", Type: TaskConfigPatch})
	if err == nil {
		t.Fatal("expected error for non-UUID ID")
	}
	if !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("expected ErrInvalidTaskID, got: %v", err)
	}
}

func TestSubmitDedupExistingActive(t *testing.T) {
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
	}, newTestStore(t))

	const dedupID = "bbbbbbbb-1111-2222-3333-444444444444"
	id1, err := eng.Submit(Task{ID: dedupID, Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	id2, err := eng.Submit(Task{ID: dedupID, Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("dedup should return same ID: got %q and %q", id1, id2)
	}

	close(blocked)
}

func TestSubmitDedupExistingCompleted(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	const dedupID = "cccccccc-1111-2222-3333-444444444444"
	id1, _ := eng.Submit(Task{ID: dedupID, Type: TaskConfigPatch})
	waitForResult(t, eng, id1)

	id2, err := eng.Submit(Task{ID: dedupID, Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("dedup submit: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("dedup should return same ID: got %q and %q", id1, id2)
	}
}

func TestSubmitNoIDGeneratesUUID(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id1, _ := eng.Submit(Task{Type: TaskConfigPatch})
	id2, _ := eng.Submit(Task{Type: TaskConfigPatch})

	if id1 == "" || id2 == "" {
		t.Fatal("expected non-empty generated IDs")
	}
	if id1 == id2 {
		t.Fatal("generated IDs should be unique")
	}
}

func TestSubmitConcurrent(t *testing.T) {
	var callCount atomic.Int32
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			callCount.Add(1)
			time.Sleep(20 * time.Millisecond)
			return nil
		},
	})

	id1, err := eng.Submit(Task{Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	id2, err := eng.Submit(Task{Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}

	waitForResult(t, eng, id1)
	waitForResult(t, eng, id2)

	if c := callCount.Load(); c != 2 {
		t.Fatalf("expected 2 concurrent executions, got %d", c)
	}
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

	id2, _ := eng.Submit(Task{Type: TaskConfigPatch})
	waitForResult(t, eng, id2)

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

// --- Result tests ---

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
	if result.Status != TaskStatusCompleted {
		t.Fatalf("expected status %q, got %q", TaskStatusCompleted, result.Status)
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

	if result.Status != TaskStatusFailed {
		t.Fatalf("expected status %q, got %q", TaskStatusFailed, result.Status)
	}
	if result.Error != handlerErr.Error() {
		t.Fatalf("expected error %q, got %q", handlerErr.Error(), result.Error)
	}
}

func TestGetResultReturnsRunning(t *testing.T) {
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
	}, newTestStore(t))

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	result := eng.GetResult(id)
	if result == nil {
		t.Fatal("expected non-nil result for active task")
	}
	if result.Status != TaskStatusRunning {
		t.Fatalf("expected status %q, got %q", TaskStatusRunning, result.Status)
	}
	if result.CompletedAt != nil {
		t.Fatal("expected CompletedAt to be nil for running task")
	}

	close(blocked)
	completed := waitForResult(t, eng, id)
	if completed.Status != TaskStatusCompleted {
		t.Fatalf("expected status %q after completion, got %q", TaskStatusCompleted, completed.Status)
	}
}

func TestRecentResultsReturnsAll(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	var ids []string
	for i := 0; i < 7; i++ {
		id, _ := eng.Submit(Task{Type: TaskConfigPatch})
		ids = append(ids, id)
	}
	for _, id := range ids {
		waitForResult(t, eng, id)
	}

	results := eng.RecentResults()
	if len(results) != 7 {
		t.Fatalf("expected 7 results, got %d", len(results))
	}
}

func TestRecentResultsIncludesActive(t *testing.T) {
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
	}, newTestStore(t))

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	results := eng.RecentResults()
	if len(results) != 1 {
		t.Fatalf("expected 1 result (active), got %d", len(results))
	}
	if results[0].ID != id {
		t.Fatalf("expected ID %q, got %q", id, results[0].ID)
	}
	if results[0].Status != TaskStatusRunning {
		t.Fatalf("expected status %q, got %q", TaskStatusRunning, results[0].Status)
	}

	close(blocked)
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

func TestRemoveActiveTaskCancels(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(ctx context.Context, _ map[string]any) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}, newTestStore(t))

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task to start")
	}

	if !eng.RemoveResult(id) {
		t.Fatal("expected remove to return true")
	}
	if eng.GetResult(id) != nil {
		t.Fatal("expected nil after removal")
	}
}

// --- Context cancellation ---

func TestContextCancellationStopsEngine(t *testing.T) {
	var mu sync.Mutex
	executed := 0
	ctx, cancel := context.WithCancel(context.Background())
	store, _ := NewMemoryStore()
	t.Cleanup(func() { store.Close() })
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			mu.Lock()
			executed++
			mu.Unlock()
			return nil
		},
	}, store)

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	waitForResult(t, eng, id)
	cancel()

	mu.Lock()
	if executed != 1 {
		t.Fatalf("expected 1 execution, got %d", executed)
	}
	mu.Unlock()
}

// --- Long-running task tests ---

func TestLongRunningTaskCompletion(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	result := waitForResult(t, eng, id)

	if result.Status != TaskStatusCompleted {
		t.Fatalf("expected status %q, got %q", TaskStatusCompleted, result.Status)
	}
}

func TestLongRunningTaskFailure(t *testing.T) {
	handlerErr := errors.New("fatal crash")
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return handlerErr },
	})

	id, _ := eng.Submit(Task{Type: TaskConfigPatch})
	result := waitForResult(t, eng, id)

	if result.Status != TaskStatusFailed {
		t.Fatalf("expected status %q, got %q", TaskStatusFailed, result.Status)
	}
	if result.Error != handlerErr.Error() {
		t.Fatalf("expected error %q, got %q", handlerErr.Error(), result.Error)
	}
}

func TestLongRunningTaskDoesNotBlockOthers(t *testing.T) {
	bgStarted := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := NewEngine(ctx, map[TaskType]TaskHandler{
		TaskConfigPatch: func(ctx context.Context, _ map[string]any) error {
			close(bgStarted)
			<-ctx.Done()
			return ctx.Err()
		},
		TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	}, newTestStore(t))

	_, _ = eng.Submit(Task{Type: TaskConfigPatch})

	select {
	case <-bgStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for long-running task to start")
	}

	id, err := eng.Submit(Task{Type: TaskMarkReady})
	if err != nil {
		t.Fatalf("second submit should not be blocked: %v", err)
	}
	waitForResult(t, eng, id)
}

func TestTaskErrorProducesRichErrorString(t *testing.T) {
	eng := newTestEngine(t, map[TaskType]TaskHandler{
		TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			return &TaskError{
				Task:      "config-patch",
				Operation: "S3",
				Message:   "bucket not found",
				Hint:      "check SEI_SNAPSHOT_BUCKET",
			}
		},
	})

	id, err := eng.Submit(Task{Type: TaskConfigPatch})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	r := waitForResult(t, eng, id)
	if r.Status != TaskStatusFailed {
		t.Fatalf("expected Failed, got %s", r.Status)
	}
	if !strings.Contains(r.Error, "config-patch") {
		t.Errorf("error should contain task name, got: %s", r.Error)
	}
	if !strings.Contains(r.Error, "bucket not found") {
		t.Errorf("error should contain message, got: %s", r.Error)
	}
	if !strings.Contains(r.Error, "hint:") {
		t.Errorf("error should contain hint, got: %s", r.Error)
	}
}
