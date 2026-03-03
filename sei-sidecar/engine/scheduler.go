package engine

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ScheduleID uniquely identifies a schedule.
type ScheduleID = string

// Schedule defines a recurring (cron) or one-shot (runAt) task trigger.
type Schedule struct {
	ID        ScheduleID     `json:"id"`
	TaskType  TaskType       `json:"taskType"`
	Params    map[string]any `json:"params,omitempty"`
	Cron      string         `json:"cron,omitempty"`
	RunAt     *time.Time     `json:"runAt,omitempty"`
	Enabled   bool           `json:"enabled"`
	LastRunAt *time.Time     `json:"lastRunAt,omitempty"`
	NextRunAt *time.Time     `json:"nextRunAt,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// DueTask is returned by Tick for schedules that are ready to fire.
type DueTask struct {
	ScheduleID ScheduleID
	TaskType   TaskType
	Params     map[string]any
}

// Scheduler manages time-based schedule definitions and evaluates due tasks.
type Scheduler struct {
	mu        sync.Mutex
	schedules map[ScheduleID]*Schedule
}

// NewScheduler creates an empty scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{
		schedules: make(map[ScheduleID]*Schedule),
	}
}

// Add creates a new schedule. Exactly one of cron or runAt must be provided.
func (s *Scheduler) Add(taskType TaskType, params map[string]any, cronExpr string, runAt *time.Time) (*Schedule, error) {
	hasCron := cronExpr != ""
	hasRunAt := runAt != nil

	if hasCron == hasRunAt {
		return nil, fmt.Errorf("exactly one of cron or runAt must be provided")
	}

	if hasCron {
		if err := validateCron(cronExpr); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	sched := &Schedule{
		ID:        uuid.New().String(),
		TaskType:  taskType,
		Params:    params,
		Cron:      cronExpr,
		Enabled:   true,
		CreatedAt: now,
	}

	if hasCron {
		next, _ := nextCronTime(cronExpr, now) // already validated
		sched.NextRunAt = &next
	} else {
		sched.RunAt = runAt
		sched.NextRunAt = runAt
	}

	cp := *sched
	s.schedules[sched.ID] = sched
	return &cp, nil
}

// Remove deletes a schedule. Returns true if found.
func (s *Scheduler) Remove(id ScheduleID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.schedules[id]; !ok {
		return false
	}
	delete(s.schedules, id)
	return true
}

// Get returns a copy of the schedule, or nil if not found.
func (s *Scheduler) Get(id ScheduleID) *Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()

	sched, ok := s.schedules[id]
	if !ok {
		return nil
	}
	cp := *sched
	return &cp
}

// List returns copies of all schedules.
func (s *Scheduler) List() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Schedule, 0, len(s.schedules))
	for _, sched := range s.schedules {
		cp := *sched
		result = append(result, cp)
	}
	return result
}

// Tick evaluates which schedules are due at the given time. Returns the due
// tasks without mutating schedule state — call ConfirmRun after successful
// submission.
func (s *Scheduler) Tick(now time.Time) []DueTask {
	s.mu.Lock()
	defer s.mu.Unlock()

	var due []DueTask
	for _, sched := range s.schedules {
		if !sched.Enabled || sched.NextRunAt == nil {
			continue
		}
		if now.Before(*sched.NextRunAt) {
			continue
		}
		due = append(due, DueTask{
			ScheduleID: sched.ID,
			TaskType:   sched.TaskType,
			Params:     sched.Params,
		})
	}
	return due
}

// ConfirmRun updates the schedule after a successful task submission. For cron
// schedules it advances NextRunAt; for one-shot schedules it disables them.
func (s *Scheduler) ConfirmRun(id ScheduleID, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sched, ok := s.schedules[id]
	if !ok {
		return
	}

	sched.LastRunAt = &now

	if sched.Cron != "" {
		next, err := nextCronTime(sched.Cron, now)
		if err == nil {
			sched.NextRunAt = &next
		}
	} else {
		// One-shot: disable after firing.
		sched.Enabled = false
		sched.NextRunAt = nil
	}
}
