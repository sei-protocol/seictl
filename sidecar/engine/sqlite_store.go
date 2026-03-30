package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore persists task results in a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and runs
// any pending schema migrations. The file is opened in WAL mode with
// pragmas tuned for a single-writer sidecar workload.
//
// The database file must reside on a local or block-device-backed
// filesystem (e.g. EBS, GCE PD, local SSD). WAL mode is unsafe on
// NFS-backed volumes (EFS, Azure Files, CephFS over NFS) because they
// do not support the POSIX byte-range locks that SQLite requires for
// the shared-memory (-shm) file.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	return openStore(dbPath)
}

// NewMemoryStore returns a SQLiteStore backed by an in-memory SQLite
// database (the ":memory:" DSN). The database exists only for the
// lifetime of the returned store — nothing is written to disk. Useful
// for tests and non-sidecar CLI commands.
func NewMemoryStore() (*SQLiteStore, error) {
	return openStore(":memory:")
}

func openStore(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Save(r *TaskResult) error {
	params, err := json.Marshal(r.Params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	var sched []byte
	if r.Schedule != nil {
		sched, err = json.Marshal(r.Schedule)
		if err != nil {
			return fmt.Errorf("marshal schedule: %w", err)
		}
	}

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO task_results
			(id, type, status, params, schedule, error, submitted_at, completed_at, next_run_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID,
		r.Type,
		string(r.Status),
		string(params),
		nullableBytes(sched),
		r.Error,
		r.SubmittedAt.UTC().Format(time.RFC3339Nano),
		formatNullableTime(r.CompletedAt),
		formatNullableTime(r.NextRunAt),
	)
	return err
}

func (s *SQLiteStore) Get(id string) (*TaskResult, error) {
	row := s.db.QueryRow(selectColumns+` WHERE id = ?`, id)
	r, err := scanTaskResult(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *SQLiteStore) List(limit int) ([]TaskResult, error) {
	return s.queryMany(selectColumns+` ORDER BY submitted_at DESC LIMIT ?`, limit)
}

func (s *SQLiteStore) ListScheduled(now time.Time) ([]TaskResult, error) {
	return s.queryMany(
		selectColumns+` WHERE schedule IS NOT NULL AND next_run_at <= ? ORDER BY next_run_at ASC`,
		now.UTC().Format(time.RFC3339Nano),
	)
}

func (s *SQLiteStore) ListStaleTasks() ([]TaskResult, error) {
	return s.queryMany(
		selectColumns+` WHERE status = ? AND schedule IS NULL`,
		string(TaskStatusRunning),
	)
}

func (s *SQLiteStore) Delete(id string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM task_results WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- query helpers ---

const selectColumns = `
	SELECT id, type, status, params, schedule, error,
	       submitted_at, completed_at, next_run_at
	FROM task_results`

// queryMany executes a query and scans all rows into TaskResults.
func (s *SQLiteStore) queryMany(query string, args ...any) ([]TaskResult, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TaskResult
	for rows.Next() {
		r, err := scanTaskResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *r)
	}
	return results, rows.Err()
}

// rowScanner abstracts *sql.Row and *sql.Rows for shared scan logic.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanTaskResult(s rowScanner) (*TaskResult, error) {
	var (
		r                      TaskResult
		status                 string
		paramsJSON             string
		schedJSON              sql.NullString
		submittedAt            string
		completedAt, nextRunAt sql.NullString
	)

	if err := s.Scan(
		&r.ID, &r.Type, &status, &paramsJSON, &schedJSON,
		&r.Error, &submittedAt, &completedAt, &nextRunAt,
	); err != nil {
		return nil, err
	}

	r.Status = TaskStatus(status)

	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &r.Params); err != nil {
			return nil, fmt.Errorf("unmarshal params: %w", err)
		}
	}

	if schedJSON.Valid {
		var sc ScheduleConfig
		if err := json.Unmarshal([]byte(schedJSON.String), &sc); err != nil {
			return nil, fmt.Errorf("unmarshal schedule: %w", err)
		}
		r.Schedule = &sc
	}

	t, err := time.Parse(time.RFC3339Nano, submittedAt)
	if err != nil {
		return nil, fmt.Errorf("parse submitted_at: %w", err)
	}
	r.SubmittedAt = t

	if completedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse completed_at: %w", err)
		}
		r.CompletedAt = &t
	}

	if nextRunAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, nextRunAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse next_run_at: %w", err)
		}
		r.NextRunAt = &t
	}

	return &r, nil
}

func formatNullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}
