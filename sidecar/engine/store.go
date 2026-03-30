package engine

import "time"

// ResultStore persists task results across all lifecycle states.
// Implementations must be safe for concurrent use.
type ResultStore interface {
	// Save persists a TaskResult. If a result with the same ID already
	// exists, it is overwritten (upsert).
	Save(r *TaskResult) error

	// Get returns a result by ID, or (nil, nil) when not found.
	Get(id string) (*TaskResult, error)

	// List returns the most recent results, newest first, up to limit.
	List(limit int) ([]TaskResult, error)

	// ListScheduled returns scheduled tasks that are due for execution
	// (next_run_at <= now and schedule IS NOT NULL).
	ListScheduled(now time.Time) ([]TaskResult, error)

	// ListStaleTasks returns one-shot tasks left in "running" state from
	// a previous process that exited without completing them.
	ListStaleTasks() ([]TaskResult, error)

	// Delete removes a result by ID. Returns true if it existed.
	Delete(id string) (bool, error)

	// Close releases underlying resources.
	Close() error
}
