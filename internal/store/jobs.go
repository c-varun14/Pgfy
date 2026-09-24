package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrJobBusy = errors.New("another heavy job is already queued or running")

type Job struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	ProjectID     string `json:"project_id"`
	State         string `json:"state"` // queued, running, succeeded, failed, interrupted
	Stage         string `json:"stage"`
	Error         string `json:"error"`
	Input         string `json:"-"`
	Result        string `json:"result"`
	CreatedAt     int64  `json:"created_at"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at"`
	StageAt       int64  `json:"stage_at"`
	Scheduled     bool   `json:"scheduled"`
	TargetStorage string `json:"-"`
	TargetKey     string `json:"-"`
}

const jobCols = "id,kind,COALESCE(project_id,''),state,stage,error,input,result,created_at,COALESCE(started_at,0),COALESCE(finished_at,0),COALESCE(stage_at,0),scheduled,target_storage,target_key"

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var scheduled int
	e := row.Scan(&j.ID, &j.Kind, &j.ProjectID, &j.State, &j.Stage, &j.Error, &j.Input, &j.Result, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.StageAt, &scheduled, &j.TargetStorage, &j.TargetKey)
	j.Scheduled = scheduled != 0
	return j, e
}

// EnqueueJob records a heavy job. Only one backup/restore may be queued or
// running at a time so the small host is never asked to do two dumps at once.
func (s *Store) EnqueueJob(ctx context.Context, id, kind, projectID, input string, now time.Time) (Job, error) {
	return s.enqueue(ctx, id, kind, projectID, input, false, now)
}

// EnqueueBackupJob records the job and the attempt together. An attempt that
// existed without its scheduling state would defeat the fairness ordering and
// the failure backoff after a crash between the two writes.
func (s *Store) EnqueueBackupJob(ctx context.Context, id, projectID, input string, scheduled bool, now time.Time) (Job, error) {
	return s.enqueue(ctx, id, "backup", projectID, input, scheduled, now)
}

func (s *Store) enqueue(ctx context.Context, id, kind, projectID, input string, scheduled bool, now time.Time) (Job, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	// Checked first so nothing, not even the attempt time, is written while an
	// update holds the installation quiet.
	if on, e := maintenance(ctx, tx); e != nil {
		return Job{}, e
	} else if on {
		return Job{}, ErrMaintenance
	}
	var active int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE state IN ('queued','running')").Scan(&active); e != nil {
		return Job{}, e
	}
	if active > 0 {
		return Job{}, ErrJobBusy
	}
	var project any
	if projectID != "" {
		project = projectID
	}
	flag := 0
	if scheduled {
		flag = 1
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO jobs(id,kind,project_id,input,created_at,scheduled) VALUES (?,?,?,?,?,?)", id, kind, project, input, now.Unix(), flag); e != nil {
		return Job{}, e
	}
	if kind == "backup" && projectID != "" {
		if _, e = tx.ExecContext(ctx, `INSERT INTO backup_schedule(project_id,last_attempt_at) VALUES (?,?)
			ON CONFLICT(project_id) DO UPDATE SET last_attempt_at=excluded.last_attempt_at`, projectID, now.Unix()); e != nil {
			return Job{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return Job{}, e
	}
	return s.Job(ctx, id)
}

// SetJobTarget records where a backup intends to write before it uploads
// anything. Cleanup later needs this to prove an abandoned archive is ours and
// not another installation's work in the same bucket.
func (s *Store) SetJobTarget(ctx context.Context, id, target, key string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE jobs SET target_storage=?, target_key=? WHERE id=?", target, key, id)
	return e
}

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	j, e := scanJob(s.DB.QueryRowContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE id=?", id))
	if errors.Is(e, sql.ErrNoRows) {
		return j, ErrProjectNotFound
	}
	return j, e
}

func (s *Store) jobs(ctx context.Context, query string, args ...any) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+jobCols+" FROM jobs "+query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ProjectJobs(ctx context.Context, projectID string, limit int) ([]Job, error) {
	return s.jobs(ctx, "WHERE project_id=? ORDER BY created_at DESC, id DESC LIMIT ?", projectID, limit)
}

func (s *Store) ActiveJobs(ctx context.Context) ([]Job, error) {
	return s.jobs(ctx, "WHERE state IN ('queued','running') ORDER BY created_at, id")
}

func (s *Store) RecentJobs(ctx context.Context, limit int) ([]Job, error) {
	return s.jobs(ctx, "ORDER BY created_at DESC, id DESC LIMIT ?", limit)
}

// ClaimJob moves the oldest queued job to running; nil means nothing to do.
func (s *Store) ClaimJob(ctx context.Context, now time.Time) (*Job, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	j, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE state='queued' ORDER BY created_at, id LIMIT 1"))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	result, e := tx.ExecContext(ctx, "UPDATE jobs SET state='running', stage='starting', started_at=?, stage_at=? WHERE id=? AND "+maintenanceGuard, now.Unix(), now.Unix(), j.ID)
	if e != nil {
		return nil, e
	}
	if n, e := result.RowsAffected(); e != nil || n == 0 {
		// Queued work waits for the update to finish.
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	j.State, j.Stage, j.StartedAt = "running", "starting", now.Unix()
	return &j, nil
}

func (s *Store) SetJobStage(ctx context.Context, id, stage string, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE jobs SET stage=?, stage_at=? WHERE id=? AND state='running'", stage, now.Unix(), id)
	return e
}

func (s *Store) FinishJob(ctx context.Context, id, state, stage, errorText, result string, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE jobs SET state=?, stage=?, error=?, result=?, finished_at=? WHERE id=?", state, stage, errorText, result, now.Unix(), id)
	return e
}

// InterruptRunningJobs is called at startup: a job that was running when the
// process died cannot have finished, so it is never reported as success. An
// interrupted scheduled backup counts as a failed attempt in the same
// transaction, so a crash loop backs off instead of restarting immediately.
func (s *Store) InterruptRunningJobs(ctx context.Context, now time.Time, interval time.Duration) ([]Job, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE state='running'")
	if e != nil {
		return nil, e
	}
	var running []Job
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		running = append(running, j)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for _, j := range running {
		if _, e = tx.ExecContext(ctx, "UPDATE jobs SET state='interrupted', error='The application restarted before this job finished.', finished_at=? WHERE id=?", now.Unix(), j.ID); e != nil {
			return nil, e
		}
		if j.Kind == "backup" && j.Scheduled && j.ProjectID != "" {
			if e = recordOutcome(ctx, tx, j.ProjectID, false, now, interval); e != nil {
				return nil, e
			}
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return running, nil
}

// CompleteBackupJob finishes a backup: the job row, its history row and the
// schedule outcome are one transaction, so a crash can neither lose the backoff
// nor record a backup the job never reported.
func (s *Store) CompleteBackupJob(ctx context.Context, id, projectID, state, stage, errorText, result string, backup *Backup, scheduled bool, now time.Time, interval time.Duration) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "UPDATE jobs SET state=?, stage=?, error=?, result=?, finished_at=? WHERE id=?", state, stage, errorText, result, now.Unix(), id); e != nil {
		return e
	}
	if backup != nil {
		if _, e = tx.ExecContext(ctx, "INSERT INTO backups(id,project_id,job_id,object_key,manifest,size_bytes,created_at) VALUES (?,?,?,?,?,?,?)",
			backup.ID, backup.ProjectID, backup.JobID, backup.ObjectKey, backup.Manifest, backup.SizeBytes, backup.CreatedAt); e != nil {
			return e
		}
	}
	if projectID != "" {
		succeeded := state == "succeeded"
		// A manual attempt can only help: success clears the backoff, while a
		// manual failure never pushes the automatic schedule further out.
		if succeeded || scheduled {
			if e = recordOutcome(ctx, tx, projectID, succeeded, now, interval); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}

func recordOutcome(ctx context.Context, tx *sql.Tx, projectID string, succeeded bool, now time.Time, interval time.Duration) error {
	if succeeded {
		_, e := tx.ExecContext(ctx, `INSERT INTO backup_schedule(project_id,last_attempt_at,failures,next_attempt_at) VALUES (?,?,0,0)
			ON CONFLICT(project_id) DO UPDATE SET last_attempt_at=excluded.last_attempt_at, failures=0, next_attempt_at=0`, projectID, now.Unix())
		return e
	}
	var failures int
	e := tx.QueryRowContext(ctx, "SELECT failures FROM backup_schedule WHERE project_id=?", projectID).Scan(&failures)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	failures++
	next := now.Add(backoff(failures, interval)).Unix()
	_, e = tx.ExecContext(ctx, `INSERT INTO backup_schedule(project_id,last_attempt_at,failures,next_attempt_at) VALUES (?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET last_attempt_at=excluded.last_attempt_at, failures=excluded.failures, next_attempt_at=excluded.next_attempt_at`,
		projectID, now.Unix(), failures, next)
	return e
}

type Backup struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	JobID     string `json:"job_id"`
	ObjectKey string `json:"object_key"`
	Manifest  string `json:"manifest"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) RecordBackup(ctx context.Context, b Backup) error {
	_, e := s.DB.ExecContext(ctx, "INSERT INTO backups(id,project_id,job_id,object_key,manifest,size_bytes,created_at) VALUES (?,?,?,?,?,?,?)", b.ID, b.ProjectID, b.JobID, b.ObjectKey, b.Manifest, b.SizeBytes, b.CreatedAt)
	return e
}

func (s *Store) ProjectBackups(ctx context.Context, projectID string, limit int) ([]Backup, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,COALESCE(project_id,''),job_id,object_key,manifest,size_bytes,created_at FROM backups WHERE project_id=? ORDER BY created_at DESC LIMIT ?", projectID, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		var b Backup
		if e := rows.Scan(&b.ID, &b.ProjectID, &b.JobID, &b.ObjectKey, &b.Manifest, &b.SizeBytes, &b.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var sealed string
	e := s.DB.QueryRowContext(ctx, "SELECT sealed_value FROM settings WHERE key=?", key).Scan(&sealed)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return sealed, e
}

func (s *Store) SetSetting(ctx context.Context, key, sealed string, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "INSERT INTO settings(key,sealed_value,updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET sealed_value=excluded.sealed_value, updated_at=excluded.updated_at", key, sealed, now.Unix())
	return e
}
