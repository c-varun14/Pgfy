package store

import (
	"context"
	"testing"
	"time"
)

func ready(t *testing.T, s *Store, id string, created time.Time) Project {
	t.Helper()
	p, _, e := s.CreateProject(context.Background(), testProject(id), "key-"+id, "sealed", created)
	if e != nil {
		t.Fatal(e)
	}
	if e := s.SetProjectStage(context.Background(), p.ID, "ready", created); e != nil {
		t.Fatal(e)
	}
	p.Stage, p.ReadyAt = "ready", created.Unix()
	return p
}

func recoverable(t *testing.T, s *Store, target, installation string, p Project, at time.Time) {
	t.Helper()
	key := "pgfy/backups/" + p.DBName + "/" + at.UTC().Format("20060102T150405Z") + "/manifest.json"
	state := PrefixState{DBName: p.DBName, Complete: 1}
	backup := BucketBackup{ManifestKey: key, ArchiveKey: key[:len(key)-len("manifest.json")] + "archive.dump",
		DBName: p.DBName, TakenAt: at.Unix(), State: BackupComplete, InstallationID: installation, ProjectID: p.ID}
	if e := s.ReplacePrefix(context.Background(), target, state, []BucketBackup{backup}, at); e != nil {
		t.Fatal(e)
	}
}

func ids(projects []Project) []string {
	out := []string{}
	for _, p := range projects {
		out = append(out, p.ID)
	}
	return out
}

// A project whose backup keeps failing must not hold up the projects behind it:
// candidates are ordered by attempt, not by creation, and a failure backs off.
func TestBackupCandidatesRotateAndBackOff(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	day := 24 * time.Hour
	old := ready(t, s, "aaa000000001", start)
	young := ready(t, s, "bbb000000002", start.Add(time.Minute))
	now := start.Add(2 * day)

	got, e := s.BackupCandidates(ctx, "target", "install", now, day)
	if e != nil || len(got) != 2 || got[0].ID != old.ID {
		t.Fatal(ids(got), e)
	}
	// The older project is attempted first and fails.
	if _, e := s.EnqueueBackupJob(ctx, "job_1", old.ID, "{}", true, now); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteBackupJob(ctx, "job_1", old.ID, "failed", "dump", "pg_dump failed", "{}", nil, true, now, day); e != nil {
		t.Fatal(e)
	}
	got, _ = s.BackupCandidates(ctx, "target", "install", now.Add(time.Minute), day)
	if len(got) != 1 || got[0].ID != young.ID {
		t.Fatal("the failing project still blocks the queue", ids(got))
	}
	// Backoff steps: 15 minutes, an hour, four hours, then the target interval.
	schedule, _ := s.BackupSchedule(ctx, old.ID)
	if schedule.Failures != 1 || schedule.NextAttemptAt != now.Add(15*time.Minute).Unix() {
		t.Fatal(schedule)
	}
	for _, step := range []time.Duration{time.Hour, 4 * time.Hour, day} {
		at := time.Unix(schedule.NextAttemptAt, 0)
		if _, e := s.EnqueueBackupJob(ctx, "job_"+step.String(), old.ID, "{}", true, at); e != nil {
			t.Fatal(e)
		}
		if e := s.CompleteBackupJob(ctx, "job_"+step.String(), old.ID, "failed", "dump", "again", "{}", nil, true, at, day); e != nil {
			t.Fatal(e)
		}
		schedule, _ = s.BackupSchedule(ctx, old.ID)
		if schedule.NextAttemptAt != at.Add(step).Unix() {
			t.Fatalf("expected a %s backoff, got %d", step, schedule.NextAttemptAt-at.Unix())
		}
	}
	// A success clears the backoff and the backup becomes the newest recoverable one.
	at := time.Unix(schedule.NextAttemptAt, 0)
	if _, e := s.EnqueueBackupJob(ctx, "job_ok", old.ID, "{}", true, at); e != nil {
		t.Fatal(e)
	}
	record := Backup{ID: "bk_1", ProjectID: old.ID, JobID: "job_ok", ObjectKey: "pgfy/backups/" + old.DBName + "/x/manifest.json", Manifest: "{}", CreatedAt: at.Unix()}
	if e := s.CompleteBackupJob(ctx, "job_ok", old.ID, "succeeded", "done", "", "{}", &record, true, at, day); e != nil {
		t.Fatal(e)
	}
	if schedule, _ = s.BackupSchedule(ctx, old.ID); schedule.Failures != 0 || schedule.NextAttemptAt != 0 {
		t.Fatal(schedule)
	}
	recoverable(t, s, "target", "install", old, at)
	got, _ = s.BackupCandidates(ctx, "target", "install", at.Add(time.Hour), day)
	if len(got) != 1 || got[0].ID != young.ID {
		t.Fatal("a fresh backup should stop the project being due", ids(got))
	}
}

// Another installation's backup, or one for a different database, must never
// satisfy this project's schedule.
func TestBackupCandidatesIgnoreForeignBackups(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	p := ready(t, s, "aaa000000001", start)
	now := start.Add(48 * time.Hour)
	recoverable(t, s, "target", "somebody-else", p, now.Add(-time.Minute))
	got, _ := s.BackupCandidates(ctx, "target", "install", now, 24*time.Hour)
	if len(got) != 1 {
		t.Fatal("a foreign backup satisfied a local schedule", ids(got))
	}
	// The same bucket under a different store is also not this store's answer.
	recoverable(t, s, "other-target", "install", p, now.Add(-time.Minute))
	if got, _ = s.BackupCandidates(ctx, "target", "install", now, 24*time.Hour); len(got) != 1 {
		t.Fatal("another store's backup satisfied this one", ids(got))
	}
	recoverable(t, s, "target", "install", p, now.Add(-time.Minute))
	if got, _ = s.BackupCandidates(ctx, "target", "install", now, 24*time.Hour); len(got) != 0 {
		t.Fatal("this installation's own backup was ignored", ids(got))
	}
}

// A manual attempt can only help: success clears a backoff, and failure never
// pushes the automatic schedule further out.
func TestManualBackupDoesNotExtendBackoff(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	p := ready(t, s, "aaa000000001", start)
	now := start.Add(48 * time.Hour)
	if _, e := s.EnqueueBackupJob(ctx, "job_1", p.ID, "{}", true, now); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteBackupJob(ctx, "job_1", p.ID, "failed", "dump", "no", "{}", nil, true, now, 24*time.Hour); e != nil {
		t.Fatal(e)
	}
	scheduled, _ := s.BackupSchedule(ctx, p.ID)
	manualAt := now.Add(time.Minute)
	if _, e := s.EnqueueBackupJob(ctx, "job_2", p.ID, "{}", false, manualAt); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteBackupJob(ctx, "job_2", p.ID, "failed", "dump", "no", "{}", nil, false, manualAt, 24*time.Hour); e != nil {
		t.Fatal(e)
	}
	after, _ := s.BackupSchedule(ctx, p.ID)
	if after.Failures != scheduled.Failures || after.NextAttemptAt != scheduled.NextAttemptAt {
		t.Fatal("a manual failure changed the automatic schedule", scheduled, after)
	}
	if after.LastAttemptAt != manualAt.Unix() {
		t.Fatal("the attempt was not recorded for fairness ordering", after)
	}
	record := Backup{ID: "bk_1", ProjectID: p.ID, JobID: "job_3", ObjectKey: "k", Manifest: "{}", CreatedAt: manualAt.Unix()}
	if _, e := s.EnqueueBackupJob(ctx, "job_3", p.ID, "{}", false, manualAt); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteBackupJob(ctx, "job_3", p.ID, "succeeded", "done", "", "{}", &record, false, manualAt, 24*time.Hour); e != nil {
		t.Fatal(e)
	}
	if after, _ = s.BackupSchedule(ctx, p.ID); after.Failures != 0 || after.NextAttemptAt != 0 {
		t.Fatal("a manual success did not clear the backoff", after)
	}
}

// An attempt must never exist without its scheduling state, and an interrupted
// scheduled backup counts as a failure so a crash loop backs off.
func TestEnqueueRecordsAttemptAndInterruptionBacksOff(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	p := ready(t, s, "aaa000000001", now)
	if _, e := s.EnqueueBackupJob(ctx, "job_1", p.ID, "{}", true, now); e != nil {
		t.Fatal(e)
	}
	schedule, _ := s.BackupSchedule(ctx, p.ID)
	if schedule.LastAttemptAt != now.Unix() {
		t.Fatal("the attempt was not recorded with the job", schedule)
	}
	if _, e := s.ClaimJob(ctx, now); e != nil {
		t.Fatal(e)
	}
	interrupted, e := s.InterruptRunningJobs(ctx, now.Add(time.Minute), 24*time.Hour)
	if e != nil || len(interrupted) != 1 {
		t.Fatal(interrupted, e)
	}
	if schedule, _ = s.BackupSchedule(ctx, p.ID); schedule.Failures != 1 || schedule.NextAttemptAt == 0 {
		t.Fatal("an interrupted scheduled backup did not back off", schedule)
	}
}

func TestBackupPolicyValidation(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	policy, e := s.BackupPolicy(ctx)
	if e != nil || policy.TargetIntervalHours != 24 || policy.RetentionDaily != 14 || policy.RetentionWeekly != 8 {
		t.Fatal(policy, e)
	}
	for _, bad := range []BackupPolicy{{TargetIntervalHours: 5, RetentionDaily: 14, RetentionWeekly: 8},
		{TargetIntervalHours: 24, RetentionDaily: 0, RetentionWeekly: 8},
		{TargetIntervalHours: 24, RetentionDaily: 14, RetentionWeekly: 53}} {
		if e := s.SetBackupPolicy(ctx, bad, time.Now()); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	if e := s.SetBackupPolicy(ctx, BackupPolicy{TargetIntervalHours: 6, RetentionDaily: 7, RetentionWeekly: 4}, time.Now()); e != nil {
		t.Fatal(e)
	}
	if policy, _ = s.BackupPolicy(ctx); policy.Interval() != 6*time.Hour {
		t.Fatal(policy)
	}
}

// Reconciliation is the only thing allowed to retire history, and it carries
// deletion intent forward only while the manifest is still missing.
func TestReplacePrefixReconcilesHistoryAndDeletionIntent(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	p := ready(t, s, "aaa000000001", now)
	key := "pgfy/backups/" + p.DBName + "/20231114T221320Z/manifest.json"
	archive := "pgfy/backups/" + p.DBName + "/20231114T221320Z/archive.dump"
	if e := s.RecordBackup(ctx, Backup{ID: "bk_1", ProjectID: p.ID, JobID: "job_1", ObjectKey: key, Manifest: "{}", CreatedAt: now.Unix()}); e != nil {
		t.Fatal(e)
	}
	entry := BucketBackup{ManifestKey: key, ArchiveKey: archive, DBName: p.DBName, TakenAt: now.Unix(), State: BackupComplete, InstallationID: "install", ProjectID: p.ID}
	if e := s.ReplacePrefix(ctx, "target", PrefixState{DBName: p.DBName, Complete: 1}, []BucketBackup{entry}, now); e != nil {
		t.Fatal(e)
	}
	if n, e := s.ForgetLostLocalBackups(ctx, "target"); e != nil || n != 0 {
		t.Fatal("a live backup was retired", n, e)
	}
	// Deletion started, then the manifest turned out to be there after all.
	if e := s.MarkBackupDeleting(ctx, "target", key, now); e != nil {
		t.Fatal(e)
	}
	if e := s.ReplacePrefix(ctx, "target", PrefixState{DBName: p.DBName, Complete: 1}, []BucketBackup{entry}, now); e != nil {
		t.Fatal(e)
	}
	got, e := s.BucketBackup(ctx, "target", key)
	if e != nil || got.DeleteStartedAt != 0 {
		t.Fatal("a stale deletion marker survived seeing the manifest", got, e)
	}
	// Archive-only entries keep the marker so a half-delete can be finished.
	if e := s.MarkBackupDeleting(ctx, "target", key, now); e != nil {
		t.Fatal(e)
	}
	entry.State = BackupArchiveOnly
	if e := s.ReplacePrefix(ctx, "target", PrefixState{DBName: p.DBName, ArchiveOnly: 1}, []BucketBackup{entry}, now); e != nil {
		t.Fatal(e)
	}
	if got, _ = s.BucketBackup(ctx, "target", key); got.DeleteStartedAt == 0 {
		t.Fatal("our own half-delete lost its provenance", got)
	}
	// The object is gone from the bucket, so the history row goes too.
	if n, e := s.ForgetLostLocalBackups(ctx, "target"); e != nil || n != 1 {
		t.Fatal("a backup missing from the bucket stayed in history", n, e)
	}
	if e := s.ReplacePrefix(ctx, "target", PrefixState{DBName: p.DBName}, nil, now); e != nil {
		t.Fatal(e)
	}
	if backups, e := s.PrefixBackups(ctx, "target", p.DBName); e != nil || len(backups) != 0 {
		t.Fatal(backups, e)
	}
}

// Pruning keeps history bounded but never discards provenance that cleanup
// still needs, and never for a store it has not completely reconciled.
func TestPruneJobsKeepsLiveProvenance(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	p := ready(t, s, "aaa000000001", now)
	old := now.Add(-200 * 24 * time.Hour)
	directory := "pgfy/backups/" + p.DBName + "/20230101T000000Z"
	for _, id := range []string{"job_live", "job_done"} {
		if _, e := s.EnqueueBackupJob(ctx, id, p.ID, "{}", true, old); e != nil {
			t.Fatal(e)
		}
		if e := s.CompleteBackupJob(ctx, id, p.ID, "failed", "upload_archive", "interrupted", "{}", nil, true, old, 24*time.Hour); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.SetJobTarget(ctx, "job_live", "target", directory); e != nil {
		t.Fatal(e)
	}
	if e := s.SetJobTarget(ctx, "job_done", "target", "pgfy/backups/"+p.DBName+"/20230202T000000Z"); e != nil {
		t.Fatal(e)
	}
	if e := s.ActivateStorageTarget(ctx, StorageTarget{Target: "target", Endpoint: "https://s3.example", Bucket: "b", Prefix: "pgfy"}); e != nil {
		t.Fatal(e)
	}
	abandoned := BucketBackup{ManifestKey: directory + "/manifest.json", ArchiveKey: directory + "/archive.dump",
		DBName: p.DBName, TakenAt: old.Unix(), State: BackupArchiveOnly}
	if e := s.ReplacePrefix(ctx, "target", PrefixState{DBName: p.DBName, ArchiveOnly: 1}, []BucketBackup{abandoned}, old); e != nil {
		t.Fatal(e)
	}
	// Nothing is pruned while the store has no complete reconciliation.
	if n, e := s.PruneJobs(ctx, now.Add(-90*24*time.Hour)); e != nil || n != 0 {
		t.Fatal("provenance was pruned for an unreconciled store", n, e)
	}
	if e := s.SetTargetReconciled(ctx, "target", now, ""); e != nil {
		t.Fatal(e)
	}
	n, e := s.PruneJobs(ctx, now.Add(-90*24*time.Hour))
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, e := s.Job(ctx, "job_live"); e != nil {
		t.Fatal("the row proving an abandoned archive is ours was pruned", e)
	}
	ours, e := s.AbandonedUploadKey(ctx, "target", directory, now)
	if e != nil || !ours {
		t.Fatal("provenance lost", ours, e)
	}
	if ours, _ := s.AbandonedUploadKey(ctx, "another-target", directory, now); ours {
		t.Fatal("provenance authorised a different store")
	}
}
