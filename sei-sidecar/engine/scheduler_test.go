package engine

import (
	"sync"
	"testing"
	"time"
)

func TestSchedulerAddCron(t *testing.T) {
	s := NewScheduler()
	sched, err := s.Add(TaskUpdatePeers, nil, "*/5 * * * *")
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

func TestSchedulerAddRejectsEmpty(t *testing.T) {
	s := NewScheduler()
	_, err := s.Add(TaskUpdatePeers, nil, "")
	if err == nil {
		t.Fatal("expected error when cron is empty")
	}
}

func TestSchedulerAddRejectsInvalidCron(t *testing.T) {
	s := NewScheduler()
	_, err := s.Add(TaskUpdatePeers, nil, "not a cron")
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestSchedulerRemove(t *testing.T) {
	s := NewScheduler()
	sched, _ := s.Add(TaskUpdatePeers, nil, "*/5 * * * *")
	if !s.Remove(sched.ID) {
		t.Fatal("expected remove to return true")
	}
	if s.Remove(sched.ID) {
		t.Fatal("second remove should return false")
	}
}

func TestSchedulerList(t *testing.T) {
	s := NewScheduler()
	s.Add(TaskUpdatePeers, nil, "*/5 * * * *")
	s.Add(TaskSnapshotUpload, nil, "0 * * * *")

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(list))
	}
}

func TestSchedulerTickReturnsDueTasks(t *testing.T) {
	s := NewScheduler()
	sched, _ := s.Add(TaskUpdatePeers, map[string]any{"k": "v"}, "* * * * *")

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
	s.Add(TaskUpdatePeers, nil, "* * * * *")

	due := s.Tick(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(due) != 0 {
		t.Fatalf("expected 0 due tasks, got %d", len(due))
	}
}

func TestSchedulerConfirmRunAdvancesCron(t *testing.T) {
	s := NewScheduler()
	sched, _ := s.Add(TaskUpdatePeers, nil, "*/5 * * * *")

	originalNext := *sched.NextRunAt
	now := originalNext.Add(1 * time.Second)
	s.ConfirmRun(sched.ID, now)

	updated := s.List()
	var found *Schedule
	for i := range updated {
		if updated[i].ID == sched.ID {
			found = &updated[i]
			break
		}
	}
	if found == nil {
		t.Fatal("schedule not found")
	}
	if found.LastRunAt == nil || !found.LastRunAt.Equal(now) {
		t.Fatal("expected LastRunAt to be updated")
	}
	if found.NextRunAt == nil || !found.NextRunAt.After(now) {
		t.Fatal("expected NextRunAt to advance past now")
	}
	if !found.Enabled {
		t.Fatal("cron schedule should remain enabled")
	}
}

func TestSchedulerConcurrentAccess(t *testing.T) {
	s := NewScheduler()
	var wg sync.WaitGroup

	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sched, err := s.Add(TaskUpdatePeers, nil, "* * * * *")
			if err != nil {
				return
			}
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
