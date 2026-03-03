package engine

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Schedule defines a recurring cron-based task trigger.
type Schedule struct {
	ID        string         `json:"id"`
	TaskType  TaskType       `json:"taskType"`
	Params    map[string]any `json:"params,omitempty"`
	Cron      string         `json:"cron"`
	LastRunAt *time.Time     `json:"lastRunAt,omitempty"`
	NextRunAt *time.Time     `json:"nextRunAt,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// DueTask is returned by Tick for schedules that are ready to fire.
type DueTask struct {
	ScheduleID string
	TaskType   TaskType
	Params     map[string]any
}

// Scheduler manages cron-based schedule definitions and evaluates due tasks.
type Scheduler struct {
	mu        sync.Mutex
	schedules map[string]*Schedule
}

// NewScheduler creates an empty scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{
		schedules: make(map[string]*Schedule),
	}
}

// Add creates a new cron schedule.
func (s *Scheduler) Add(taskType TaskType, params map[string]any, cronExpr string) (*Schedule, error) {
	if cronExpr == "" {
		return nil, fmt.Errorf("cron expression is required")
	}
	if err := validateCron(cronExpr); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	next, _ := nextCronTime(cronExpr, now) // already validated

	sched := &Schedule{
		ID:        uuid.New().String(),
		TaskType:  taskType,
		Params:    params,
		Cron:      cronExpr,
		NextRunAt: &next,
		CreatedAt: now,
	}

	s.schedules[sched.ID] = sched
	cp := *sched
	return &cp, nil
}

// Remove deletes a schedule. Returns true if found.
func (s *Scheduler) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.schedules[id]; !ok {
		return false
	}
	delete(s.schedules, id)
	return true
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
// tasks without mutating state — call ConfirmRun after successful submission.
func (s *Scheduler) Tick(now time.Time) []DueTask {
	s.mu.Lock()
	defer s.mu.Unlock()

	var due []DueTask
	for _, sched := range s.schedules {
		if sched.NextRunAt == nil {
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

// ConfirmRun advances the schedule's NextRunAt after a successful submission.
func (s *Scheduler) ConfirmRun(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sched, ok := s.schedules[id]
	if !ok {
		return
	}

	sched.LastRunAt = &now
	if next, err := nextCronTime(sched.Cron, now); err == nil {
		sched.NextRunAt = &next
	}
}
