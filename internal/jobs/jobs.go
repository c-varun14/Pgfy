// Package jobs runs the heavy, durable work (backups, restores, the daily
// schedule) in one goroutine, independent of browser sessions. Every stage is
// persisted so an interrupted job is reported as interrupted, never as done.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	ScheduleEvery   = 24 * time.Hour
)

var ErrStorageNotConfigured = errors.New("backup storage is not configured")

type Worker struct {
	Store          *store.Store
	Vault          *security.Vault
	PG             *postgres.Management
	Provisioner    *provision.Provisioner
	InstallationID string
	Workspace      string
	kick           chan struct{}
}

func New(s *store.Store, v *security.Vault, pg *postgres.Management, p *provision.Provisioner, installationID, workspace string) *Worker {
	return &Worker{Store: s, Vault: v, PG: pg, Provisioner: p, InstallationID: installationID, Workspace: workspace, kick: make(chan struct{}, 1)}
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
	if n, e := w.Store.InterruptRunningJobs(ctx, time.Now()); e != nil {
		slog.Error("job reconciliation failed", "reason", e.Error())
	} else if n > 0 {
		slog.Warn("jobs interrupted by restart", "count", n)
	}
	_ = os.MkdirAll(w.Workspace, 0o700)
	// Only one job runs at a time, so anything left in the workspace is abandoned work.
	if entries, e := os.ReadDir(w.Workspace); e == nil {
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(w.Workspace, entry.Name()))
		}
	}
	schedule := time.NewTicker(5 * time.Minute)
	defer schedule.Stop()
	w.scheduleBackups(ctx)
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-schedule.C:
			w.scheduleBackups(ctx)
		case <-time.After(time.Minute):
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
	var e error
	switch job.Kind {
	case "backup":
		result, e = w.runBackup(ctx, job)
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
	if e != nil {
		slog.Warn("job failed", "job", job.ID, "kind", job.Kind, "stage", current.Stage, "reason", e.Error())
		_ = w.Store.FinishJob(ctx, job.ID, "failed", current.Stage, e.Error(), string(encoded), time.Now())
		return
	}
	_ = w.Store.FinishJob(ctx, job.ID, "succeeded", "done", "", string(encoded), time.Now())
}

func (w *Worker) stage(ctx context.Context, job store.Job, name string) {
	_ = w.Store.SetJobStage(ctx, job.ID, name, time.Now())
}

// scheduleBackups enqueues one overdue project per tick once storage exists;
// the queue's one-at-a-time rule spaces the rest out naturally.
func (w *Worker) scheduleBackups(ctx context.Context) {
	if _, e := w.StorageSettings(ctx); e != nil {
		return
	}
	projects, e := w.Store.Projects(ctx)
	if e != nil {
		return
	}
	last, e := w.Store.LastBackupAt(ctx)
	if e != nil {
		return
	}
	now := time.Now()
	for _, p := range projects {
		if p.Stage != "ready" || p.Failed {
			continue
		}
		if at, ok := last[p.ID]; ok && now.Sub(time.Unix(at, 0)) < ScheduleEvery {
			continue
		}
		if _, e := w.Store.EnqueueJob(ctx, "job_"+security.Token()[:16], "backup", p.ID, `{"scheduled":true}`, now); e != nil {
			return // busy: try again next tick
		}
		return
	}
}

type BackupResult struct {
	BackupID  string               `json:"backup_id,omitempty"`
	ObjectKey string               `json:"object_key,omitempty"`
	SizeBytes int64                `json:"size_bytes,omitempty"`
	SHA256    string               `json:"sha256,omitempty"`
	Tables    []storage.TableCount `json:"tables,omitempty"`
}

func (w *Worker) runBackup(ctx context.Context, job store.Job) (BackupResult, error) {
	var result BackupResult
	w.stage(ctx, job, "preparing")
	project, e := w.Store.Project(ctx, job.ProjectID)
	if e != nil {
		return result, errors.New("the project no longer exists")
	}
	if project.Stage != "ready" || project.Failed {
		return result, errors.New("the project database is not ready")
	}
	client, e := w.StorageClient(ctx)
	if e != nil {
		return result, e
	}
	size, e := w.PG.DatabaseSize(ctx, project.DBName)
	if e != nil {
		return result, errors.New("PostgreSQL is not reachable")
	}
	if e := w.ensureWorkspace(size); e != nil {
		return result, e
	}
	work, e := os.MkdirTemp(w.Workspace, "backup-")
	if e != nil {
		return result, e
	}
	defer os.RemoveAll(work)
	passfile, e := w.passfile()
	if e != nil {
		return result, e
	}
	defer os.Remove(passfile)

	w.stage(ctx, job, "snapshot")
	conn, e := w.PG.Connect(ctx, project.DBName)
	if e != nil {
		return result, errors.New("PostgreSQL is not reachable")
	}
	defer conn.Close(context.Background())
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return result, e
	}
	defer tx.Rollback(context.Background())
	var snapshot, version string
	if e := tx.QueryRow(ctx, "SELECT pg_export_snapshot(), current_setting('server_version')").Scan(&snapshot, &version); e != nil {
		return result, e
	}
	// Row counts come from the same snapshot pg_dump reads, so they are comparable after restore.
	tables, e := countTables(ctx, tx)
	if e != nil {
		return result, e
	}
	result.Tables = tables

	w.stage(ctx, job, "dump")
	archive := filepath.Join(work, "archive.dump")
	if e := w.tool(ctx, passfile, 2*time.Hour, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--snapshot="+snapshot, "--host=postgres", "--port=5432", "--username=pgfy_mgmt", "--dbname="+project.DBName, "--file="+archive); e != nil {
		return result, e
	}
	_ = tx.Commit(ctx)

	w.stage(ctx, job, "checksum")
	sum, n, e := storage.FileSHA256(archive)
	if e != nil {
		return result, e
	}
	if n > MaxArchiveBytes {
		return result, fmt.Errorf("the archive is %d bytes, above the %d byte limit of this release", n, MaxArchiveBytes)
	}
	result.SHA256, result.SizeBytes = sum, n

	now := time.Now()
	base := client.BackupKey(project.DBName, now)
	manifest := storage.Manifest{Version: 1, InstallationID: w.InstallationID, ProjectID: project.ID, ProjectName: project.Name, DBName: project.DBName, PostgresVersion: version, CreatedAt: now.UTC(), ArchiveKey: base + "/archive.dump", SHA256: sum, SizeBytes: n, Tables: tables}
	w.stage(ctx, job, "upload_archive")
	if e := client.UploadFile(ctx, manifest.ArchiveKey, archive); e != nil {
		return result, e
	}
	w.stage(ctx, job, "upload_manifest")
	encoded, _ := json.MarshalIndent(manifest, "", " ")
	if e := client.UploadBytes(ctx, base+"/manifest.json", encoded, "application/json"); e != nil {
		return result, e
	}
	result.BackupID = "bk_" + security.Token()[:16]
	result.ObjectKey = base + "/manifest.json"
	if e := w.Store.RecordBackup(ctx, store.Backup{ID: result.BackupID, ProjectID: project.ID, JobID: job.ID, ObjectKey: result.ObjectKey, Manifest: string(encoded), SizeBytes: n, CreatedAt: now.Unix()}); e != nil {
		return result, e
	}
	return result, nil
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type RestoreResult struct {
	ProjectID string  `json:"project_id,omitempty"`
	Verified  bool    `json:"verified"`
	Checks    []Check `json:"checks,omitempty"`
	Warnings  string  `json:"warnings,omitempty"`
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
	restoreErr := w.tool(ctx, passfile, 2*time.Hour, "pg_restore", "--no-owner", "--no-privileges", "--role="+project.RoleName, "--host=postgres", "--port=5432", "--username=pgfy_mgmt", "--dbname="+project.DBName, archive)
	if restoreErr != nil {
		// pg_restore exits non-zero for warnings too; verification below decides.
		result.Warnings = restoreErr.Error()
	}

	w.stage(ctx, job, "verify")
	conn, e := w.PG.Connect(ctx, project.DBName)
	if e != nil {
		return result, errors.New("PostgreSQL is not reachable for verification")
	}
	defer conn.Close(context.Background())
	result.Checks, result.Verified = verifyRestore(ctx, conn, manifest, project.RoleName)
	if !result.Verified {
		return result, errors.New("the restore finished but verification found differences; review the checks before relying on this database")
	}
	return result, nil
}

func verifyRestore(ctx context.Context, conn *pgx.Conn, manifest storage.Manifest, role string) ([]Check, bool) {
	checks := []Check{}
	ok := true
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
	return append(checks, perm), ok
}

func countTables(ctx context.Context, tx pgx.Tx) ([]storage.TableCount, error) {
	rows, e := tx.Query(ctx, "SELECT schemaname, tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY 1,2 LIMIT 500")
	if e != nil {
		return nil, e
	}
	var tables []storage.TableCount
	for rows.Next() {
		var t storage.TableCount
		if e := rows.Scan(&t.Schema, &t.Name); e != nil {
			rows.Close()
			return nil, e
		}
		tables = append(tables, t)
	}
	rows.Close()
	if e := rows.Err(); e != nil {
		return nil, e
	}
	for i := range tables {
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{tables[i].Schema, tables[i].Name}.Sanitize()).Scan(&tables[i].Rows); e != nil {
			return nil, e
		}
	}
	if tables == nil {
		tables = []storage.TableCount{}
	}
	return tables, nil
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PGPASSFILE=" + passfile, "PATH=/usr/local/bin:/usr/bin:/bin", "PGCONNECT_TIMEOUT=10"}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{b: &stderr, limit: 4096}
	if e := cmd.Run(); e != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		last := lines[len(lines)-1]
		if len(last) > 300 {
			last = last[:300]
		}
		return fmt.Errorf("%s failed: %s", name, last)
	}
	return nil
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
