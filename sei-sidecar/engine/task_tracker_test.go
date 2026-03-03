package engine

import (
	"sync"
	"testing"
)

func TestTrackerCreateAndGet(t *testing.T) {
	tr := NewTaskTracker()
	id := tr.Create(TaskConfigPatch, map[string]any{"key": "val"})

	task := tr.Get(id)
	if task == nil {
		t.Fatal("expected task, got nil")
	}
	if task.State != TaskStatePending {
		t.Fatalf("expected pending, got %s", task.State)
	}
	if task.Type != TaskConfigPatch {
		t.Fatalf("expected config-patch, got %s", task.Type)
	}
}

func TestTrackerLifecycleTransitions(t *testing.T) {
	tr := NewTaskTracker()
	id := tr.Create(TaskMarkReady, nil)

	tr.MarkRunning(id)
	task := tr.Get(id)
	if task.State != TaskStateRunning {
		t.Fatalf("expected running, got %s", task.State)
	}
	if task.StartedAt == nil {
		t.Fatal("expected StartedAt to be set")
	}

	tr.MarkCompleted(id)
	task = tr.Get(id)
	if task.State != TaskStateCompleted {
		t.Fatalf("expected completed, got %s", task.State)
	}
	if task.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set")
	}
}

func TestTrackerMarkFailed(t *testing.T) {
	tr := NewTaskTracker()
	id := tr.Create(TaskConfigPatch, nil)
	tr.MarkRunning(id)
	tr.MarkFailed(id, "boom")

	task := tr.Get(id)
	if task.State != TaskStateFailed {
		t.Fatalf("expected failed, got %s", task.State)
	}
	if task.Error != "boom" {
		t.Fatalf("expected error 'boom', got %q", task.Error)
	}
	if task.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set on failure")
	}
}

func TestTrackerListNewestFirst(t *testing.T) {
	tr := NewTaskTracker()
	id1 := tr.Create(TaskConfigPatch, nil)
	id2 := tr.Create(TaskMarkReady, nil)
	id3 := tr.Create(TaskUpdatePeers, nil)

	list := tr.List("", 0)
	if len(list) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(list))
	}
	if list[0].ID != id3 || list[1].ID != id2 || list[2].ID != id1 {
		t.Fatal("expected newest-first ordering")
	}
}

func TestTrackerListFilterByState(t *testing.T) {
	tr := NewTaskTracker()
	tr.Create(TaskConfigPatch, nil)
	id2 := tr.Create(TaskMarkReady, nil)
	tr.MarkRunning(id2)
	tr.Create(TaskUpdatePeers, nil)

	running := tr.List(TaskStateRunning, 0)
	if len(running) != 1 {
		t.Fatalf("expected 1 running task, got %d", len(running))
	}
	if running[0].ID != id2 {
		t.Fatal("wrong task returned by filter")
	}
}

func TestTrackerListWithLimit(t *testing.T) {
	tr := NewTaskTracker()
	for range 10 {
		tr.Create(TaskConfigPatch, nil)
	}

	list := tr.List("", 3)
	if len(list) != 3 {
		t.Fatalf("expected 3 tasks with limit, got %d", len(list))
	}
}

func TestTrackerEviction(t *testing.T) {
	tr := NewTaskTracker()
	var firstID TaskID
	for i := range maxHistory + 10 {
		id := tr.Create(TaskConfigPatch, nil)
		if i == 0 {
			firstID = id
		}
	}

	list := tr.List("", 0)
	if len(list) != maxHistory {
		t.Fatalf("expected %d tasks after eviction, got %d", maxHistory, len(list))
	}

	if tr.Get(firstID) != nil {
		t.Fatal("expected oldest task to be evicted")
	}
}

func TestTrackerGetReturnsNilForUnknown(t *testing.T) {
	tr := NewTaskTracker()
	if tr.Get("nonexistent") != nil {
		t.Fatal("expected nil for unknown ID")
	}
}

func TestTrackerGetReturnsCopy(t *testing.T) {
	tr := NewTaskTracker()
	id := tr.Create(TaskConfigPatch, nil)

	task1 := tr.Get(id)
	task1.State = TaskStateFailed

	task2 := tr.Get(id)
	if task2.State != TaskStatePending {
		t.Fatal("Get should return copies — mutation should not affect tracker")
	}
}

func TestTrackerConcurrentAccess(t *testing.T) {
	tr := NewTaskTracker()
	var wg sync.WaitGroup

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := tr.Create(TaskConfigPatch, nil)
			tr.MarkRunning(id)
			tr.MarkCompleted(id)
			tr.Get(id)
			tr.List("", 10)
		}()
	}
	wg.Wait()

	list := tr.List("", 0)
	if len(list) != 50 {
		t.Fatalf("expected 50 tasks, got %d", len(list))
	}
}

func TestTrackerActiveCount(t *testing.T) {
	tr := NewTaskTracker()
	id1 := tr.Create(TaskConfigPatch, nil)
	id2 := tr.Create(TaskMarkReady, nil)
	tr.Create(TaskUpdatePeers, nil)

	tr.MarkRunning(id1)
	tr.MarkRunning(id2)

	if tr.ActiveCount() != 2 {
		t.Fatalf("expected 2 active, got %d", tr.ActiveCount())
	}

	tr.MarkCompleted(id1)
	if tr.ActiveCount() != 1 {
		t.Fatalf("expected 1 active, got %d", tr.ActiveCount())
	}
}
