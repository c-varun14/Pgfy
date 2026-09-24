package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceHoldsJobsAndWritesNothing(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	p := ready(t, s, "prj_a", now)
	queued, e := s.EnqueueJob(ctx, "job_queued", "restore", "", "{}", now)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetMaintenance(ctx, true); e != nil {
		t.Fatal(e)
	}
	if on, _ := s.Maintenance(ctx); !on {
		t.Fatal("maintenance not reported")
	}
	if job, e := s.ClaimJob(ctx, now); e != nil || job != nil {
		t.Fatalf("a job started during maintenance: %v %v", job, e)
	}
	if _, e = s.EnqueueBackupJob(ctx, "job_b", p.ID, "{}", false, now); !errors.Is(e, ErrMaintenance) {
		t.Fatalf("enqueue during maintenance: %v", e)
	}
	var attempts int
	if e = s.DB.QueryRow("SELECT count(*) FROM backup_schedule WHERE project_id=? AND last_attempt_at IS NOT NULL", p.ID).Scan(&attempts); e != nil || attempts != 0 {
		t.Fatalf("an attempt was recorded during maintenance: %d %v", attempts, e)
	}
	if n, _ := s.RunningJobs(ctx); n != 0 {
		t.Fatal("running count", n)
	}
	if e = s.SetMaintenance(ctx, false); e != nil {
		t.Fatal(e)
	}
	job, e := s.ClaimJob(ctx, now)
	if e != nil || job == nil || job.ID != queued.ID {
		t.Fatalf("queued job did not resume: %v %v", job, e)
	}
	if n, _ := s.RunningJobs(ctx); n != 1 {
		t.Fatal("running count", n)
	}
}

func TestSnapshotAndRestoreIgnoreSchemaAndDiscardWAL(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	ready(t, s, "prj_kept", time.Unix(1_800_000_000, 0))
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if e := Snapshot(ctx, path, snapshot); e != nil {
		t.Fatal(e)
	}
	// A newer release migrates and writes; its frames sit in the WAL.
	if _, e := s.DB.Exec("INSERT INTO schema_migrations(name,checksum) VALUES ('999_future.sql','x'); CREATE TABLE future(x)"); e != nil {
		t.Fatal(e)
	}
	ready(t, s, "prj_later", time.Unix(1_800_000_100, 0))
	if _, e := os.Stat(path + "-wal"); e != nil {
		t.Fatal("expected un-checkpointed WAL frames", e)
	}
	// An unknown migration would make Open refuse; snapshot must not migrate.
	if e := Snapshot(ctx, path, filepath.Join(t.TempDir(), "again.db")); e != nil {
		t.Fatal("snapshot of a newer schema:", e)
	}
	// Simulate the application having stopped without a checkpoint: copy the
	// database and its WAL so the restore target carries stale frames.
	stopped := filepath.Join(t.TempDir(), "pgfy.db")
	for _, suffix := range []string{"", "-wal"} {
		b, e := os.ReadFile(path + suffix)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(stopped+suffix, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := Restore(ctx, snapshot, stopped); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(stopped + "-wal"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("stale WAL survived the restore")
	}
	if _, e := os.Stat(snapshot); e != nil {
		t.Fatal("snapshot must be kept until the rollback succeeds")
	}
	restored, e := Open(stopped)
	if e != nil {
		t.Fatal("restored database does not open:", e)
	}
	defer restored.DB.Close()
	var n int
	restored.DB.QueryRow("SELECT count(*) FROM projects").Scan(&n)
	var future int
	restored.DB.QueryRow("SELECT count(*) FROM schema_migrations WHERE name='999_future.sql'").Scan(&future)
	if n != 1 || future != 0 {
		t.Fatalf("restore did not return the snapshot state: projects=%d future=%d", n, future)
	}
}

func TestSnapshotCheckRejectsMissingAndCorrupt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if e := CheckSnapshot(ctx, filepath.Join(dir, "missing.db")); e == nil {
		t.Fatal("missing snapshot accepted")
	}
	bad := filepath.Join(dir, "bad.db")
	os.WriteFile(bad, []byte("not a database at all, just text that is long enough"), 0600)
	if e := CheckSnapshot(ctx, bad); e == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	target := filepath.Join(dir, "target.db")
	os.WriteFile(target, []byte("original"), 0600)
	if e := Restore(ctx, bad, target); e == nil {
		t.Fatal("restore accepted a corrupt snapshot")
	}
	if b, _ := os.ReadFile(target); string(b) != "original" {
		t.Fatal("a refused restore changed the database")
	}
}
