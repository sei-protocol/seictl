package engine

import (
	"sync"
	"testing"
	"time"
)

func TestSchedulerAddCron(t *testing.T) {
	s := NewScheduler()
	sched, err := s.Add(TaskUpdatePeers, nil, "*/5 * * * *", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sched.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if !sched.Enabled {
		t.Fatal("expected enabled")
	}
	if sched.NextRunAt == nil {
		t.Fatal("expected NextRunAt to be set")
	}
	if sched.Cron != "*/5 * * * *" {
		t.Fatalf("expected cron expression, got %q", sched.Cron)
	}
}

func TestSchedulerAddRunAt(t *testing.T) {
	s := NewScheduler()
	runAt := time.Now().Add(1 * time.Hour)
	sched, err := s.Add(TaskSnapshotUpload, nil, "", &runAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sched.RunAt == nil || !sched.RunAt.Equal(runAt) {
		t.Fatal("expected RunAt to match")
	}
	if sched.NextRunAt == nil || !sched.NextRunAt.Equal(runAt) {
		t.Fatal("expected NextRunAt to equal RunAt")
	}
}

func TestSchedulerAddRejectsBothCronAndRunAt(t *testing.T) {
	s := NewScheduler()
	runAt := time.Now().Add(1 * time.Hour)
	_, err := s.Add(TaskUpdatePeers, nil, "*/5 * * * *", &runAt)
	if err == nil {
		t.Fatal("expected error when both cron and runAt provided")
	}
}

func TestSchedulerAddRejectsNeither(t *testing.T) {
	s := NewScheduler()
	_, err := s.Add(TaskUpdatePeers, nil, "", nil)
	if err == nil {
		t.Fatal("expected error when neither cron nor runAt provided")
	}
}

func TestSchedulerAddRejectsInvalidCron(t *testing.T) {
	s := NewScheduler()
	_, err := s.Add(TaskUpdatePeers, nil, "not a cron", nil)
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestSchedulerRemove(t *testing.T) {
	s := NewScheduler()
	sched, _ := s.Add(TaskUpdatePeers, nil, "*/5 * * * *", nil)
	if !s.Remove(sched.ID) {
		t.Fatal("expected remove to return true")
	}
	if s.Remove(sched.ID) {
		t.Fatal("second remove should return false")
	}
	if s.Get(sched.ID) != nil {
		t.Fatal("expected nil after remove")
	}
}

func TestSchedulerGetReturnsCopy(t *testing.T) {
	s := NewScheduler()
	sched, _ := s.Add(TaskUpdatePeers, nil, "*/5 * * * *", nil)

	got := s.Get(sched.ID)
	got.Enabled = false

	got2 := s.Get(sched.ID)
	if !got2.Enabled {
		t.Fatal("Get should return copies — mutation should not affect scheduler")
	}
}

func TestSchedulerList(t *testing.T) {
	s := NewScheduler()
	s.Add(TaskUpdatePeers, nil, "*/5 * * * *", nil)
	s.Add(TaskSnapshotUpload, nil, "0 * * * *", nil)

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(list))
	}
}

func TestSchedulerTickReturnsDueTasks(t *testing.T) {
	s := NewScheduler()
	// Create a cron schedule that should be due.
	sched, _ := s.Add(TaskUpdatePeers, map[string]any{"k": "v"}, "* * * * *", nil)

	// Tick at a time well past NextRunAt.
	due := s.Tick(sched.NextRunAt.Add(1 * time.Second))
	if len(due) != 1 {
		t.Fatalf("expected 1 due task, got %d", len(due))
	}
	if due[0].ScheduleID != sched.ID {
		t.Fatal("wrong schedule ID")
	}
	if due[0].TaskType != TaskUpdatePeers {
		t.Fatalf("wrong task type: %s", due[0].TaskType)
	}
}

func TestSchedulerTickSkipsFuture(t *testing.T) {
	s := NewScheduler()
	s.Add(TaskUpdatePeers, nil, "* * * * *", nil)

	// Tick at epoch (well before any NextRunAt).
	due := s.Tick(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(due) != 0 {
		t.Fatalf("expected 0 due tasks, got %d", len(due))
	}
}

func TestSchedulerTickSkipsDisabled(t *testing.T) {
	s := NewScheduler()
	runAt := time.Now().Add(-1 * time.Hour) // already past
	sched, _ := s.Add(TaskUpdatePeers, nil, "", &runAt)

	// Confirm the one-shot to disable it.
	s.ConfirmRun(sched.ID, time.Now())

	due := s.Tick(time.Now().Add(1 * time.Hour))
	if len(due) != 0 {
		t.Fatalf("expected 0 due tasks after disable, got %d", len(due))
	}
}

func TestSchedulerConfirmRunAdvancesCron(t *testing.T) {
	s := NewScheduler()
	sched, _ := s.Add(TaskUpdatePeers, nil, "*/5 * * * *", nil)

	originalNext := *sched.NextRunAt
	now := originalNext.Add(1 * time.Second)
	s.ConfirmRun(sched.ID, now)

	updated := s.Get(sched.ID)
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(now) {
		t.Fatal("expected LastRunAt to be updated")
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(now) {
		t.Fatal("expected NextRunAt to advance past now")
	}
	if updated.NextRunAt.Equal(originalNext) {
		t.Fatal("expected NextRunAt to change")
	}
	if !updated.Enabled {
		t.Fatal("cron schedule should remain enabled")
	}
}

func TestSchedulerConfirmRunDisablesOneShot(t *testing.T) {
	s := NewScheduler()
	runAt := time.Now().Add(1 * time.Hour)
	sched, _ := s.Add(TaskSnapshotUpload, nil, "", &runAt)

	s.ConfirmRun(sched.ID, time.Now())

	updated := s.Get(sched.ID)
	if updated.Enabled {
		t.Fatal("one-shot should be disabled after confirm")
	}
	if updated.NextRunAt != nil {
		t.Fatal("one-shot NextRunAt should be nil after confirm")
	}
}

func TestSchedulerConcurrentAccess(t *testing.T) {
	s := NewScheduler()
	var wg sync.WaitGroup

	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sched, err := s.Add(TaskUpdatePeers, nil, "* * * * *", nil)
			if err != nil {
				return
			}
			s.Get(sched.ID)
			s.List()
			s.Tick(time.Now().Add(1 * time.Hour))
			s.ConfirmRun(sched.ID, time.Now())
		}()
	}
	wg.Wait()

	list := s.List()
	if len(list) != 20 {
		t.Fatalf("expected 20 schedules, got %d", len(list))
	}
}
