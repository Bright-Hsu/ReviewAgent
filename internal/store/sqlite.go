package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bright/reviewagent/internal/domain"
	_ "modernc.org/sqlite"
)

var ErrInvalidTransition = errors.New("invalid review-run status transition")

type Store interface {
	Claim(context.Context, domain.PullRequest) (domain.ReviewRun, bool, error)
	ClaimNext(context.Context, time.Duration) (domain.ReviewRun, bool, error)
	RenewLease(context.Context, string, time.Duration) error
	Retry(context.Context, string, time.Time, error) error
	CancelOlder(context.Context, domain.PullRequest) error
	RecoverExpired(context.Context, time.Time) error
	MarkRunning(context.Context, string) error
	Complete(context.Context, string, domain.RunStatus, int, int, int, []string) error
	Fail(context.Context, string, error) error
	Get(context.Context, string) (domain.ReviewRun, error)
	Close() error
}

type SQLiteStore struct{ db *sql.DB }

func OpenSQLite(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping SQLite: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS review_runs (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, repo TEXT NOT NULL, pr_number INTEGER NOT NULL, head_sha TEXT NOT NULL,
 status TEXT NOT NULL, created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, error TEXT NOT NULL DEFAULT '',
 files_seen INTEGER NOT NULL DEFAULT 0, files_reviewed INTEGER NOT NULL DEFAULT 0, findings_published INTEGER NOT NULL DEFAULT 0,
 skipped_json TEXT NOT NULL DEFAULT '[]', UNIQUE(owner, repo, pr_number, head_sha)
);`)
	if err != nil {
		return fmt.Errorf("migrate review runs: %w", err)
	}
	for _, column := range []struct{ name, definition string }{
		{"attempts", `INTEGER NOT NULL DEFAULT 0`},
		{"next_attempt_at", `TEXT`},
		{"lease_until", `TEXT`},
	} {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE review_runs ADD COLUMN `+column.name+` `+column.definition); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("add review_runs.%s: %w", column.name, err)
		}
	}
	return nil
}

func (s *SQLiteStore) Claim(ctx context.Context, pr domain.PullRequest) (domain.ReviewRun, bool, error) {
	now := time.Now().UTC()
	id, err := newID()
	if err != nil {
		return domain.ReviewRun{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO review_runs (id, owner, repo, pr_number, head_sha, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, domain.RunQueued, now.Format(time.RFC3339Nano))
	if err != nil {
		return domain.ReviewRun{}, false, fmt.Errorf("claim review run: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.ReviewRun{}, false, err
	}
	if count == 1 {
		if err := s.CancelOlder(ctx, pr); err != nil {
			return domain.ReviewRun{}, false, err
		}
		return domain.ReviewRun{ID: id, PR: pr, Status: domain.RunQueued, CreatedAt: now}, true, nil
	}
	var existing domain.ReviewRun
	if err := s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM review_runs WHERE owner=? AND repo=? AND pr_number=? AND head_sha=?`, pr.Owner, pr.Repo, pr.Number, pr.HeadSHA).Scan(scanRun(&existing)...); err != nil {
		return domain.ReviewRun{}, false, fmt.Errorf("load existing review run: %w", err)
	}
	return existing, false, nil
}

// ClaimNext atomically leases the oldest ready task to the single local worker.
func (s *SQLiteStore) ClaimNext(ctx context.Context, lease time.Duration) (domain.ReviewRun, bool, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ReviewRun{}, false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM review_runs WHERE status=? AND (next_attempt_at IS NULL OR next_attempt_at<=?) ORDER BY created_at LIMIT 1`, domain.RunQueued, now.Format(time.RFC3339Nano)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewRun{}, false, nil
	}
	if err != nil {
		return domain.ReviewRun{}, false, err
	}
	until := now.Add(lease).Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE review_runs SET status=?, started_at=?, attempts=attempts+1, lease_until=?, error='' WHERE id=? AND status=?`, domain.RunRunning, now.Format(time.RFC3339Nano), until, id, domain.RunQueued); err != nil {
		return domain.ReviewRun{}, false, err
	}
	var run domain.ReviewRun
	if err := tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM review_runs WHERE id=?`, id).Scan(scanRun(&run)...); err != nil {
		return domain.ReviewRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReviewRun{}, false, err
	}
	return run, true, nil
}

func (s *SQLiteStore) RenewLease(ctx context.Context, id string, lease time.Duration) error {
	result, err := s.db.ExecContext(ctx, `UPDATE review_runs SET lease_until=? WHERE id=? AND status=?`, time.Now().UTC().Add(lease).Format(time.RFC3339Nano), id, domain.RunRunning)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (s *SQLiteStore) Retry(ctx context.Context, id string, next time.Time, reason error) error {
	message := "temporary failure"
	if reason != nil {
		message = reason.Error()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE review_runs SET status=?, error=?, next_attempt_at=?, lease_until=NULL WHERE id=? AND status=? AND attempts<3`, domain.RunQueued, message, next.UTC().Format(time.RFC3339Nano), id, domain.RunRunning)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (s *SQLiteStore) CancelOlder(ctx context.Context, pr domain.PullRequest) error {
	_, err := s.db.ExecContext(ctx, `UPDATE review_runs SET status=?, finished_at=?, lease_until=NULL WHERE owner=? AND repo=? AND pr_number=? AND head_sha<>? AND status IN (?,?)`, domain.RunCancelled, time.Now().UTC().Format(time.RFC3339Nano), pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, domain.RunQueued, domain.RunRunning)
	return err
}

func (s *SQLiteStore) RecoverExpired(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE review_runs SET status=CASE WHEN attempts<3 THEN ? ELSE ? END, next_attempt_at=CASE WHEN attempts<3 THEN ? ELSE next_attempt_at END, lease_until=NULL, error='worker lease expired' WHERE status=? AND (lease_until IS NULL OR lease_until<=?)`, domain.RunQueued, domain.RunFailed, now.UTC().Format(time.RFC3339Nano), domain.RunRunning, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) MarkRunning(ctx context.Context, id string) error {
	return s.update(ctx, id, []domain.RunStatus{domain.RunQueued}, domain.RunRunning, "", 0, 0, 0, nil, false)
}
func (s *SQLiteStore) Complete(ctx context.Context, id string, status domain.RunStatus, seen, reviewed, findings int, skipped []string) error {
	if status != domain.RunCompleted && status != domain.RunPartial {
		return ErrInvalidTransition
	}
	return s.update(ctx, id, []domain.RunStatus{domain.RunRunning}, status, "", seen, reviewed, findings, skipped, true)
}
func (s *SQLiteStore) Fail(ctx context.Context, id string, reason error) error {
	message := "unknown error"
	if reason != nil {
		message = reason.Error()
	}
	return s.update(ctx, id, []domain.RunStatus{domain.RunQueued, domain.RunRunning}, domain.RunFailed, message, 0, 0, 0, nil, true)
}

func (s *SQLiteStore) update(ctx context.Context, id string, from []domain.RunStatus, to domain.RunStatus, reason string, seen, reviewed, findings int, skipped []string, finish bool) error {
	placeholders := ""
	statusArgs := make([]any, 0, len(from))
	for index, status := range from {
		if index > 0 {
			placeholders += ","
		}
		placeholders += "?"
		statusArgs = append(statusArgs, status)
	}
	query, args := `UPDATE review_runs SET status=?, error=?, lease_until=CASE WHEN ? IN ('completed','partial','failed','cancelled') THEN NULL ELSE lease_until END`, []any{to, reason, to}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if to == domain.RunRunning {
		query += `, started_at=?`
		args = append(args, now)
	}
	if finish {
		encoded, _ := json.Marshal(skipped)
		query += `, finished_at=?, files_seen=?, files_reviewed=?, findings_published=?, skipped_json=?`
		args = append(args, now, seen, reviewed, findings, string(encoded))
	}
	query += ` WHERE id=? AND status IN (` + placeholders + `)`
	args = append(args, id)
	args = append(args, statusArgs...)
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update review run: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrInvalidTransition
	}
	return nil
}

const runColumns = `id, owner, repo, pr_number, head_sha, status, created_at, COALESCE(started_at, ''), COALESCE(finished_at, ''), error, files_seen, files_reviewed, findings_published, skipped_json, attempts, COALESCE(lease_until, ''), COALESCE(next_attempt_at, '')`

func (s *SQLiteStore) Get(ctx context.Context, id string) (domain.ReviewRun, error) {
	var run domain.ReviewRun
	if err := s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM review_runs WHERE id=?`, id).Scan(scanRun(&run)...); err != nil {
		return domain.ReviewRun{}, err
	}
	return run, nil
}

func scanRun(run *domain.ReviewRun) []any {
	return []any{&run.ID, &run.PR.Owner, &run.PR.Repo, &run.PR.Number, &run.PR.HeadSHA, &run.Status, &timeScanner{target: &run.CreatedAt}, &timeScanner{target: &run.StartedAt}, &timeScanner{target: &run.FinishedAt}, &run.Error, &run.FilesSeen, &run.FilesReviewed, &run.FindingsPublished, &skippedScanner{target: &run.Skipped}, &run.Attempts, &timeScanner{target: &run.LeaseUntil}, &timeScanner{target: &run.NextAttemptAt}}
}

type timeScanner struct{ target *time.Time }

func (s *timeScanner) Scan(value any) error {
	if value == nil || value == "" {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("unexpected timestamp %T", value)
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return err
	}
	*s.target = parsed
	return nil
}

type skippedScanner struct{ target *[]string }

func (s *skippedScanner) Scan(value any) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("unexpected skipped JSON %T", value)
	}
	return json.Unmarshal([]byte(text), s.target)
}
func (s *SQLiteStore) Close() error { return s.db.Close() }
func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
