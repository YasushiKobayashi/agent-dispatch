// Package store persists which events reviewd has already acted on, and the
// history of Claude Code runs it started.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary stays portable

	"github.com/YasushiKobayashi/agent-dispatch/internal/model"
)

const schema = `
CREATE TABLE IF NOT EXISTS processed (
    repo         TEXT    NOT NULL,
    pr_number    INTEGER NOT NULL,
    kind         TEXT    NOT NULL,
    cursor       TEXT    NOT NULL,
    processed_at TEXT    NOT NULL,
    PRIMARY KEY (repo, pr_number, kind, cursor)
);

CREATE TABLE IF NOT EXISTS runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo        TEXT    NOT NULL,
    pr_number   INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    cursor      TEXT    NOT NULL,
    rule        TEXT    NOT NULL,
    status      TEXT    NOT NULL,
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    exit_code   INTEGER,
    output_dir  TEXT,
    error       TEXT
);

CREATE INDEX IF NOT EXISTS idx_runs_started_at ON runs(started_at DESC);
`

// Store is reviewd's SQLite-backed state. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// WAL keeps a long-running Claude Code run from blocking the next poll's
	// reads, and busy_timeout absorbs the brief writer overlap when it does.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema to %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// IsProcessed reports whether this exact event has already been acted on.
func (s *Store) IsProcessed(ctx context.Context, ev model.Event) (bool, error) {
	const q = `SELECT 1 FROM processed WHERE repo = ? AND pr_number = ? AND kind = ? AND cursor = ?`

	var one int
	err := s.db.QueryRowContext(ctx, q, ev.Repo, ev.PRNumber, string(ev.Kind), ev.Cursor).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("query processed for %s: %w", ev.Key(), err)
	default:
		return true, nil
	}
}

// MarkProcessed records that the event has been acted on. It is idempotent.
//
// Callers must mark an event processed once they decide to act on it, not once
// the run succeeds: retrying a failing run on every poll would burn tokens
// indefinitely. Deliberate re-runs go through `reviewd run`.
func (s *Store) MarkProcessed(ctx context.Context, ev model.Event) error {
	const q = `
		INSERT INTO processed (repo, pr_number, kind, cursor, processed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (repo, pr_number, kind, cursor) DO NOTHING`

	at := ev.DetectedAt
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := s.db.ExecContext(ctx, q, ev.Repo, ev.PRNumber, string(ev.Kind), ev.Cursor, formatTime(at)); err != nil {
		return fmt.Errorf("mark %s processed: %w", ev.Key(), err)
	}
	return nil
}

// StartRun inserts a run row in the "running" state and returns its id.
func (s *Store) StartRun(ctx context.Context, ev model.Event, rule, outputDir string) (int64, error) {
	const q = `
		INSERT INTO runs (repo, pr_number, kind, cursor, rule, status, started_at, output_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	res, err := s.db.ExecContext(ctx, q,
		ev.Repo, ev.PRNumber, string(ev.Kind), ev.Cursor, rule,
		string(model.RunRunning), formatTime(time.Now()), outputDir)
	if err != nil {
		return 0, fmt.Errorf("start run for %s: %w", ev.Key(), err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start run for %s: %w", ev.Key(), err)
	}
	return id, nil
}

// FinishRun closes out a run row. exitCode may be nil when the process never
// started, errMsg is empty on success, and outputDir is only known once the
// runner has created it, which is why it is set here rather than at StartRun.
func (s *Store) FinishRun(ctx context.Context, id int64, status model.RunStatus, exitCode *int, outputDir, errMsg string) error {
	const q = `
		UPDATE runs
		SET status = ?, finished_at = ?, exit_code = ?, error = ?,
		    output_dir = CASE WHEN ? = '' THEN output_dir ELSE ? END
		WHERE id = ?`

	var code any
	if exitCode != nil {
		code = *exitCode
	}
	if _, err := s.db.ExecContext(ctx, q, string(status), formatTime(time.Now()), code, errMsg, outputDir, outputDir, id); err != nil {
		return fmt.Errorf("finish run %d: %w", id, err)
	}
	return nil
}

// RecentRuns returns the most recently started runs, newest first.
func (s *Store) RecentRuns(ctx context.Context, limit int) ([]model.Run, error) {
	if limit <= 0 {
		limit = 20
	}
	const q = `
		SELECT id, repo, pr_number, kind, cursor, rule, status, started_at, finished_at, exit_code, output_dir, error
		FROM runs ORDER BY started_at DESC, id DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []model.Run
	for rows.Next() {
		var (
			r          model.Run
			kind       string
			status     string
			startedAt  string
			finishedAt sql.NullString
			exitCode   sql.NullInt64
			outputDir  sql.NullString
			errMsg     sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.Repo, &r.PRNumber, &kind, &r.Cursor, &r.Rule,
			&status, &startedAt, &finishedAt, &exitCode, &outputDir, &errMsg); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		r.Kind = model.Kind(kind)
		r.Status = model.RunStatus(status)
		r.StartedAt = parseTime(startedAt)
		if finishedAt.Valid {
			t := parseTime(finishedAt.String)
			r.FinishedAt = &t
		}
		if exitCode.Valid {
			c := int(exitCode.Int64)
			r.ExitCode = &c
		}
		r.OutputDir = outputDir.String
		r.Error = errMsg.String
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return out, nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.Local()
}
