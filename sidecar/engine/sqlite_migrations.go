package engine

import "database/sql"

func migrate(db *sql.DB) error {
	var version int
	_ = db.QueryRow("PRAGMA user_version").Scan(&version)

	if version < 1 {
		if _, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS task_results (
				id           TEXT PRIMARY KEY,
				type         TEXT    NOT NULL,
				status       TEXT    NOT NULL,
				params       TEXT,
				schedule     TEXT,
				error        TEXT    NOT NULL DEFAULT '',
				submitted_at TEXT    NOT NULL,
				completed_at TEXT,
				next_run_at  TEXT
			);
			CREATE INDEX IF NOT EXISTS idx_task_results_submitted_at
				ON task_results (submitted_at DESC);
			PRAGMA user_version = 1;
		`); err != nil {
			return err
		}
	}

	return nil
}
