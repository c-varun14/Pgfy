package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJobQueueSingleHeavyJobAndInterruption(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	p, _, _ := s.CreateProject(ctx, testProject("aaa000000001"), "key-1", "sealed", now)
	j, e := s.EnqueueJob(ctx, "job_1", "backup", p.ID, "{}", now)
	if e != nil || j.State != "queued" {
		t.Fatal(j, e)
	}
	if _, e = s.EnqueueJob(ctx, "job_2", "backup", p.ID, "{}", now); !errors.Is(e, ErrJobBusy) {
		t.Fatal("second heavy job accepted", e)
	}
	claimed, e := s.ClaimJob(ctx, now)
	if e != nil || claimed == nil || claimed.ID != "job_1" || claimed.State != "running" {
		t.Fatal(claimed, e)
	}
	if again, _ := s.ClaimJob(ctx, now); again != nil {
		t.Fatal("running job claimed twice")
	}
	if e = s.SetJobStage(ctx, "job_1", "dump", now); e != nil {
		t.Fatal(e)
	}
	interrupted, e := s.InterruptRunningJobs(ctx, now, 24*time.Hour)
	if e != nil || len(interrupted) != 1 {
		t.Fatal(interrupted, e)
	}
	got, _ := s.Job(ctx, "job_1")
	if got.State != "interrupted" || got.Stage != "dump" || got.Error == "" {
		t.Fatal(got)
	}
	// Once interrupted the queue is free again and history keeps the failure.
	if _, e = s.EnqueueJob(ctx, "job_2", "backup", p.ID, "{}", now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	history, _ := s.ProjectJobs(ctx, p.ID, 10)
	if len(history) != 2 || history[0].ID != "job_2" {
		t.Fatal(history)
	}
	if e = s.RecordBackup(ctx, Backup{ID: "bk_1", ProjectID: p.ID, JobID: "job_2", ObjectKey: "k", Manifest: "{}", SizeBytes: 10, CreatedAt: now.Unix()}); e != nil {
		t.Fatal(e)
	}
	if e = s.SetSetting(ctx, "storage", "sealed-1", now); e != nil {
		t.Fatal(e)
	}
	if e = s.SetSetting(ctx, "storage", "sealed-2", now); e != nil {
		t.Fatal(e)
	}
	if v, _ := s.Setting(ctx, "storage"); v != "sealed-2" {
		t.Fatal(v)
	}
	if v, e := s.Setting(ctx, "missing"); v != "" || e != nil {
		t.Fatal(v, e)
	}
}
