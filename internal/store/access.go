package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// AuditEntry records one administrative action. Detail is a short description
// and never carries a secret.
type AuditEntry struct {
	ID        int64  `json:"id"`
	At        int64  `json:"at"`
	AdminID   int64  `json:"admin_id"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	RequestID string `json:"request_id"`
	Detail    string `json:"detail"`
}

func (s *Store) Audit(ctx context.Context, entry AuditEntry) error {
	return audit(ctx, s.DB, entry)
}

func audit(ctx context.Context, x execer, entry AuditEntry) error {
	var admin any
	if entry.AdminID != 0 {
		admin = entry.AdminID
	}
	_, e := x.ExecContext(ctx, "INSERT INTO audit(at,admin_id,action,target,request_id,detail) VALUES (?,?,?,?,?,?)",
		entry.At, admin, entry.Action, entry.Target, entry.RequestID, entry.Detail)
	return e
}

func (s *Store) AuditEntries(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,at,COALESCE(admin_id,0),action,target,request_id,detail FROM audit ORDER BY id DESC LIMIT ?", limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if e = rows.Scan(&a.ID, &a.At, &a.AdminID, &a.Action, &a.Target, &a.RequestID, &a.Detail); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Limits are the guardrails set on a project's role. Timeouts of 0 are off;
// a temp_file_limit of -1 is unlimited, as in PostgreSQL.
type Limits struct {
	StatementTimeoutMS  int64 `json:"statement_timeout_ms"`
	IdleInTransactionMS int64 `json:"idle_in_transaction_ms"`
	TempFileLimitKB     int64 `json:"temp_file_limit_kb"`
	LockTimeoutMS       int64 `json:"lock_timeout_ms"`
	ConnectionLimit     int64 `json:"connection_limit"`
}

var DefaultLimits = Limits{StatementTimeoutMS: 60_000, IdleInTransactionMS: 300_000, TempFileLimitKB: 1_048_576, LockTimeoutMS: 10_000, ConnectionLimit: 25}

// Validate keeps every value inside a range PostgreSQL accepts and that cannot
// disable the database by accident: a temp_file_limit of 0 would fail every
// sort that spills to disk, so it is refused.
func (l Limits) Validate() error {
	timeout := func(name string, v int64) error {
		if v != 0 && (v < 1000 || v > 86_400_000) {
			return fmt.Errorf("%s must be off or between 1 second and 24 hours", name)
		}
		return nil
	}
	for _, e := range []error{timeout("statement timeout", l.StatementTimeoutMS), timeout("idle-in-transaction timeout", l.IdleInTransactionMS), timeout("lock timeout", l.LockTimeoutMS)} {
		if e != nil {
			return e
		}
	}
	if l.TempFileLimitKB != -1 && (l.TempFileLimitKB < 1024 || l.TempFileLimitKB > 1_073_741_824) {
		return errors.New("temporary file limit must be unlimited or between 1 MB and 1 TB")
	}
	if l.ConnectionLimit < 1 || l.ConnectionLimit > 100 {
		return errors.New("connection limit must be between 1 and 100")
	}
	return nil
}

type ProjectLimits struct {
	Limits
	Revision        int64 `json:"revision"`
	AppliedRevision int64 `json:"applied_revision"`
}

const limitCols = "statement_timeout_ms,idle_in_transaction_ms,temp_file_limit_kb,lock_timeout_ms,connection_limit,revision,applied_revision"

func scanLimits(row interface{ Scan(...any) error }, extra ...any) (ProjectLimits, error) {
	var l ProjectLimits
	e := row.Scan(append([]any{&l.StatementTimeoutMS, &l.IdleInTransactionMS, &l.TempFileLimitKB, &l.LockTimeoutMS, &l.ConnectionLimit, &l.Revision, &l.AppliedRevision}, extra...)...)
	return l, e
}

func (s *Store) ProjectLimits(ctx context.Context, projectID string) (ProjectLimits, error) {
	l, e := scanLimits(s.DB.QueryRowContext(ctx, "SELECT "+limitCols+" FROM project_limits WHERE project_id=?", projectID))
	if errors.Is(e, sql.ErrNoRows) {
		return l, ErrProjectNotFound
	}
	return l, e
}

// SetProjectLimits records a new revision only if the caller saw the current
// one, so two editors cannot silently overwrite each other.
func (s *Store) SetProjectLimits(ctx context.Context, projectID string, l Limits, seenRevision int64, now time.Time) (ProjectLimits, error) {
	if e := l.Validate(); e != nil {
		return ProjectLimits{}, e
	}
	r, e := s.DB.ExecContext(ctx, `UPDATE project_limits SET statement_timeout_ms=?, idle_in_transaction_ms=?, temp_file_limit_kb=?, lock_timeout_ms=?, connection_limit=?, revision=revision+1, updated_at=?
		WHERE project_id=? AND revision=?`, l.StatementTimeoutMS, l.IdleInTransactionMS, l.TempFileLimitKB, l.LockTimeoutMS, l.ConnectionLimit, now.Unix(), projectID, seenRevision)
	if e != nil {
		return ProjectLimits{}, e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		if _, e := s.ProjectLimits(ctx, projectID); e != nil {
			return ProjectLimits{}, e
		}
		return ProjectLimits{}, ErrRevisionConflict
	}
	return s.ProjectLimits(ctx, projectID)
}

type RoleLimits struct {
	ProjectID string
	Role      string
	ProjectLimits
}

// ReadyProjectLimits lists every ready project's desired limits, applied or not:
// drift on the PostgreSQL side is detected by comparing, not by revision alone.
func (s *Store) ReadyProjectLimits(ctx context.Context) ([]RoleLimits, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+limitCols+", p.id, p.role_name FROM project_limits l JOIN projects p ON p.id=l.project_id WHERE p.stage='ready' ORDER BY p.created_at")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []RoleLimits{}
	for rows.Next() {
		var r RoleLimits
		if r.ProjectLimits, e = scanLimits(rows, &r.ProjectID, &r.Role); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MarkLimitsApplied(ctx context.Context, projectID string, revision int64) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE project_limits SET applied_revision=? WHERE project_id=? AND revision=? AND applied_revision<?", revision, projectID, revision, revision)
	return e
}

// BeginRotation records the new sealed password before the role changes, so a
// failure at any later step can be finished rather than lost. Overwriting an
// earlier pending password is safe only because every rotation step for a
// project runs under the provisioner's per-project rotation lock.
func (s *Store) BeginRotation(ctx context.Context, projectID, sealed, requestID string) error {
	r, e := s.DB.ExecContext(ctx, "UPDATE project_secrets SET pending_password_sealed=?, pending_request_id=? WHERE project_id=?", sealed, requestID, projectID)
	if e != nil {
		return e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrProjectNotFound
	}
	return nil
}

func (s *Store) PendingPassword(ctx context.Context, projectID string) (string, error) {
	var pending sql.NullString
	e := s.DB.QueryRowContext(ctx, "SELECT pending_password_sealed FROM project_secrets WHERE project_id=?", projectID).Scan(&pending)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrProjectNotFound
	}
	return pending.String, e
}

type PendingRotation struct {
	ProjectID string
	RequestID string
	Sealed    string
}

func (s *Store) PendingRotations(ctx context.Context) ([]PendingRotation, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT project_id, pending_request_id, pending_password_sealed FROM project_secrets WHERE pending_password_sealed IS NOT NULL")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PendingRotation{}
	for rows.Next() {
		var r PendingRotation
		if e = rows.Scan(&r.ProjectID, &r.RequestID, &r.Sealed); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CompleteRotation makes the pending password the active one and records the
// audit row in the same transaction. It is a compare-and-swap on the pending
// value: false means another finisher already completed this rotation.
func (s *Store) CompleteRotation(ctx context.Context, projectID, sealed string, entry AuditEntry) (bool, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	r, e := tx.ExecContext(ctx, "UPDATE project_secrets SET sealed_password=?, pending_password_sealed=NULL, pending_request_id='' WHERE project_id=? AND pending_password_sealed=?", sealed, projectID, sealed)
	if e != nil {
		return false, e
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return false, nil
	}
	if e = audit(ctx, tx, entry); e != nil {
		return false, e
	}
	return true, tx.Commit()
}
