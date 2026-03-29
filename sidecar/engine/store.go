package engine

// ResultStore persists completed task results. Implementations must be
// safe for concurrent use by a single writer and multiple readers.
type ResultStore interface {
	// Save persists a TaskResult. If a result with the same ID already
	// exists, it is overwritten (upsert).
	Save(r *TaskResult) error

	// Get returns a result by ID, or (nil, nil) when not found.
	Get(id string) (*TaskResult, error)

	// List returns the most recent results, newest first, up to limit.
	List(limit int) ([]TaskResult, error)

	// Delete removes a result by ID. Returns true if it existed.
	Delete(id string) (bool, error)

	// Close releases underlying resources.
	Close() error
}
