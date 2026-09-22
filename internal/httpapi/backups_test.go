package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/jobs"
	"github.com/c-varun14/Pgfy/internal/storage"
	"github.com/c-varun14/Pgfy/internal/store"
)

// withStorage gives the fixture a worker and a configured store without
// contacting anything: the API reads the reconciled view from SQLite.
func (f *fixture) withStorage(t *testing.T) string {
	t.Helper()
	f.s.Jobs = jobs.New(f.s.Store, f.s.Vault, nil, nil, f.s.Config.ID, t.TempDir())
	settings := storage.Settings{Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1", Bucket: "backups", Prefix: "pgfy",
		AccessKey: "key", SecretKey: "secret", BucketProtection: storage.ProtectionVersioning}
	if e := f.s.Jobs.SaveStorageSettings(context.Background(), settings); e != nil {
		t.Fatal(e)
	}
	return settings.Target()
}

func (f *fixture) readyProject(t *testing.T, id, name string, since time.Duration) store.Project {
	t.Helper()
	at := f.now.Add(-since)
	p, _, e := f.s.Store.CreateProject(context.Background(), store.Project{ID: "prj_" + id, Name: name, DBName: "app_" + id, RoleName: "app_" + id},
		"key-"+id, f.s.Vault.Seal("project:prj_"+id, []byte("password")), at)
	if e != nil {
		t.Fatal(e)
	}
	if e := f.s.Store.SetProjectStage(context.Background(), p.ID, "ready", at); e != nil {
		t.Fatal(e)
	}
	p.Stage, p.ReadyAt = "ready", at.Unix()
	return p
}

func (f *fixture) recordBackup(t *testing.T, target string, p store.Project, at time.Time) {
	t.Helper()
	directory := "pgfy/backups/" + p.DBName + "/" + at.UTC().Format(storage.TimeLayout)
	backup := store.BucketBackup{ManifestKey: directory + "/manifest.json", ArchiveKey: directory + "/archive.dump", DBName: p.DBName,
		TakenAt: at.Unix(), State: store.BackupComplete, InstallationID: f.s.Config.ID, ProjectID: p.ID, ProjectName: p.Name, SizeBytes: 1000}
	existing, e := f.s.Store.PrefixBackups(context.Background(), target, p.DBName)
	if e != nil {
		t.Fatal(e)
	}
	backups := append([]store.BucketBackup{backup}, existing...)
	state := store.PrefixState{DBName: p.DBName, Complete: len(backups)}
	if e := f.s.Store.ReplacePrefix(context.Background(), target, state, backups, f.now); e != nil {
		t.Fatal(e)
	}
}

// Discovery pages each database on its own, so a store holding many backups
// cannot push a database out of the list.
func TestRecoveryDiscoveryPagesEachDatabase(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.setup(t)
	target := f.withStorage(t)
	shop := f.readyProject(t, "aaa000000001", "Shop", time.Hour)
	blog := f.readyProject(t, "bbb000000002", "Blog", time.Hour)
	start := f.now.Add(-300 * time.Hour)
	for i := 0; i < 250; i++ {
		f.recordBackup(t, target, shop, start.Add(time.Duration(i)*time.Hour))
	}
	f.recordBackup(t, target, blog, f.now.Add(-time.Hour))
	if e := f.s.Store.SetTargetReconciled(context.Background(), target, f.now, ""); e != nil {
		t.Fatal(e)
	}
	r := f.request("GET", "/api/v1/recovery/backups", "", cookie, "", "")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var body struct {
		State     string `json:"state"`
		Databases []struct {
			DBName    string `json:"db_name"`
			Count     int    `json:"count"`
			HasMore   bool   `json:"has_more"`
			Foreign   bool   `json:"foreign"`
			ProjectID string `json:"project_id"`
			Backups   []struct {
				ManifestKey string `json:"manifest_key"`
				TakenAt     int64  `json:"taken_at"`
			} `json:"backups"`
		} `json:"databases"`
	}
	if e := json.Unmarshal(r.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body.State != "ok" || len(body.Databases) != 2 {
		t.Fatal("a database disappeared from discovery", body.State, len(body.Databases))
	}
	for _, group := range body.Databases {
		if group.DBName == blog.DBName && group.Count != 1 {
			t.Fatal("the smaller database was truncated away", group)
		}
		if group.DBName == shop.DBName {
			if group.Count != 250 || !group.HasMore || len(group.Backups) != 20 {
				t.Fatal("paging is wrong", group.Count, group.HasMore, len(group.Backups))
			}
			if group.ProjectID != shop.ID || group.Foreign {
				t.Fatal("identity came out wrong", group)
			}
			if group.Backups[0].TakenAt < group.Backups[1].TakenAt {
				t.Fatal("backups are not newest first")
			}
		}
	}
	// Older pages come from the same validated parameters.
	page := f.request("GET", "/api/v1/recovery/backups?db="+shop.DBName+"&before="+start.Add(10*time.Hour).UTC().Format(storage.TimeLayout)+"&limit=5", "", cookie, "", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), shop.DBName) {
		t.Fatal(page.Code, page.Body.String())
	}
	for _, query := range []string{"?db=../etc", "?db=" + shop.DBName + "&limit=0", "?db=" + shop.DBName + "&limit=1000", "?db=" + shop.DBName + "&before=yesterday"} {
		if r := f.request("GET", "/api/v1/recovery/backups"+query, "", cookie, "", ""); r.Code != 400 {
			t.Fatal("accepted", query, r.Code)
		}
	}
}

// Until the store has been read completely, "no backup" cannot be told from
// "not looked yet", and the dashboard says so.
func TestBackupStatusPrecedence(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.setup(t)
	status := func() string {
		r := f.request("GET", "/api/v1/system/status", "", cookie, "", "")
		var body struct {
			Backups string `json:"backups"`
		}
		if e := json.Unmarshal(r.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		return body.Backups
	}
	if got := status(); got != "not_configured" {
		t.Fatal(got)
	}
	target := f.withStorage(t)
	if got := status(); got != "checking" {
		t.Fatal("an unread store claimed to know its backups", got)
	}
	ctx := context.Background()
	// A ready project that is long overdue and has nothing recoverable is stale.
	shop := f.readyProject(t, "aaa000000001", "Shop", 72*time.Hour)
	if e := f.s.Store.SetTargetReconciled(ctx, target, f.now, ""); e != nil {
		t.Fatal(e)
	}
	if got := status(); got != "stale" {
		t.Fatal(got)
	}
	f.recordBackup(t, target, shop, f.now.Add(-time.Hour))
	if got := status(); got != "ok" {
		t.Fatal(got)
	}
	// A failing backup takes precedence over the age of what is in the bucket.
	if _, e := f.s.Store.EnqueueBackupJob(ctx, "job_1", shop.ID, "{}", true, f.now); e != nil {
		t.Fatal(e)
	}
	if e := f.s.Store.CompleteBackupJob(ctx, "job_1", shop.ID, "failed", "dump", "no", "{}", nil, true, f.now, 24*time.Hour); e != nil {
		t.Fatal(e)
	}
	if got := status(); got != "failing" {
		t.Fatal(got)
	}
}

func TestBackupPolicyEndpoint(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	if r := f.request("GET", "/api/v1/settings/backups", "", nil, "", ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	r := f.request("GET", "/api/v1/settings/backups", "", cookie, "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"target_interval_hours":24`) {
		t.Fatal(r.Code, r.Body.String())
	}
	if r = f.request("PUT", "/api/v1/settings/backups", `{"target_interval_hours":6,"retention_daily":7,"retention_weekly":4}`, cookie, "", f.s.Config.Origin); r.Code != 403 {
		t.Fatal("missing CSRF accepted", r.Code)
	}
	if r = f.request("PUT", "/api/v1/settings/backups", `{"target_interval_hours":5,"retention_daily":7,"retention_weekly":4}`, cookie, csrf, f.s.Config.Origin); r.Code != 400 {
		t.Fatal("an unsupported interval was accepted", r.Code, r.Body.String())
	}
	if r = f.request("PUT", "/api/v1/settings/backups", `{"target_interval_hours":6,"retention_daily":7,"retention_weekly":4}`, cookie, csrf, f.s.Config.Origin); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	policy, e := f.s.Store.BackupPolicy(context.Background())
	if e != nil || policy.TargetIntervalHours != 6 || policy.RetentionDaily != 7 {
		t.Fatal(policy, e)
	}
}

// The project view answers with the bucket's age, not with local history.
func TestProjectBackupsReportRecoverableAge(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.setup(t)
	target := f.withStorage(t)
	ctx := context.Background()
	shop := f.readyProject(t, "aaa000000001", "Shop", time.Hour)
	taken := f.now.Add(-2 * time.Hour)
	// A history row whose objects are gone must not be reported as recoverable.
	if e := f.s.Store.RecordBackup(ctx, store.Backup{ID: "bk_old", ProjectID: shop.ID, JobID: "job_old",
		ObjectKey: "pgfy/backups/" + shop.DBName + "/20200101T000000Z/manifest.json", Manifest: "{}", CreatedAt: taken.Add(-time.Hour).Unix()}); e != nil {
		t.Fatal(e)
	}
	f.recordBackup(t, target, shop, taken)
	r := f.request("GET", "/api/v1/projects/"+shop.ID+"/backups", "", cookie, "", "")
	var body struct {
		NewestBackupAt      int64 `json:"newest_backup_at"`
		TargetIntervalHours int   `json:"target_interval_hours"`
		NextScheduledAt     int64 `json:"next_scheduled_at"`
		Failures            int   `json:"failures"`
	}
	if e := json.Unmarshal(r.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body.NewestBackupAt != taken.Unix() {
		t.Fatal("the age came from history instead of the bucket", body.NewestBackupAt, taken.Unix())
	}
	if body.TargetIntervalHours != 24 || body.NextScheduledAt != taken.Unix()+24*3600 {
		t.Fatal(body)
	}
	if body.Failures != 0 {
		t.Fatal(body)
	}
}
