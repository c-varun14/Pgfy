package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotDeletable means the project is still being provisioned. Only a ready
// or a failed project may be deleted, so deletion never races the provisioner.
var ErrNotDeletable = errors.New("the project cannot be deleted while it is being set up")

// BeginProjectDeletion marks the project for deletion and, in the same
// transaction, records the audit row, cancels the project's queued jobs and
// queues the delete job. The delete job is queued even behind another job: the
// queue still runs one job at a time. For a project already marked, a delete
// job is queued only if none is waiting or running, which is how a failed
// deletion is retried; created reports whether the tombstone is new.
func (s *Store) BeginProjectDeletion(ctx context.Context, projectID string, newJob func(Project) (id, input string), entry AuditEntry, now time.Time) (job Job, created bool, err error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return job, false, e
	}
	defer tx.Rollback()
	if on, e := maintenance(ctx, tx); e != nil {
		return job, false, e
	} else if on {
		return job, false, ErrMaintenance
	}
	p, e := s.scanProject(tx.QueryRowContext(ctx, "SELECT "+projectCols+" FROM projects WHERE id=?", projectID))
	if errors.Is(e, sql.ErrNoRows) {
		return job, false, ErrProjectNotFound
	}
	if e != nil {
		return job, false, e
	}
	if p.Stage == "deleting" {
		active, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE project_id=? AND kind='delete' AND state IN ('queued','running') ORDER BY created_at LIMIT 1", projectID))
		if e == nil {
			return active, false, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return job, false, e
		}
	} else {
		if p.Stage != "ready" && !p.Failed {
			return job, false, ErrNotDeletable
		}
		if _, e = tx.ExecContext(ctx, "UPDATE projects SET stage='deleting', failed=0, stage_error='', deleting_at=? WHERE id=?", now.Unix(), projectID); e != nil {
			return job, false, e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE jobs SET state='failed', error='Cancelled: the database is being deleted.', finished_at=? WHERE project_id=? AND state='queued' AND kind<>'delete'", now.Unix(), projectID); e != nil {
			return job, false, e
		}
		if e = audit(ctx, tx, entry); e != nil {
			return job, false, e
		}
		created = true
	}
	id, input := newJob(p)
	if e = insertDeleteJob(ctx, tx, id, projectID, input, now); e != nil {
		return job, false, e
	}
	if e = tx.Commit(); e != nil {
		return job, false, e
	}
	job, e = s.Job(ctx, id)
	return job, created, e
}

func insertDeleteJob(ctx context.Context, tx *sql.Tx, id, projectID, input string, now time.Time) error {
	_, e := tx.ExecContext(ctx, "INSERT INTO jobs(id,kind,project_id,input,created_at) VALUES (?,'delete',?,?,?)", id, projectID, input, now.Unix())
	return e
}

// ResumeDeletions queues a delete job for every project marked for deletion
// that has none waiting or running: after a restart, or after a failed
// attempt. Nothing is queued while an update holds the installation quiet.
func (s *Store) ResumeDeletions(ctx context.Context, newJob func(Project) (id, input string), now time.Time) (int, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if on, e := maintenance(ctx, tx); e != nil || on {
		return 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+prefixed(projectCols, "p")+` FROM projects p WHERE p.stage='deleting'
		AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.project_id=p.id AND j.kind='delete' AND j.state IN ('queued','running'))
		ORDER BY p.deleting_at, p.id`)
	if e != nil {
		return 0, e
	}
	var pending []Project
	for rows.Next() {
		p, e := s.scanProject(rows)
		if e != nil {
			rows.Close()
			return 0, e
		}
		pending = append(pending, p)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return 0, e
	}
	for _, p := range pending {
		id, input := newJob(p)
		if e = insertDeleteJob(ctx, tx, id, p.ID, input, now); e != nil {
			return 0, e
		}
	}
	return len(pending), tx.Commit()
}

// LastDeleteJob returns the newest delete job of a project, if any.
func (s *Store) LastDeleteJob(ctx context.Context, projectID string) (*Job, error) {
	j, e := scanJob(s.DB.QueryRowContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE project_id=? AND kind='delete' ORDER BY created_at DESC, id DESC LIMIT 1", projectID))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &j, nil
}

// CompleteProjectDeletion removes the project's rows once its database and
// role are gone, finishes the job and records the notice, in one transaction.
// Dependent rows are deleted explicitly rather than trusted to the foreign-key
// cascade, so the sealed password can never outlive the project. Job and
// backup history keep their rows with the project reference cleared, and the
// bucket is not touched.
func (s *Store) CompleteProjectDeletion(ctx context.Context, jobID string, p Project, result string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var stage string
	e = tx.QueryRowContext(ctx, "SELECT stage FROM projects WHERE id=?", p.ID).Scan(&stage)
	if errors.Is(e, sql.ErrNoRows) || (e == nil && stage != "deleting") {
		return errors.New("the project is no longer marked for deletion")
	}
	if e != nil {
		return e
	}
	for _, statement := range []string{
		"DELETE FROM project_secrets WHERE project_id=?",
		"DELETE FROM policy_revisions WHERE project_id=?",
		"DELETE FROM policy_state WHERE project_id=?",
		"DELETE FROM connection_checks WHERE project_id=?",
		"DELETE FROM project_limits WHERE project_id=?",
		"DELETE FROM backup_schedule WHERE project_id=?",
		"UPDATE jobs SET project_id=NULL WHERE project_id=?",
		"UPDATE backups SET project_id=NULL WHERE project_id=?",
		"DELETE FROM projects WHERE id=?",
	} {
		if _, e = tx.ExecContext(ctx, statement, p.ID); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE jobs SET state='succeeded', stage='done', error='', result=?, finished_at=? WHERE id=?", result, now.Unix(), jobID); e != nil {
		return e
	}
	if e = RecordAlertEvent(ctx, tx, "project_deleted", "project_deleted:"+p.ID, "A database was deleted",
		"Project "+p.Name+" ("+p.DBName+") and its PostgreSQL user were removed. Its backups stay in the bucket under the retention policy.", now); e != nil {
		return e
	}
	return tx.Commit()
}

// FailDeleteJob records a failed deletion attempt. The administrator is told
// once per deletion, on its first failure; later automatic retries that fail
// the same way are visible on the project without repeating the alert.
func (s *Store) FailDeleteJob(ctx context.Context, jobID, projectID, projectName, stage, message, result string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var earlier int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE project_id=? AND kind='delete' AND state IN ('failed','interrupted') AND id<>?", projectID, jobID).Scan(&earlier); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE jobs SET state='failed', stage=?, error=?, result=?, finished_at=? WHERE id=?", stage, message, result, now.Unix(), jobID); e != nil {
		return e
	}
	if earlier == 0 && projectID != "" {
		if e = RecordAlertEvent(ctx, tx, "delete_failed", "delete_failed:"+projectID, "Deleting "+projectName+" did not finish",
			"It stopped at stage "+stage+": "+message+" It is retried automatically every 15 minutes; see the database page.", now); e != nil {
			return e
		}
	}
	return tx.Commit()
}
