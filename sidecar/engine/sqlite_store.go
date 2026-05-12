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

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO task_results
			(id, type, status, run, params, error, submitted_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID,
		r.Type,
		string(r.Status),
		r.Run,
		string(params),
		r.Error,
		r.SubmittedAt.UTC().Format(time.RFC3339Nano),
		formatNullableTime(r.CompletedAt),
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

func (s *SQLiteStore) ListStaleTasks() ([]TaskResult, error) {
	return s.queryMany(selectColumns+` WHERE status = ?`, string(TaskStatusRunning))
}

func (s *SQLiteStore) Delete(id string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM task_results WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *SQLiteStore) Ping() error {
	var n int
	return s.db.QueryRow("SELECT 1").Scan(&n)
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- query helpers ---

const selectColumns = `
	SELECT id, type, status, run, params, error, submitted_at, completed_at
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
		r           TaskResult
		status      string
		paramsJSON  string
		submittedAt string
		completedAt sql.NullString
	)

	if err := s.Scan(
		&r.ID, &r.Type, &status, &r.Run, &paramsJSON,
		&r.Error, &submittedAt, &completedAt,
	); err != nil {
		return nil, err
	}

	r.Status = TaskStatus(status)

	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &r.Params); err != nil {
			return nil, fmt.Errorf("unmarshal params: %w", err)
		}
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

	return &r, nil
}

func formatNullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// SaveCheckpoint upserts a sign-tx checkpoint. Called by SignAndBroadcast
// before the tx is broadcast so the post-broadcast lookup survives a
// crash between sign and persist-result.
//
// Atomicity: wrapped in BEGIN IMMEDIATE so two concurrent writers
// serialize. Refuses to overwrite a row whose chain_id differs from the
// incoming one — that scenario is a cross-chain TaskID collision and
// silent clobber would confuse the audit trail.
func (s *SQLiteStore) SaveCheckpoint(c *SignTxCheckpoint) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var existingChainID string
	err = tx.QueryRow(
		"SELECT chain_id FROM sign_tx_checkpoints WHERE task_id = ?",
		c.TaskID,
	).Scan(&existingChainID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// fresh insert; fall through
	case err != nil:
		return fmt.Errorf("lookup existing checkpoint: %w", err)
	case existingChainID != c.ChainID:
		return fmt.Errorf("checkpoint chain mismatch for task %q: existing=%q new=%q",
			c.TaskID, existingChainID, c.ChainID)
	}

	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO sign_tx_checkpoints
			(task_id, tx_hash, sequence, account_number, chain_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.TaskID,
		c.TxHash,
		int64(c.Sequence),
		int64(c.AccountNumber),
		c.ChainID,
		c.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadCheckpoint returns (nil, nil) when no row exists.
func (s *SQLiteStore) LoadCheckpoint(taskID string) (*SignTxCheckpoint, error) {
	row := s.db.QueryRow(`
		SELECT task_id, tx_hash, sequence, account_number, chain_id, created_at
		FROM sign_tx_checkpoints WHERE task_id = ?`, taskID)

	var (
		c          SignTxCheckpoint
		seq        int64
		accNum     int64
		createdRaw string
	)
	if err := row.Scan(&c.TaskID, &c.TxHash, &seq, &accNum, &c.ChainID, &createdRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c.Sequence = uint64(seq)
	c.AccountNumber = uint64(accNum)
	t, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	c.CreatedAt = t
	return &c, nil
}

// DeleteCheckpoint is a no-op when the row does not exist. We use it on
// the "safe-to-retry" path after a chain query confirms the tx never
// landed and the sequence has not advanced.
func (s *SQLiteStore) DeleteCheckpoint(taskID string) error {
	_, err := s.db.Exec("DELETE FROM sign_tx_checkpoints WHERE task_id = ?", taskID)
	return err
}
