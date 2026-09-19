package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrRevisionConflict = errors.New("policy revision conflict")
var ErrProjectNotFound = errors.New("project not found")

type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DBName     string `json:"db_name"`
	RoleName   string `json:"role_name"`
	Stage      string `json:"stage"`
	Failed     bool   `json:"failed"`
	StageError string `json:"stage_error"`
	CreatedAt  int64  `json:"created_at"`
	ReadyAt    int64  `json:"ready_at"`
}

func (s *Store) scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var ready sql.NullInt64
	var failed int
	e := row.Scan(&p.ID, &p.Name, &p.DBName, &p.RoleName, &p.Stage, &failed, &p.StageError, &p.CreatedAt, &ready)
	p.Failed = failed != 0
	if ready.Valid {
		p.ReadyAt = ready.Int64
	}
	return p, e
}

const projectCols = "id,name,db_name,role_name,stage,failed,stage_error,created_at,ready_at"

// CreateProject persists identity and sealed credentials atomically; a repeated
// idempotency key returns the existing project with created=false.
func (s *Store) CreateProject(ctx context.Context, p Project, idempotencyKey, sealedPassword string, now time.Time) (Project, bool, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Project{}, false, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "INSERT INTO projects(id,name,db_name,role_name,idempotency_key,stage,created_at) VALUES (?,?,?,?,?,'identity_persisted',?)",
		p.ID, p.Name, p.DBName, p.RoleName, idempotencyKey, now.Unix())
	if e != nil {
		tx.Rollback()
		// A unique-key conflict on retry returns the originally persisted project.
		existing, se := s.projectBy(ctx, "idempotency_key", idempotencyKey)
		if se == nil {
			return existing, false, nil
		}
		return Project{}, false, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO project_secrets(project_id,sealed_password) VALUES (?,?)", p.ID, sealedPassword); e != nil {
		return Project{}, false, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO policy_state(project_id) VALUES (?)", p.ID); e != nil {
		return Project{}, false, e
	}
	if e = tx.Commit(); e != nil {
		return Project{}, false, e
	}
	p.Stage = "identity_persisted"
	p.CreatedAt = now.Unix()
	return p, true, nil
}

func (s *Store) projectBy(ctx context.Context, column, value string) (Project, error) {
	row := s.DB.QueryRowContext(ctx, "SELECT "+projectCols+" FROM projects WHERE "+column+"=?", value)
	p, e := s.scanProject(row)
	if errors.Is(e, sql.ErrNoRows) {
		return p, ErrProjectNotFound
	}
	return p, e
}

func (s *Store) Project(ctx context.Context, id string) (Project, error) {
	return s.projectBy(ctx, "id", id)
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+projectCols+" FROM projects ORDER BY created_at, id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, e := s.scanProject(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IncompleteProjects returns projects the reconciler still owes work:
// ready projects are done; failed projects wait for an explicit retry.
func (s *Store) IncompleteProjects(ctx context.Context) ([]Project, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+projectCols+" FROM projects WHERE failed=0 AND stage<>'ready' ORDER BY created_at, id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, e := s.scanProject(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetProjectStage(ctx context.Context, id, stage string, now time.Time) error {
	if stage == "ready" {
		_, e := s.DB.ExecContext(ctx, "UPDATE projects SET stage=?, ready_at=? WHERE id=?", stage, now.Unix(), id)
		return e
	}
	_, e := s.DB.ExecContext(ctx, "UPDATE projects SET stage=? WHERE id=?", stage, id)
	return e
}

func (s *Store) FailProject(ctx context.Context, id, stageError string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE projects SET failed=1, stage_error=? WHERE id=?", stageError, id)
	return e
}

// RetryProject clears the failure so the reconciler resumes from the
// persisted stage using the existing identity.
func (s *Store) RetryProject(ctx context.Context, id string) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE projects SET failed=0, stage_error='' WHERE id=? AND failed=1", id)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrProjectNotFound
	}
	return nil
}

func (s *Store) SealedPassword(ctx context.Context, id string) (string, error) {
	var sealed string
	e := s.DB.QueryRowContext(ctx, "SELECT sealed_password FROM project_secrets WHERE project_id=?", id).Scan(&sealed)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrProjectNotFound
	}
	return sealed, e
}

type PolicyState struct {
	CurrentRevision int             `json:"current_revision"`
	AppliedRevision int             `json:"applied_revision"`
	State           string          `json:"state"`
	LastError       string          `json:"last_error"`
	Addresses       json.RawMessage `json:"addresses"` // JSON array of the current revision
}

// RecordPolicyRevision replaces the desired allowlist under an optimistic
// revision check and marks the project pending application.
func (s *Store) RecordPolicyRevision(ctx context.Context, projectID string, expectedRevision int, addresses string, now time.Time) (int, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var current int
	e = tx.QueryRowContext(ctx, "SELECT current_revision FROM policy_state WHERE project_id=?", projectID).Scan(&current)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, ErrProjectNotFound
	}
	if e != nil {
		return 0, e
	}
	if current != expectedRevision {
		return 0, ErrRevisionConflict
	}
	next := current + 1
	if _, e = tx.ExecContext(ctx, "INSERT INTO policy_revisions(project_id,revision,addresses,created_at) VALUES (?,?,?,?)", projectID, next, addresses, now.Unix()); e != nil {
		return 0, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE policy_state SET current_revision=?, state='pending', last_error='' WHERE project_id=?", next, projectID); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	return next, nil
}

func (s *Store) PolicyState(ctx context.Context, projectID string) (PolicyState, error) {
	var st PolicyState
	var addresses string
	e := s.DB.QueryRowContext(ctx, `SELECT st.current_revision, st.applied_revision, st.state, st.last_error,
		COALESCE((SELECT addresses FROM policy_revisions r WHERE r.project_id=st.project_id AND r.revision=st.current_revision), '[]')
		FROM policy_state st WHERE st.project_id=?`, projectID).
		Scan(&st.CurrentRevision, &st.AppliedRevision, &st.State, &st.LastError, &addresses)
	if errors.Is(e, sql.ErrNoRows) {
		return st, ErrProjectNotFound
	}
	st.Addresses = json.RawMessage(addresses)
	return st, e
}

// ApplyPolicyResult records the host helper's outcome for one project.
func (s *Store) ApplyPolicyResult(ctx context.Context, projectID string, revision int, applied bool, message string) error {
	if applied {
		_, e := s.DB.ExecContext(ctx, `UPDATE policy_state SET applied_revision=?, state='applied', last_error=''
			WHERE project_id=? AND current_revision=?`, revision, projectID, revision)
		return e
	}
	_, e := s.DB.ExecContext(ctx, `UPDATE policy_state SET state='failed', last_error=?
		WHERE project_id=? AND current_revision=?`, message, projectID, revision)
	return e
}

type ConnectionCheck struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	ChallengeHash string
	ExpiresAt     int64  `json:"expires_at"`
	State         string `json:"state"`
	Evidence      string `json:"evidence"`
	Fingerprint   string `json:"fingerprint"`
	CreatedAt     int64  `json:"created_at"`
}

func (s *Store) NewConnectionCheck(ctx context.Context, id, projectID, challengeHash, fingerprint string, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "INSERT INTO connection_checks(id,project_id,challenge_hash,expires_at,fingerprint,created_at) VALUES (?,?,?,?,?,?)",
		id, projectID, challengeHash, now.Add(10*time.Minute).Unix(), fingerprint, now.Unix())
	return e
}

func (s *Store) ConnectionCheck(ctx context.Context, projectID, checkID string) (ConnectionCheck, error) {
	var c ConnectionCheck
	var evidence sql.NullString
	e := s.DB.QueryRowContext(ctx, "SELECT id,project_id,expires_at,state,evidence,fingerprint,created_at FROM connection_checks WHERE id=? AND project_id=?", checkID, projectID).
		Scan(&c.ID, &c.ProjectID, &c.ExpiresAt, &c.State, &evidence, &c.Fingerprint, &c.CreatedAt)
	if evidence.Valid {
		c.Evidence = evidence.String
	}
	if errors.Is(e, sql.ErrNoRows) {
		return c, ErrProjectNotFound
	}
	return c, e
}

// ExpireConnectionChecks marks overdue pending checks; callers run it before
// reading a check so "pending" never outlives its deadline.
func (s *Store) ExpireConnectionChecks(ctx context.Context, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE connection_checks SET state='expired' WHERE state='pending' AND expires_at<=?", now.Unix())
	return e
}

// PendingChallengeHash returns the challenge hash for a pending check.
func (s *Store) PendingChallengeHash(ctx context.Context, projectID, checkID string) (string, error) {
	var hash string
	e := s.DB.QueryRowContext(ctx, "SELECT challenge_hash FROM connection_checks WHERE id=? AND project_id=? AND state='pending'", checkID, projectID).Scan(&hash)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrProjectNotFound
	}
	return hash, e
}

// CompleteConnectionCheck stores imported evidence exactly once.
func (s *Store) CompleteConnectionCheck(ctx context.Context, id, evidence string) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE connection_checks SET state='successful', evidence=? WHERE id=? AND state='pending'", evidence, id)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrProjectNotFound
	}
	return nil
}
