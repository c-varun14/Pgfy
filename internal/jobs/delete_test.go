package jobs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/hba"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/provision"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

type dropRoles struct {
	mu      sync.Mutex
	dropped []string
	fail    error
}

func (d *dropRoles) EnsureRole(context.Context, string, string, int64) error { return nil }
func (d *dropRoles) EnsureDatabase(context.Context, string, string) error    { return nil }
func (d *dropRoles) ApplyLimits(context.Context, string, map[string]string, int64) error {
	return nil
}
func (d *dropRoles) RoleStates(context.Context) (map[string]postgres.RoleState, error) {
	return map[string]postgres.RoleState{}, nil
}
func (d *dropRoles) ResetDatabaseLimits(context.Context, string, string, []string) error { return nil }
func (d *dropRoles) SetPassword(context.Context, string, string) error                   { return nil }
func (d *dropRoles) TerminateSessions(context.Context, string, string) error             { return nil }
func (d *dropRoles) DropProject(_ context.Context, database, role string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return d.fail
	}
	d.dropped = append(d.dropped, database+"/"+role)
	return nil
}

type reloader struct{}

func (reloader) HBAFileErrors(context.Context) ([]string, error) { return nil, nil }
func (reloader) Reload(context.Context) (time.Time, error)       { return time.Now(), nil }

func deleteWorker(t *testing.T) (*Worker, *store.Store, *dropRoles, string) {
	t.Helper()
	w, s := testWorker(t, time.Now())
	v, _ := security.NewVault(bytes.Repeat([]byte{3}, 32))
	roles := &dropRoles{}
	dir := t.TempDir()
	w.Provisioner = provision.New(s, v, roles, &hba.Manager{Dir: dir, TunnelSource: "172.20.242.0/24", PG: reloader{}})
	return w, s, roles, dir
}

func readyProject(t *testing.T, s *store.Store, id, name string) store.Project {
	t.Helper()
	ctx := context.Background()
	p, _, e := s.CreateProject(ctx, store.Project{ID: "prj_" + id, Name: name, DBName: "app_" + id, RoleName: "app_" + id}, "key-"+id, "sealed", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if e := s.SetProjectStage(ctx, p.ID, "ready", time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RecordPolicyRevision(ctx, p.ID, 0, `["0.0.0.0/0"]`, time.Now()); e != nil {
		t.Fatal(e)
	}
	p.Stage = "ready"
	return p
}

func TestDeletionRemovesDatabaseRulesAndRows(t *testing.T) {
	w, s, roles, dir := deleteWorker(t)
	ctx := context.Background()
	shop := readyProject(t, s, "000000000021", "Shop")
	blog := readyProject(t, s, "000000000022", "Blog")
	if e := w.Provisioner.SyncPolicy(ctx); e != nil {
		t.Fatal(e)
	}
	if rules, _ := os.ReadFile(filepath.Join(dir, "projects.conf")); !strings.Contains(string(rules), shop.RoleName) {
		t.Fatal("fixture policy missing", string(rules))
	}
	// A backup queued for the project is cancelled, never run against a database being removed.
	backup, e := s.EnqueueBackupJob(ctx, "job_backup", shop.ID, `{}`, true, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	job, created, e := s.BeginProjectDeletion(ctx, shop.ID, NewDeleteJob, store.AuditEntry{At: time.Now().Unix(), Action: "project.delete", Target: shop.ID}, time.Now())
	if e != nil || !created || job.Kind != "delete" {
		t.Fatal(job, created, e)
	}
	if cancelled, _ := s.Job(ctx, backup.ID); cancelled.State != "failed" {
		t.Fatal("queued backup still queued", cancelled.State)
	}
	w.drain(ctx)
	done, _ := s.Job(ctx, job.ID)
	if done.State != "succeeded" || done.ProjectID != "" {
		t.Fatal(done)
	}
	if len(roles.dropped) != 1 || roles.dropped[0] != shop.DBName+"/"+shop.RoleName {
		t.Fatal(roles.dropped)
	}
	if _, e := s.Project(ctx, shop.ID); !errors.Is(e, store.ErrProjectNotFound) {
		t.Fatal("project row survived", e)
	}
	var secrets int
	s.DB.QueryRow("SELECT count(*) FROM project_secrets WHERE project_id=?", shop.ID).Scan(&secrets)
	if secrets != 0 {
		t.Fatal("the sealed password outlived the project")
	}
	rules, _ := os.ReadFile(filepath.Join(dir, "projects.conf"))
	if strings.Contains(string(rules), shop.RoleName) || !strings.Contains(string(rules), blog.RoleName) {
		t.Fatal("access policy not synced", string(rules))
	}
	if _, e := s.Project(ctx, blog.ID); e != nil {
		t.Fatal("another project was touched", e)
	}
	alerts, _ := s.AlertsToSend(ctx, time.Now())
	found := false
	for _, a := range alerts {
		found = found || a.Kind == "project_deleted"
	}
	if !found {
		t.Fatal("deletion not reported", alerts)
	}
}

func TestFailedDeletionKeepsTombstoneAndResumes(t *testing.T) {
	w, s, roles, _ := deleteWorker(t)
	ctx := context.Background()
	shop := readyProject(t, s, "000000000031", "Shop")
	roles.fail = errors.New("the database could not be dropped: connection refused")
	job, _, e := s.BeginProjectDeletion(ctx, shop.ID, NewDeleteJob, store.AuditEntry{At: time.Now().Unix(), Action: "project.delete"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	w.drain(ctx)
	failed, _ := s.Job(ctx, job.ID)
	if failed.State != "failed" || failed.Stage != "drop_database" || !strings.Contains(failed.Error, "connection refused") {
		t.Fatal(failed)
	}
	p, e := s.Project(ctx, shop.ID)
	if e != nil || p.Stage != "deleting" {
		t.Fatal("tombstone lost", p, e)
	}
	// The provisioner never brings a project marked for deletion back.
	if incomplete, _ := s.IncompleteProjects(ctx); len(incomplete) != 0 {
		t.Fatal(incomplete)
	}
	if e := s.SetProjectStage(ctx, shop.ID, "ready", time.Now()); e != nil {
		t.Fatal(e)
	}
	if p, _ := s.Project(ctx, shop.ID); p.Stage != "deleting" {
		t.Fatal("a provisioning result overwrote the tombstone")
	}
	// Recovery, after a restart or on the next reconcile tick, queues another attempt.
	roles.fail = nil
	w.resumeDeletions(ctx)
	w.resumeDeletions(ctx)
	if active, _ := s.ActiveJobs(ctx); len(active) != 1 {
		t.Fatal("expected exactly one resumed job", active)
	}
	w.drain(ctx)
	if _, e := s.Project(ctx, shop.ID); !errors.Is(e, store.ErrProjectNotFound) {
		t.Fatal("resumed deletion did not finish", e)
	}
	// One alert for the failure, not one per retry.
	alerts, _ := s.AlertsToSend(ctx, time.Now())
	failures := 0
	for _, a := range alerts {
		if a.Kind == "delete_failed" {
			failures++
		}
	}
	if failures != 1 {
		t.Fatal("expected one failure alert", alerts)
	}
}

func TestDeletionIsRefusedWhileProvisioningAndDuringUpdates(t *testing.T) {
	_, s, _, _ := deleteWorker(t)
	ctx := context.Background()
	p, _, _ := s.CreateProject(ctx, store.Project{ID: "prj_000000000041", Name: "New", DBName: "app_000000000041", RoleName: "app_000000000041"}, "k", "sealed", time.Now())
	if _, _, e := s.BeginProjectDeletion(ctx, p.ID, NewDeleteJob, store.AuditEntry{Action: "project.delete"}, time.Now()); !errors.Is(e, store.ErrNotDeletable) {
		t.Fatal(e)
	}
	shop := readyProject(t, s, "000000000042", "Shop")
	s.SetMaintenance(ctx, true)
	if _, _, e := s.BeginProjectDeletion(ctx, shop.ID, NewDeleteJob, store.AuditEntry{Action: "project.delete"}, time.Now()); !errors.Is(e, store.ErrMaintenance) {
		t.Fatal(e)
	}
	if p, _ := s.Project(ctx, shop.ID); p.Stage != "ready" {
		t.Fatal("tombstone written during an update")
	}
	// A failed project can be deleted: it never held data.
	s.SetMaintenance(ctx, false)
	s.FailProject(ctx, p.ID, "boom")
	if _, created, e := s.BeginProjectDeletion(ctx, p.ID, NewDeleteJob, store.AuditEntry{Action: "project.delete"}, time.Now()); e != nil || !created {
		t.Fatal(e)
	}
}
