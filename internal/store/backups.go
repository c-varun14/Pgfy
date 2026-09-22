package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Backup states as recorded in the reconciled view of the bucket. Only
// "complete" is recoverable: the archive alone cannot be restored, and a
// manifest without its archive is an incomplete backup that is never offered.
const (
	BackupComplete     = "complete"
	BackupManifestOnly = "manifest_only"
	BackupArchiveOnly  = "archive_only"
	BackupDamaged      = "damaged"
)

type BackupPolicy struct {
	TargetIntervalHours int   `json:"target_interval_hours"`
	RetentionDaily      int   `json:"retention_daily"`
	RetentionWeekly     int   `json:"retention_weekly"`
	UpdatedAt           int64 `json:"updated_at"`
}

// Interval is the backup target: a goal the scheduler works towards, never a
// guarantee. The dashboard's staleness warning is measured against it.
func (p BackupPolicy) Interval() time.Duration {
	return time.Duration(p.TargetIntervalHours) * time.Hour
}

var backupIntervals = map[int]bool{1: true, 6: true, 12: true, 24: true}

func (p BackupPolicy) Validate() error {
	if !backupIntervals[p.TargetIntervalHours] {
		return errors.New("the backup target interval must be 1, 6, 12 or 24 hours")
	}
	if p.RetentionDaily < 1 || p.RetentionDaily > 90 {
		return errors.New("daily retention must be between 1 and 90 backups")
	}
	if p.RetentionWeekly < 0 || p.RetentionWeekly > 52 {
		return errors.New("weekly retention must be between 0 and 52 backups")
	}
	return nil
}

func (s *Store) BackupPolicy(ctx context.Context) (BackupPolicy, error) {
	var p BackupPolicy
	e := s.DB.QueryRowContext(ctx, "SELECT target_interval_hours,retention_daily,retention_weekly,updated_at FROM backup_policy WHERE id=1").
		Scan(&p.TargetIntervalHours, &p.RetentionDaily, &p.RetentionWeekly, &p.UpdatedAt)
	return p, e
}

func (s *Store) SetBackupPolicy(ctx context.Context, p BackupPolicy, now time.Time) error {
	if e := p.Validate(); e != nil {
		return e
	}
	_, e := s.DB.ExecContext(ctx, "UPDATE backup_policy SET target_interval_hours=?, retention_daily=?, retention_weekly=?, updated_at=? WHERE id=1",
		p.TargetIntervalHours, p.RetentionDaily, p.RetentionWeekly, now.Unix())
	return e
}

type BackupSchedule struct {
	ProjectID     string `json:"project_id"`
	LastAttemptAt int64  `json:"last_attempt_at"`
	Failures      int    `json:"failures"`
	NextAttemptAt int64  `json:"next_attempt_at"`
}

func (s *Store) BackupSchedule(ctx context.Context, projectID string) (BackupSchedule, error) {
	out := BackupSchedule{ProjectID: projectID}
	e := s.DB.QueryRowContext(ctx, "SELECT last_attempt_at,failures,next_attempt_at FROM backup_schedule WHERE project_id=?", projectID).
		Scan(&out.LastAttemptAt, &out.Failures, &out.NextAttemptAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	return out, e
}

// BackupFailures returns every project that has a failing backup, so status can
// distinguish "failing" from merely "stale".
func (s *Store) BackupFailures(ctx context.Context) (map[string]BackupSchedule, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT project_id,last_attempt_at,failures,next_attempt_at FROM backup_schedule WHERE failures>0")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]BackupSchedule{}
	for rows.Next() {
		var b BackupSchedule
		if e := rows.Scan(&b.ProjectID, &b.LastAttemptAt, &b.Failures, &b.NextAttemptAt); e != nil {
			return nil, e
		}
		out[b.ProjectID] = b
	}
	return out, rows.Err()
}

// backoff spaces out repeated failures: 15 minutes, an hour, four hours, then
// the configured target interval. One failing project must never monopolise the
// single heavy-job slot, and it must never block the projects behind it.
func backoff(failures int, interval time.Duration) time.Duration {
	switch {
	case failures <= 1:
		return 15 * time.Minute
	case failures == 2:
		return time.Hour
	case failures == 3:
		return 4 * time.Hour
	default:
		return interval
	}
}

// BackupCandidates returns every ready project whose newest recoverable backup
// in the active store is older than the target interval and whose backoff has
// expired, least recently attempted first. A backup counts only when its
// manifest names this installation, this project and its database, so another
// server's backup can never satisfy a local project's schedule.
func (s *Store) BackupCandidates(ctx context.Context, target, installationID string, now time.Time, interval time.Duration) ([]Project, error) {
	rows, e := s.DB.QueryContext(ctx, `
		SELECT `+prefixed(projectCols, "p")+` FROM projects p
		LEFT JOIN backup_schedule s ON s.project_id=p.id
		LEFT JOIN (SELECT project_id, db_name, max(taken_at) AS newest FROM bucket_backups
		           WHERE storage_target=? AND state='complete' AND installation_id=?
		           GROUP BY project_id, db_name) b ON b.project_id=p.id AND b.db_name=p.db_name
		WHERE p.stage='ready' AND p.failed=0
		  AND COALESCE(s.next_attempt_at,0) <= ?
		  AND COALESCE(b.newest,0) <= ?
		ORDER BY COALESCE(s.last_attempt_at,0) ASC, p.created_at ASC, p.id ASC`,
		target, installationID, now.Unix(), now.Add(-interval).Unix())
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

// prefixed qualifies the shared project column list for a joined query.
func prefixed(columns, alias string) string {
	return alias + "." + strings.Join(strings.Split(columns, ","), ","+alias+".")
}

// NewestRecoverable returns, per project, the time of the newest backup in the
// active store that this installation can actually restore.
func (s *Store) NewestRecoverable(ctx context.Context, target, installationID string) (map[string]int64, error) {
	rows, e := s.DB.QueryContext(ctx, `
		SELECT p.id, max(b.taken_at) FROM projects p
		JOIN bucket_backups b ON b.project_id=p.id AND b.db_name=p.db_name
		WHERE b.storage_target=? AND b.state='complete' AND b.installation_id=?
		GROUP BY p.id`, target, installationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var at int64
		if e := rows.Scan(&id, &at); e != nil {
			return nil, e
		}
		out[id] = at
	}
	return out, rows.Err()
}

type StorageTarget struct {
	Target       string `json:"target"`
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
	Active       bool   `json:"active"`
	ReconciledAt int64  `json:"reconciled_at"`
	LastError    string `json:"last_error"`
}

// ActivateStorageTarget records the store the application is using now. Rows for
// previous targets are kept, never deleted: backup-job provenance may still
// point at them and is what authorises deleting an abandoned archive.
func (s *Store) ActivateStorageTarget(ctx context.Context, t StorageTarget) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO bucket_targets(storage_target,endpoint,bucket,prefix,active) VALUES (?,?,?,?,1)
		ON CONFLICT(storage_target) DO UPDATE SET endpoint=excluded.endpoint, bucket=excluded.bucket, prefix=excluded.prefix, active=1`,
		t.Target, t.Endpoint, t.Bucket, t.Prefix); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE bucket_targets SET active=0 WHERE storage_target<>?", t.Target); e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) StorageTarget(ctx context.Context, target string) (StorageTarget, error) {
	out := StorageTarget{Target: target}
	var active int
	e := s.DB.QueryRowContext(ctx, "SELECT endpoint,bucket,prefix,active,reconciled_at,last_error FROM bucket_targets WHERE storage_target=?", target).
		Scan(&out.Endpoint, &out.Bucket, &out.Prefix, &active, &out.ReconciledAt, &out.LastError)
	out.Active = active != 0
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	return out, e
}

// SetTargetReconciled records a fully successful pass: the prefix listing and
// every database prefix under it. Anything less leaves the previous value, so
// nothing mistakes missing cache rows for an empty bucket.
func (s *Store) SetTargetReconciled(ctx context.Context, target string, at time.Time, lastError string) error {
	if _, e := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO bucket_targets(storage_target,endpoint,bucket,prefix) VALUES (?,'','','')", target); e != nil {
		return e
	}
	if lastError != "" {
		_, e := s.DB.ExecContext(ctx, "UPDATE bucket_targets SET last_error=? WHERE storage_target=?", lastError, target)
		return e
	}
	_, e := s.DB.ExecContext(ctx, "UPDATE bucket_targets SET reconciled_at=?, last_error='' WHERE storage_target=?", at.Unix(), target)
	return e
}

type BucketBackup struct {
	ManifestKey     string `json:"manifest_key"`
	ArchiveKey      string `json:"archive_key"`
	DBName          string `json:"db_name"`
	TakenAt         int64  `json:"taken_at"`
	State           string `json:"state"`
	InstallationID  string `json:"installation_id"`
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	PostgresVersion string `json:"postgres_version"`
	TableCount      int    `json:"table_count"`
	SizeBytes       int64  `json:"size_bytes"`
	DeleteStartedAt int64  `json:"-"`
}

type PrefixState struct {
	DBName       string `json:"db_name"`
	ReconciledAt int64  `json:"reconciled_at"`
	Complete     int    `json:"complete"`
	ManifestOnly int    `json:"manifest_only"`
	ArchiveOnly  int    `json:"archive_only"`
	Damaged      int    `json:"damaged"`
	Mixed        bool   `json:"mixed"`
}

const bucketCols = "manifest_key,archive_key,db_name,taken_at,state,installation_id,project_id,project_name,postgres_version,table_count,size_bytes,delete_started_at"

func scanBucketBackup(row interface{ Scan(...any) error }) (BucketBackup, error) {
	var b BucketBackup
	e := row.Scan(&b.ManifestKey, &b.ArchiveKey, &b.DBName, &b.TakenAt, &b.State, &b.InstallationID, &b.ProjectID, &b.ProjectName, &b.PostgresVersion, &b.TableCount, &b.SizeBytes, &b.DeleteStartedAt)
	return b, e
}

// ReplacePrefix stores one database prefix exactly as a complete listing found
// it. Deletion intent is carried forward only for entries the listing still saw
// as archive-only: seeing the manifest again means the earlier delete did not
// happen, and a stale marker would later be mistaken for our own half-delete.
func (s *Store) ReplacePrefix(ctx context.Context, target string, state PrefixState, backups []BucketBackup, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	keys := map[string]bool{}
	for _, b := range backups {
		keys[b.ManifestKey] = true
	}
	rows, e := tx.QueryContext(ctx, "SELECT manifest_key FROM bucket_backups WHERE storage_target=? AND db_name=?", target, state.DBName)
	if e != nil {
		return e
	}
	var gone []string
	for rows.Next() {
		var key string
		if e := rows.Scan(&key); e != nil {
			rows.Close()
			return e
		}
		if !keys[key] {
			gone = append(gone, key)
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, key := range gone {
		if _, e = tx.ExecContext(ctx, "DELETE FROM bucket_backups WHERE storage_target=? AND manifest_key=?", target, key); e != nil {
			return e
		}
	}
	for _, b := range backups {
		keep := "0"
		if b.State == BackupArchiveOnly {
			keep = "bucket_backups.delete_started_at"
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO bucket_backups(storage_target,manifest_key,archive_key,db_name,taken_at,state,installation_id,project_id,project_name,postgres_version,table_count,size_bytes,seen_at,delete_started_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0)
			ON CONFLICT(storage_target,manifest_key) DO UPDATE SET archive_key=excluded.archive_key, db_name=excluded.db_name, taken_at=excluded.taken_at,
			 state=excluded.state, installation_id=excluded.installation_id, project_id=excluded.project_id, project_name=excluded.project_name,
			 postgres_version=excluded.postgres_version, table_count=excluded.table_count, size_bytes=excluded.size_bytes, seen_at=excluded.seen_at,
			 delete_started_at=`+keep,
			target, b.ManifestKey, b.ArchiveKey, b.DBName, b.TakenAt, b.State, b.InstallationID, b.ProjectID, b.ProjectName, b.PostgresVersion, b.TableCount, b.SizeBytes, now.Unix()); e != nil {
			return e
		}
	}
	mixed := 0
	if state.Mixed {
		mixed = 1
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO bucket_prefixes(storage_target,db_name,reconciled_at,complete,manifest_only,archive_only,damaged,mixed)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(storage_target,db_name) DO UPDATE SET reconciled_at=excluded.reconciled_at, complete=excluded.complete,
		 manifest_only=excluded.manifest_only, archive_only=excluded.archive_only, damaged=excluded.damaged, mixed=excluded.mixed`,
		target, state.DBName, now.Unix(), state.Complete, state.ManifestOnly, state.ArchiveOnly, state.Damaged, mixed); e != nil {
		return e
	}
	return tx.Commit()
}

// TombstonePrefixes keeps a zeroed row for a database prefix that has vanished
// from the bucket. "A complete listing proved nothing is there" has to stay
// distinguishable from "never reconciled", or provenance is kept forever.
func (s *Store) TombstonePrefixes(ctx context.Context, target string, present map[string]bool, now time.Time) error {
	rows, e := s.DB.QueryContext(ctx, "SELECT db_name FROM bucket_prefixes WHERE storage_target=?", target)
	if e != nil {
		return e
	}
	var gone []string
	for rows.Next() {
		var name string
		if e := rows.Scan(&name); e != nil {
			rows.Close()
			return e
		}
		if !present[name] {
			gone = append(gone, name)
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, name := range gone {
		if e := s.ReplacePrefix(ctx, target, PrefixState{DBName: name}, nil, now); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) PrefixStates(ctx context.Context, target string) ([]PrefixState, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT db_name,reconciled_at,complete,manifest_only,archive_only,damaged,mixed FROM bucket_prefixes WHERE storage_target=? ORDER BY db_name", target)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PrefixState{}
	for rows.Next() {
		var p PrefixState
		var mixed int
		if e := rows.Scan(&p.DBName, &p.ReconciledAt, &p.Complete, &p.ManifestOnly, &p.ArchiveOnly, &p.Damaged, &mixed); e != nil {
			return nil, e
		}
		p.Mixed = mixed != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// BucketBackups pages one database prefix, newest first. Every prefix is paged
// on its own, so a bucket holding many databases can never hide one of them.
func (s *Store) BucketBackups(ctx context.Context, target, dbName string, before int64, limit int) ([]BucketBackup, bool, error) {
	if before <= 0 {
		before = 1<<62 - 1
	}
	rows, e := s.DB.QueryContext(ctx, "SELECT "+bucketCols+" FROM bucket_backups WHERE storage_target=? AND db_name=? AND state=? AND taken_at<? ORDER BY taken_at DESC, manifest_key DESC LIMIT ?",
		target, dbName, BackupComplete, before, limit+1)
	if e != nil {
		return nil, false, e
	}
	defer rows.Close()
	out := []BucketBackup{}
	for rows.Next() {
		b, e := scanBucketBackup(rows)
		if e != nil {
			return nil, false, e
		}
		out = append(out, b)
	}
	if e := rows.Err(); e != nil {
		return nil, false, e
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// PrefixBackups returns every recorded entry for one database prefix, in any
// state, for retention and cleanup decisions.
func (s *Store) PrefixBackups(ctx context.Context, target, dbName string) ([]BucketBackup, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+bucketCols+" FROM bucket_backups WHERE storage_target=? AND db_name=? ORDER BY taken_at DESC, manifest_key DESC", target, dbName)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []BucketBackup{}
	for rows.Next() {
		b, e := scanBucketBackup(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) BucketBackup(ctx context.Context, target, manifestKey string) (BucketBackup, error) {
	b, e := scanBucketBackup(s.DB.QueryRowContext(ctx, "SELECT "+bucketCols+" FROM bucket_backups WHERE storage_target=? AND manifest_key=?", target, manifestKey))
	if errors.Is(e, sql.ErrNoRows) {
		return b, ErrProjectNotFound
	}
	return b, e
}

// MarkBackupDeleting records our own intent before retention removes anything.
// It is what lets a later run finish a half-delete whose archive removal failed.
func (s *Store) MarkBackupDeleting(ctx context.Context, target, manifestKey string, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE bucket_backups SET delete_started_at=? WHERE storage_target=? AND manifest_key=?", now.Unix(), target, manifestKey)
	return e
}

// MarkBackupArchiveOnly runs the moment the manifest is gone, before the archive
// delete is attempted, so a failed archive delete never leaves a backup that
// looks recoverable but has no manifest.
func (s *Store) MarkBackupArchiveOnly(ctx context.Context, target, manifestKey string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE bucket_backups SET state=? WHERE storage_target=? AND manifest_key=?", BackupArchiveOnly, target, manifestKey)
	return e
}

// ForgetBucketBackup removes the cache row last, after both objects are gone.
func (s *Store) ForgetBucketBackup(ctx context.Context, target, manifestKey string) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM bucket_backups WHERE storage_target=? AND manifest_key=?", target, manifestKey)
	return e
}

// DeleteLocalBackupRow drops the history row for an object that no longer
// exists, so "newest recoverable backup" follows the bucket and not our history.
func (s *Store) DeleteLocalBackupRow(ctx context.Context, objectKey string) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM backups WHERE object_key=?", objectKey)
	return e
}

// ForgetLostLocalBackups drops history rows whose objects are no longer in the
// store, so the newest recoverable backup is the bucket's answer and not ours.
// It runs only after a complete reconciliation of that store.
func (s *Store) ForgetLostLocalBackups(ctx context.Context, target string) (int64, error) {
	res, e := s.DB.ExecContext(ctx, `DELETE FROM backups WHERE NOT EXISTS (
		SELECT 1 FROM bucket_backups b WHERE b.storage_target=? AND b.manifest_key=backups.object_key AND b.state='complete')`, target)
	if e != nil {
		return 0, e
	}
	return res.RowsAffected()
}

// AbandonedUploadKey reports whether a finished backup job claims this exact
// directory in this exact store. Without that record an archive of unknown
// origin is left alone: another installation may have written it.
func (s *Store) AbandonedUploadKey(ctx context.Context, target, key string, before time.Time) (bool, error) {
	var n int
	e := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind='backup' AND target_storage=? AND target_key=?
		AND state IN ('failed','interrupted') AND finished_at>0 AND finished_at<?`, target, key, before.Unix()).Scan(&n)
	return n > 0, e
}

// PruneJobs keeps job history bounded, but never at the cost of provenance: a
// row is kept while its directory may still need cleaning, and while the store
// it belongs to has no complete reconciliation proving the archive is gone.
func (s *Store) PruneJobs(ctx context.Context, before time.Time) (int64, error) {
	res, e := s.DB.ExecContext(ctx, `DELETE FROM jobs WHERE finished_at>0 AND finished_at<?
		AND NOT EXISTS (SELECT 1 FROM bucket_backups b WHERE b.storage_target=jobs.target_storage AND b.state='archive_only'
		                 AND jobs.target_key<>'' AND b.manifest_key LIKE jobs.target_key || '%')
		AND (jobs.target_key='' OR EXISTS (SELECT 1 FROM bucket_targets t WHERE t.storage_target=jobs.target_storage AND t.reconciled_at>0))`,
		before.Unix())
	if e != nil {
		return 0, e
	}
	return res.RowsAffected()
}
