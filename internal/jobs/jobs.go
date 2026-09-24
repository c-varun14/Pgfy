// Package jobs runs the heavy, durable work (backups, restores, the daily
// schedule) in one goroutine, independent of browser sessions. Every stage is
// persisted so an interrupted job is reported as interrupted, never as done.
package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/provision"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/storage"
	"github.com/c-varun14/Pgfy/internal/store"
)

const (
	StorageSetting  = "storage"
	MaxArchiveBytes = 2 << 30 // documented MVP limit per backup
	MaxTables       = 5000    // captured per backup; beyond this, counts are reported as partial
	ReconcileEvery  = 15 * time.Minute
)

var ErrStorageNotConfigured = errors.New("backup storage is not configured")

type Worker struct {
	Store          *store.Store
	Vault          *security.Vault
	PG             *postgres.Management
	Provisioner    *provision.Provisioner
	InstallationID string
	Workspace      string
	Now            func() time.Time
	ScheduleEvery  time.Duration
	kick           chan struct{}
}

func New(s *store.Store, v *security.Vault, pg *postgres.Management, p *provision.Provisioner, installationID, workspace string) *Worker {
	return &Worker{Store: s, Vault: v, PG: pg, Provisioner: p, InstallationID: installationID, Workspace: workspace,
		Now: time.Now, ScheduleEvery: scheduleInterval(), kick: make(chan struct{}, 1)}
}

// scheduleInterval is how often overdue projects are considered. It is bounded
// so a misconfigured value can neither hammer the host nor stall backups.
func scheduleInterval() time.Duration {
	d, e := time.ParseDuration(os.Getenv("PGFY_SCHEDULE_INTERVAL"))
	if e != nil || d < time.Minute {
		if os.Getenv("PGFY_SCHEDULE_INTERVAL") != "" && (e != nil || d < time.Minute) {
			slog.Warn("schedule interval out of range; using the default", "value", os.Getenv("PGFY_SCHEDULE_INTERVAL"))
		}
		return 5 * time.Minute
	}
	if d > time.Hour {
		return time.Hour
	}
	return d
}

func (w *Worker) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

// Policy is the backup target interval and retention in force.
func (w *Worker) Policy(ctx context.Context) store.BackupPolicy {
	p, e := w.Store.BackupPolicy(ctx)
	if e != nil {
		return store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 14, RetentionWeekly: 8}
	}
	return p
}

func (w *Worker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// StorageSettings returns the sealed settings, or ErrStorageNotConfigured.
func (w *Worker) StorageSettings(ctx context.Context) (storage.Settings, error) {
	var s storage.Settings
	sealed, e := w.Store.Setting(ctx, StorageSetting)
	if e != nil {
		return s, e
	}
	if sealed == "" {
		return s, ErrStorageNotConfigured
	}
	plain, e := w.Vault.Open("setting:"+StorageSetting, sealed)
	if e != nil {
		return s, errors.New("stored storage settings cannot be decrypted with this installation key")
	}
	return s, json.Unmarshal(plain, &s)
}

func (w *Worker) SaveStorageSettings(ctx context.Context, s storage.Settings) error {
	if e := s.Validate(); e != nil {
		return e
	}
	b, _ := json.Marshal(s)
	return w.Store.SetSetting(ctx, StorageSetting, w.Vault.Seal("setting:"+StorageSetting, b), time.Now())
}

func (w *Worker) StorageClient(ctx context.Context) (*storage.Client, error) {
	s, e := w.StorageSettings(ctx)
	if e != nil {
		return nil, e
	}
	return storage.New(s)
}

// Run reconciles interrupted work, then processes the queue until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	interval := w.Policy(ctx).Interval()
	if interrupted, e := w.Store.InterruptRunningJobs(ctx, w.now(), interval); e != nil {
		slog.Error("job reconciliation failed", "reason", e.Error())
	} else if len(interrupted) > 0 {
		slog.Warn("jobs interrupted by restart", "count", len(interrupted))
	}
	_ = os.MkdirAll(w.Workspace, 0o700)
	// Only one job runs at a time, so anything left in the workspace is abandoned work.
	if entries, e := os.ReadDir(w.Workspace); e == nil {
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(w.Workspace, entry.Name()))
		}
	}
	schedule := time.NewTicker(w.ScheduleEvery)
	defer schedule.Stop()
	reconcile := time.NewTicker(ReconcileEvery)
	defer reconcile.Stop()
	// The bucket is the truth about what is recoverable, so it is read before
	// the first scheduling decision rather than after it.
	w.Reconcile(ctx)
	w.scheduleBackups(ctx)
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
			// New or changed storage settings: read the bucket before deciding
			// anything, so "no backup" is never confused with "not looked yet".
			if w.reconcileIfUnknown(ctx) {
				w.scheduleBackups(ctx)
			}
		case <-reconcile.C:
			w.Reconcile(ctx)
			w.scheduleBackups(ctx)
		case <-schedule.C:
			w.scheduleBackups(ctx)
		case <-time.After(time.Minute):
			if w.reconcileIfUnknown(ctx) {
				w.scheduleBackups(ctx)
			}
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		job, e := w.Store.ClaimJob(ctx, time.Now())
		if e != nil {
			slog.Error("job queue unavailable", "reason", e.Error())
			return
		}
		if job == nil {
			return
		}
		w.execute(ctx, *job)
	}
}

func (w *Worker) execute(ctx context.Context, job store.Job) {
	var result any
	var record *store.Backup
	var e error
	switch job.Kind {
	case "backup":
		result, record, e = w.runBackup(ctx, job)
	case "restore":
		result, e = w.runRestore(ctx, job)
	default:
		e = errors.New("unknown job kind")
	}
	encoded, _ := json.Marshal(result)
	if ctx.Err() != nil {
		// Shutdown: leave the row running so the next start marks it interrupted.
		return
	}
	current, _ := w.Store.Job(ctx, job.ID)
	state, stage, message := "succeeded", "done", ""
	if e != nil {
		slog.Warn("job failed", "job", job.ID, "kind", job.Kind, "stage", current.Stage, "reason", e.Error())
		state, stage, message = "failed", current.Stage, e.Error()
		record = nil
	}
	if job.Kind != "backup" {
		_ = w.Store.FinishJob(ctx, job.ID, state, stage, message, string(encoded), w.now())
		return
	}
	interval := w.Policy(ctx).Interval()
	if e := w.Store.CompleteBackupJob(ctx, job.ID, job.ProjectID, state, stage, message, string(encoded), record, job.Scheduled, w.now(), interval); e != nil {
		slog.Error("job result could not be recorded", "job", job.ID, "reason", e.Error())
		return
	}
	if state == "succeeded" {
		// Retention and the recoverable view follow every successful backup.
		w.Reconcile(ctx)
	}
}

func (w *Worker) stage(ctx context.Context, job store.Job, name string) {
	_ = w.Store.SetJobStage(ctx, job.ID, name, time.Now())
}

// scheduleBackups considers every overdue project each tick, least recently
// attempted first, and enqueues the one it can run now. Ordering by attempt
// rather than by creation is what stops one failing project from holding up
// every project behind it.
func (w *Worker) scheduleBackups(ctx context.Context) {
	if on, e := w.Store.Maintenance(ctx); e != nil || on {
		return
	}
	settings, e := w.StorageSettings(ctx)
	if e != nil {
		return
	}
	target := settings.Target()
	state, e := w.Store.StorageTarget(ctx, target)
	if e != nil || state.ReconciledAt == 0 {
		// Without a complete view of the bucket, "no backup" cannot be told from
		// "not looked yet", so scheduled work waits. Manual backup still runs.
		return
	}
	now := w.now()
	candidates, e := w.Store.BackupCandidates(ctx, target, w.InstallationID, now, w.Policy(ctx).Interval())
	if e != nil {
		slog.Error("backup schedule unavailable", "reason", e.Error())
		return
	}
	for _, p := range candidates {
		_, e := w.Store.EnqueueBackupJob(ctx, "job_"+security.Token()[:16], p.ID, `{"scheduled":true}`, true, now)
		if e != nil {
			return // busy or unavailable: the next tick reconsiders every candidate
		}
		return
	}
}

type BackupResult struct {
	BackupID        string                `json:"backup_id,omitempty"`
	ObjectKey       string                `json:"object_key,omitempty"`
	SizeBytes       int64                 `json:"size_bytes,omitempty"`
	SHA256          string                `json:"sha256,omitempty"`
	Tables          []storage.TableCount  `json:"tables,omitempty"`
	TablesTruncated bool                  `json:"tables_truncated,omitempty"`
	Objects         *storage.ObjectCounts `json:"objects,omitempty"`
}

func (w *Worker) runBackup(ctx context.Context, job store.Job) (BackupResult, *store.Backup, error) {
	var result BackupResult
	w.stage(ctx, job, "preparing")
	project, e := w.Store.Project(ctx, job.ProjectID)
	if e != nil {
		return result, nil, errors.New("the project no longer exists")
	}
	if project.Stage != "ready" || project.Failed {
		return result, nil, errors.New("the project database is not ready")
	}
	client, e := w.StorageClient(ctx)
	if e != nil {
		return result, nil, e
	}
	size, e := w.PG.DatabaseSize(ctx, project.DBName)
	if e != nil {
		return result, nil, errors.New("PostgreSQL is not reachable")
	}
	if e := w.ensureWorkspace(size); e != nil {
		return result, nil, e
	}
	work, e := os.MkdirTemp(w.Workspace, "backup-")
	if e != nil {
		return result, nil, e
	}
	defer os.RemoveAll(work)
	passfile, e := w.passfile()
	if e != nil {
		return result, nil, e
	}
	defer os.Remove(passfile)

	w.stage(ctx, job, "snapshot")
	conn, e := w.PG.Connect(ctx, project.DBName)
	if e != nil {
		return result, nil, errors.New("PostgreSQL is not reachable")
	}
	defer conn.Close(context.Background())
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return result, nil, e
	}
	defer tx.Rollback(context.Background())
	var snapshot, version string
	if e := tx.QueryRow(ctx, "SELECT pg_export_snapshot(), current_setting('server_version')").Scan(&snapshot, &version); e != nil {
		return result, nil, e
	}
	// Row counts come from the same snapshot pg_dump reads, so they are comparable after restore.
	tables, truncated, e := countTables(ctx, tx)
	if e != nil {
		return result, nil, e
	}
	objects, e := countObjects(ctx, tx)
	if e != nil {
		return result, nil, e
	}
	result.Tables, result.TablesTruncated, result.Objects = tables, truncated, &objects
	if truncated {
		slog.Warn("table list truncated for this backup", "project", project.ID, "limit", MaxTables)
	}

	w.stage(ctx, job, "dump")
	archive := filepath.Join(work, "archive.dump")
	if e := w.tool(ctx, passfile, 2*time.Hour, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--snapshot="+snapshot, "--host=postgres", "--port=5432", "--username=pgfy_mgmt", "--dbname="+project.DBName, "--file="+archive); e != nil {
		return result, nil, e
	}
	_ = tx.Commit(ctx)

	w.stage(ctx, job, "checksum")
	sum, n, e := storage.FileSHA256(archive)
	if e != nil {
		return result, nil, e
	}
	if n > MaxArchiveBytes {
		return result, nil, fmt.Errorf("the archive is %d bytes, above the %d byte limit of this release", n, MaxArchiveBytes)
	}
	result.SHA256, result.SizeBytes = sum, n

	now := w.now().UTC().Truncate(time.Second)
	base := client.BackupKey(project.DBName, now)
	settings, e := w.StorageSettings(ctx)
	if e != nil {
		return result, nil, e
	}
	// Recorded before anything is uploaded: cleanup later needs proof that an
	// abandoned archive in this bucket is ours.
	if e := w.Store.SetJobTarget(ctx, job.ID, settings.Target(), base); e != nil {
		return result, nil, e
	}
	manifest := storage.Manifest{Version: 1, InstallationID: w.InstallationID, ProjectID: project.ID, ProjectName: project.Name, DBName: project.DBName,
		PostgresVersion: version, CreatedAt: now, ArchiveKey: base + "/" + storage.ArchiveFile, ManifestKey: base + "/" + storage.ManifestFile,
		SHA256: sum, SizeBytes: n, Tables: tables, TablesTruncated: truncated, Objects: &objects}
	w.stage(ctx, job, "upload_archive")
	if e := client.UploadFile(ctx, manifest.ArchiveKey, archive); e != nil {
		return result, nil, e
	}
	w.stage(ctx, job, "upload_manifest")
	encoded, _ := json.MarshalIndent(manifest, "", " ")
	if e := client.UploadBytes(ctx, manifest.ManifestKey, encoded, "application/json"); e != nil {
		return result, nil, e
	}
	result.BackupID = "bk_" + security.Token()[:16]
	result.ObjectKey = manifest.ManifestKey
	record := store.Backup{ID: result.BackupID, ProjectID: project.ID, JobID: job.ID, ObjectKey: result.ObjectKey, Manifest: string(encoded), SizeBytes: n, CreatedAt: now.Unix()}
	return result, &record, nil
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Verification states. "partial" is the honest answer when every comparison
// passed but the backup could not support the full set of them.
const (
	VerificationVerified = "verified"
	VerificationPartial  = "partial"
	VerificationFailed   = "failed"
)

type RestoreResult struct {
	ProjectID     string  `json:"project_id,omitempty"`
	Verification  string  `json:"verification"`
	Verified      bool    `json:"verified"`
	Summary       string  `json:"summary,omitempty"`
	RestoreErrors int     `json:"restore_errors,omitempty"`
	Stderr        string  `json:"stderr,omitempty"`
	Checks        []Check `json:"checks,omitempty"`
}

type restoreInput struct {
	ManifestKey string `json:"manifest_key"`
	Name        string `json:"name"`
	ProjectID   string `json:"project_id"`
}

func (w *Worker) runRestore(ctx context.Context, job store.Job) (RestoreResult, error) {
	var result RestoreResult
	var in restoreInput
	if e := json.Unmarshal([]byte(job.Input), &in); e != nil {
		return result, e
	}
	result.ProjectID = in.ProjectID
	w.stage(ctx, job, "preparing")
	client, e := w.StorageClient(ctx)
	if e != nil {
		return result, e
	}
	manifest, e := client.Manifest(ctx, in.ManifestKey)
	if e != nil {
		return result, e
	}
	var version string
	if e := w.PG.Pool.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&version); e != nil {
		return result, errors.New("PostgreSQL is not reachable")
	}
	if major(manifest.PostgresVersion) != major(version) {
		return result, fmt.Errorf("the backup was made with PostgreSQL %s and this server runs %s; restoring across major versions is not supported", manifest.PostgresVersion, version)
	}
	if manifest.SizeBytes > MaxArchiveBytes {
		return result, errors.New("the archive is above the size limit of this release")
	}
	if e := w.ensureWorkspace(manifest.SizeBytes); e != nil {
		return result, e
	}
	work, e := os.MkdirTemp(w.Workspace, "restore-")
	if e != nil {
		return result, e
	}
	defer os.RemoveAll(work)

	w.stage(ctx, job, "download")
	archive := filepath.Join(work, "archive.dump")
	if e := client.DownloadFile(ctx, manifest.ArchiveKey, archive); e != nil {
		return result, e
	}
	w.stage(ctx, job, "verify_archive")
	sum, n, e := storage.FileSHA256(archive)
	if e != nil {
		return result, e
	}
	if sum != manifest.SHA256 || n != manifest.SizeBytes {
		return result, errors.New("the downloaded archive does not match the manifest checksum; it will not be restored")
	}

	w.stage(ctx, job, "create_project")
	if e := w.Provisioner.ProvisionNow(ctx, in.ProjectID); e != nil {
		return result, fmt.Errorf("the target project could not be created: %w", e)
	}
	project, e := w.Store.Project(ctx, in.ProjectID)
	if e != nil {
		return result, e
	}
	passfile, e := w.passfile()
	if e != nil {
		return result, e
	}
	defer os.Remove(passfile)

	w.stage(ctx, job, "restore")
	// Objects are created as the new project role, never as the management or bootstrap role.
	run := w.run(ctx, passfile, 2*time.Hour, "pg_restore", "--no-owner", "--no-privileges", "--role="+project.RoleName, "--host=postgres", "--port=5432", "--username=pgfy_mgmt", "--dbname="+project.DBName, archive)
	if run.Err != nil {
		// The restore never ran, or was stopped: nothing about it can be claimed.
		return result, run.Err
	}
	result.RestoreErrors, result.Stderr = run.Errors, run.Excerpt

	w.stage(ctx, job, "verify")
	conn, e := w.PG.Connect(ctx, project.DBName)
	if e != nil {
		return result, errors.New("PostgreSQL is not reachable for verification")
	}
	defer conn.Close(context.Background())
	checks, state := verifyRestore(ctx, conn, manifest, project.RoleName)
	result.Checks = checks
	// pg_restore can ignore errors and still exit zero, so the count decides too.
	reported := run.Exit != 0 || run.Errors > 0
	if reported {
		state = VerificationFailed
	}
	result.Verification, result.Verified = state, state == VerificationVerified
	switch {
	case reported:
		result.Summary = fmt.Sprintf("Completed with %d restore errors (%s). The database exists and can be inspected, but it is not verified.", run.Errors, run.Counted)
	case state == VerificationPartial:
		result.Summary = "Restored. Every check that this backup supports passed, but it does not carry the full set of baselines."
	case state == VerificationFailed:
		result.Summary = "Restored, but verification found differences. Review the checks before relying on this database."
	default:
		result.Summary = "Restored and verified against the baselines recorded at backup time."
	}
	// The database exists either way, so the job reports what happened rather
	// than hiding a usable database behind a failed job.
	return result, nil
}

func verifyRestore(ctx context.Context, conn *pgx.Conn, manifest storage.Manifest, role string) ([]Check, string) {
	checks := []Check{}
	ok, full := true, true
	var count int64
	for _, t := range manifest.Tables {
		e := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t.Schema, t.Name}.Sanitize()).Scan(&count)
		check := Check{Name: fmt.Sprintf("rows in %s.%s", t.Schema, t.Name), OK: e == nil && count == t.Rows}
		if e != nil {
			check.Detail = "table missing after restore"
		} else {
			check.Detail = fmt.Sprintf("%d restored, %d in backup", count, t.Rows)
		}
		ok = ok && check.OK
		checks = append(checks, check)
	}
	if len(manifest.Tables) == 0 {
		checks = append(checks, Check{Name: "schema", OK: true, Detail: "the backup contained no user tables"})
	}
	if manifest.TablesTruncated {
		full = false
		checks = append(checks, Check{Name: "table coverage", OK: true, Detail: fmt.Sprintf("row counts cover the first %d tables recorded by this backup", len(manifest.Tables))})
	}
	if manifest.Objects == nil {
		full = false
		checks = append(checks, Check{Name: "database objects", OK: true, Detail: "this backup predates object counts, so only tables could be compared"})
	} else {
		counts, e := restoredObjects(ctx, conn)
		for _, c := range []struct {
			name           string
			backup, actual int64
		}{
			{"sequences", manifest.Objects.Sequences, counts.Sequences},
			{"views", manifest.Objects.Views, counts.Views},
			{"functions", manifest.Objects.Functions, counts.Functions},
			{"indexes", manifest.Objects.Indexes, counts.Indexes},
			{"constraints", manifest.Objects.Constraints, counts.Constraints},
		} {
			check := Check{Name: c.name, OK: e == nil && c.actual == c.backup, Detail: fmt.Sprintf("%d restored, %d in backup", c.actual, c.backup)}
			if e != nil {
				check.Detail = "the count could not be read after restore"
			}
			ok = ok && check.OK
			checks = append(checks, check)
		}
	}
	var foreign int
	e := conn.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') AND tableowner<>$1", role).Scan(&foreign)
	owner := Check{Name: "ownership", OK: e == nil && foreign == 0, Detail: "all tables belong to the new project user"}
	if e != nil {
		owner.Detail = "ownership could not be checked"
	} else if foreign > 0 {
		owner.Detail = fmt.Sprintf("%d tables belong to another role", foreign)
	}
	ok = ok && owner.OK
	checks = append(checks, owner)
	var canConnect bool
	e = conn.QueryRow(ctx, "SELECT has_database_privilege($1, current_database(), 'CONNECT')", role).Scan(&canConnect)
	perm := Check{Name: "permissions", OK: e == nil && canConnect, Detail: "the new project user can connect with its own credentials"}
	if !perm.OK {
		perm.Detail = "the new project user cannot connect"
	}
	ok = ok && perm.OK
	checks = append(checks, perm)
	switch {
	case !ok:
		return checks, VerificationFailed
	case !full:
		return checks, VerificationPartial
	}
	return checks, VerificationVerified
}

// restoredObjects counts what the restored database actually contains, to
// compare against the baselines the backup recorded.
func restoredObjects(ctx context.Context, conn *pgx.Conn) (storage.ObjectCounts, error) {
	var c storage.ObjectCounts
	e := conn.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='S' AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%'),
		(SELECT count(*) FROM pg_views WHERE schemaname NOT IN ('pg_catalog','information_schema')),
		(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema')),
		(SELECT count(*) FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog','information_schema')),
		(SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema'))`).
		Scan(&c.Sequences, &c.Views, &c.Functions, &c.Indexes, &c.Constraints)
	return c, e
}

// countTables records one row count per table from the dump's own snapshot.
// The extra row asked for beyond the limit is how truncation is detected: a
// backup with exactly the limit is not necessarily truncated.
func countTables(ctx context.Context, tx pgx.Tx) ([]storage.TableCount, bool, error) {
	rows, e := tx.Query(ctx, "SELECT schemaname, tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY 1,2 LIMIT $1", MaxTables+1)
	if e != nil {
		return nil, false, e
	}
	var tables []storage.TableCount
	for rows.Next() {
		var t storage.TableCount
		if e := rows.Scan(&t.Schema, &t.Name); e != nil {
			rows.Close()
			return nil, false, e
		}
		tables = append(tables, t)
	}
	rows.Close()
	if e := rows.Err(); e != nil {
		return nil, false, e
	}
	truncated := len(tables) > MaxTables
	if truncated {
		tables = tables[:MaxTables]
	}
	for i := range tables {
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{tables[i].Schema, tables[i].Name}.Sanitize()).Scan(&tables[i].Rows); e != nil {
			return nil, false, e
		}
	}
	if tables == nil {
		tables = []storage.TableCount{}
	}
	return tables, truncated, nil
}

// countObjects records what else the database contains, from the same snapshot,
// so a restore can show that more than row counts came back.
func countObjects(ctx context.Context, tx pgx.Tx) (storage.ObjectCounts, error) {
	var c storage.ObjectCounts
	e := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='S' AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%'),
		(SELECT count(*) FROM pg_views WHERE schemaname NOT IN ('pg_catalog','information_schema')),
		(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema')),
		(SELECT count(*) FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog','information_schema')),
		(SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema'))`).
		Scan(&c.Sequences, &c.Views, &c.Functions, &c.Indexes, &c.Constraints)
	return c, e
}

// passfile writes the management credential where only this process reads it,
// so the password never appears in arguments or the environment.
func (w *Worker) passfile() (string, error) {
	f, e := os.CreateTemp("", "pgfy-pgpass-")
	if e != nil {
		return "", e
	}
	defer f.Close()
	if e := f.Chmod(0o600); e != nil {
		return "", e
	}
	_, e = fmt.Fprintf(f, "postgres:5432:*:pgfy_mgmt:%s\n", w.PG.Password())
	return f.Name(), e
}

func (w *Worker) tool(ctx context.Context, passfile string, timeout time.Duration, name string, args ...string) error {
	run := w.run(ctx, passfile, timeout, name, args...)
	if run.Err != nil {
		return run.Err
	}
	if run.Exit != 0 {
		return fmt.Errorf("%s failed: %s", name, run.LastLine)
	}
	return nil
}

// toolRun separates "the tool ran and complained" from "the tool never ran".
// Only the first can leave a usable database behind, so only the first is
// reported as a result rather than as a failure.
type toolRun struct {
	Exit     int
	Errors   int    // counted over the whole stream, never over the excerpt
	Counted  string // where the count came from: pg_restore's summary or our own tally
	Excerpt  string
	LastLine string
	Err      error
}

func (w *Worker) run(ctx context.Context, passfile string, timeout time.Duration, name string, args ...string) toolRun {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PGPASSFILE=" + passfile, "PATH=/usr/local/bin:/usr/bin:/bin", "PGCONNECT_TIMEOUT=10"}
	pipe, e := cmd.StderrPipe()
	if e != nil {
		return toolRun{Err: fmt.Errorf("%s could not be started", name)}
	}
	if e := cmd.Start(); e != nil {
		return toolRun{Err: fmt.Errorf("%s could not be started", name)}
	}
	run := readToolErrors(pipe)
	waitErr := cmd.Wait()
	if waitErr == nil {
		return run
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			// Killed rather than finished: nothing about the result is trustworthy.
			run.Err = fmt.Errorf("%s was stopped before it finished (%s)", name, status.Signal())
			return run
		}
		if ctx.Err() != nil {
			run.Err = fmt.Errorf("%s did not finish within its time limit", name)
			return run
		}
		run.Exit = exit.ExitCode()
		return run
	}
	run.Err = fmt.Errorf("%s failed: %s", name, run.LastLine)
	return run
}

// readToolErrors reads the entire stream so the error count is complete, while
// keeping only a bounded excerpt for display.
func readToolErrors(r io.Reader) toolRun {
	run := toolRun{Counted: "counted from the output"}
	var excerpt strings.Builder
	limited := &limitedWriter{b: &excerpt, limit: 4096}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	summary := regexp.MustCompile(`errors ignored on restore: (\d+)`)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			run.LastLine = line
		}
		if strings.Contains(line, ": error:") {
			run.Errors++
		}
		if m := summary.FindStringSubmatch(line); m != nil {
			if n, e := strconv.Atoi(m[1]); e == nil {
				run.Errors, run.Counted = n, "reported by the tool"
			}
		}
		_, _ = limited.Write([]byte(line + "\n"))
	}
	run.Excerpt = strings.TrimSpace(excerpt.String())
	if len(run.LastLine) > 300 {
		run.LastLine = run.LastLine[:300]
	}
	return run
}

type limitedWriter struct {
	b     *strings.Builder
	limit int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if remaining := l.limit - l.b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		l.b.Write(p)
	}
	return len(p), nil
}

// ensureWorkspace refuses to start work that would fill the disk.
func (w *Worker) ensureWorkspace(needed int64) error {
	var fs syscall.Statfs_t
	if e := syscall.Statfs(w.Workspace, &fs); e != nil {
		return errors.New("the backup workspace is unavailable")
	}
	free := int64(fs.Bavail) * int64(fs.Bsize)
	if free < needed*2+256<<20 {
		return fmt.Errorf("not enough free disk for this job: %d MB free, about %d MB needed", free>>20, (needed*2+256<<20)>>20)
	}
	return nil
}

func major(version string) string {
	if i := strings.IndexAny(version, ". "); i > 0 {
		return version[:i]
	}
	return version
}
