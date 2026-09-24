package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/storage"
	"github.com/c-varun14/Pgfy/internal/store"
)

const installation = "install-a"

// fakeStore is a bucket that can be made to fail on demand, so the order of
// deletions and what survives a failure can be checked exactly.
type fakeStore struct {
	objects    map[string]int64
	manifests  map[string]storage.Manifest
	protection string
	listErr    map[string]error
	removeErr  map[string]error
	removed    []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]int64{}, manifests: map[string]storage.Manifest{}, protection: storage.VersioningEnabled,
		listErr: map[string]error{}, removeErr: map[string]error{}}
}

func (f *fakeStore) put(dbName, stamp string, m *storage.Manifest) {
	directory := "pgfy/backups/" + dbName + "/" + stamp
	f.objects[directory+"/archive.dump"] = 100
	if m == nil {
		return
	}
	at, _ := time.Parse(storage.TimeLayout, stamp)
	m.Version, m.DBName, m.CreatedAt = 1, dbName, at.UTC()
	m.ArchiveKey, m.ManifestKey = directory+"/archive.dump", directory+"/manifest.json"
	f.objects[m.ManifestKey] = 1
	f.manifests[m.ManifestKey] = *m
}

func (f *fakeStore) ListDatabases(context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for key := range f.objects {
		parts := strings.Split(strings.TrimPrefix(key, "pgfy/backups/"), "/")
		if len(parts) > 0 && !seen[parts[0]] {
			seen[parts[0]] = true
			out = append(out, parts[0])
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeStore) ListBackups(_ context.Context, dbName string) ([]storage.Entry, error) {
	if e := f.listErr[dbName]; e != nil {
		return nil, e
	}
	root := "pgfy/backups/" + dbName + "/"
	var objects []storage.Object
	for key, size := range f.objects {
		if strings.HasPrefix(key, root) {
			objects = append(objects, storage.Object{Key: key, Size: size})
		}
	}
	return storage.EntriesFrom(root, objects), nil
}

func (f *fakeStore) Manifest(_ context.Context, key string) (storage.Manifest, error) {
	m, ok := f.manifests[key]
	if !ok {
		return m, errors.New("no such manifest")
	}
	return m, nil
}

func (f *fakeStore) Remove(_ context.Context, key string) error {
	if e := f.removeErr[key]; e != nil {
		return e
	}
	if _, ok := f.objects[key]; !ok {
		return errors.New("no such object")
	}
	delete(f.objects, key)
	delete(f.manifests, key)
	f.removed = append(f.removed, key)
	return nil
}

func (f *fakeStore) Protection(context.Context) (string, error) { return f.protection, nil }

func (f *fakeStore) has(key string) bool { _, ok := f.objects[key]; return ok }

func testWorker(t *testing.T, now time.Time) (*Worker, *store.Store) {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "metadata.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	return &Worker{Store: s, InstallationID: installation, Now: func() time.Time { return now }}, s
}

func testSettings() storage.Settings {
	return storage.Settings{Endpoint: "https://s3.example.com", Bucket: "backups", Prefix: "pgfy",
		AccessKey: "k", SecretKey: "s", BucketProtection: storage.ProtectionVersioning}
}

func manifestFor(project, name string) *storage.Manifest {
	return &storage.Manifest{InstallationID: installation, ProjectID: project, ProjectName: name}
}

func stamps(backups []store.BucketBackup) []string {
	out := []string{}
	for _, b := range backups {
		out = append(out, b.ManifestKey)
	}
	return out
}

// Retention keeps the newest backup, a daily series and a weekly series, and
// deletes the manifest before the archive so a half-deleted backup is never
// offered for restore.
func TestRetentionKeepsSeriesAndDeletesManifestFirst(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	fake := newFakeStore()
	for _, day := range []string{"20260301T000000Z", "20260228T000000Z", "20260227T000000Z", "20260220T000000Z", "20260213T000000Z", "20260101T000000Z"} {
		fake.put("app_shop", day, manifestFor("prj_shop", "Shop"))
	}
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 2, RetentionWeekly: 1}, now); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	kept, _ := s.PrefixBackups(ctx, testSettings().Target(), "app_shop")
	if len(kept) != 3 {
		t.Fatal("expected the newest, one more daily and one weekly", stamps(kept))
	}
	// Manifests always go first, so nothing ever looks recoverable without one.
	for i := 0; i < len(fake.removed); i += 2 {
		if !strings.HasSuffix(fake.removed[i], storage.ManifestFile) || !strings.HasSuffix(fake.removed[i+1], storage.ArchiveFile) {
			t.Fatal("deletion order was not manifest then archive", fake.removed)
		}
	}
	if fake.has("pgfy/backups/app_shop/20260101T000000Z/archive.dump") {
		t.Fatal("an expired archive survived")
	}
	if !fake.has("pgfy/backups/app_shop/20260301T000000Z/manifest.json") {
		t.Fatal("the newest backup was deleted")
	}
}

// A failure part-way through a deletion must never leave a backup that looks
// recoverable, and the next run finishes what the last one started.
func TestHalfDeleteIsNeverRecoverableAndIsRetried(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	target := testSettings().Target()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	fake.put("app_shop", "20260101T000000Z", manifestFor("prj_shop", "Shop"))
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 1, RetentionWeekly: 0}, now); e != nil {
		t.Fatal(e)
	}
	expired := "pgfy/backups/app_shop/20260101T000000Z"
	fake.removeErr[expired+"/archive.dump"] = errors.New("the network went away")
	// A failed deletion is reported and retried later; it never fails the pass.
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	row, e := s.BucketBackup(ctx, target, expired+"/manifest.json")
	if e != nil || row.State != store.BackupArchiveOnly || row.DeleteStartedAt == 0 {
		t.Fatal("a manifest-less backup was left looking recoverable", row, e)
	}
	backups, _, _ := s.BucketBackups(ctx, target, "app_shop", 0, 10)
	if len(backups) != 1 {
		t.Fatal("the half-deleted backup is still on offer", stamps(backups))
	}
	// The next run finishes it from our own recorded intent.
	delete(fake.removeErr, expired+"/archive.dump")
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if fake.has(expired + "/archive.dump") {
		t.Fatal("the half-delete was not finished")
	}
	if _, e := s.BucketBackup(ctx, target, expired+"/manifest.json"); e == nil {
		t.Fatal("the record outlived both objects")
	}
}

// If the manifest deletion itself fails, nothing else may happen: the backup
// stays whole and stays recoverable.
func TestFailedManifestDeletionLeavesTheBackupWhole(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	target := testSettings().Target()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	fake.put("app_shop", "20260101T000000Z", manifestFor("prj_shop", "Shop"))
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 1, RetentionWeekly: 0}, now); e != nil {
		t.Fatal(e)
	}
	expired := "pgfy/backups/app_shop/20260101T000000Z"
	fake.removeErr[expired+"/manifest.json"] = errors.New("access denied")
	_ = w.reconcile(ctx, fake, testSettings())
	if !fake.has(expired+"/manifest.json") || !fake.has(expired+"/archive.dump") {
		t.Fatal("objects went missing after a failed manifest deletion", fake.removed)
	}
	// Seeing the manifest again clears our deletion marker, so a later removal
	// by somebody else is not mistaken for our own half-delete.
	delete(fake.removeErr, expired+"/manifest.json")
	fake.removeErr[expired+"/archive.dump"] = errors.New("still denied")
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 90, RetentionWeekly: 52}, now); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	row, e := s.BucketBackup(ctx, target, expired+"/manifest.json")
	if e != nil || row.DeleteStartedAt != 0 || row.State != store.BackupComplete {
		t.Fatal("a stale deletion marker survived", row, e)
	}
	// Somebody else removes the manifest: the archive is now of unknown origin
	// and must not be deleted without a local job claiming it.
	delete(fake.objects, expired+"/manifest.json")
	delete(fake.manifests, expired+"/manifest.json")
	before := len(fake.removed)
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if len(fake.removed) != before || !fake.has(expired+"/archive.dump") {
		t.Fatal("an archive was deleted without provenance", fake.removed)
	}
	if row, _ = s.BucketBackup(ctx, target, expired+"/manifest.json"); row.DeleteStartedAt != 0 {
		t.Fatal("an externally removed manifest was treated as our own deletion", row)
	}
}

// Another installation's backups, and anything unreadable, stop retention for
// that folder entirely.
func TestRetentionFailsClosedOnForeignOrDamagedContent(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	fake.put("app_shop", "20260101T000000Z", manifestFor("prj_shop", "Shop"))
	foreign := manifestFor("prj_other", "Other")
	foreign.InstallationID = "install-b"
	fake.put("app_shop", "20251201T000000Z", foreign)
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 1, RetentionWeekly: 0}, now); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if len(fake.removed) != 0 {
		t.Fatal("retention ran in a folder holding another installation's backups", fake.removed)
	}
	prefixes, _ := s.PrefixStates(ctx, testSettings().Target())
	if len(prefixes) != 1 || !prefixes[0].Mixed {
		t.Fatal("a folder with two owners was not reported as mixed", prefixes)
	}
}

// Nothing is deleted when the bucket cannot be shown to be protected.
func TestRetentionRequiresProtection(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	fake.put("app_shop", "20260101T000000Z", manifestFor("prj_shop", "Shop"))
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 1, RetentionWeekly: 0}, now); e != nil {
		t.Fatal(e)
	}
	fake.protection = storage.VersioningDisabled
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if len(fake.removed) != 0 {
		t.Fatal("deleted from a bucket that keeps no versions", fake.removed)
	}
	// A provider that cannot answer needs the operator's acknowledgment.
	fake.protection = storage.VersioningUnsupported
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if len(fake.removed) != 0 {
		t.Fatal("deleted without an acknowledgment", fake.removed)
	}
	acknowledged := testSettings()
	acknowledged.BucketProtection = storage.ProtectionAcknowledged
	if e := w.reconcile(ctx, fake, acknowledged); e != nil {
		t.Fatal(e)
	}
	if len(fake.removed) != 2 {
		t.Fatal("an acknowledged bucket did not apply retention", fake.removed)
	}
}

// A folder that cannot be listed completely changes nothing, and leaves the
// store unreconciled so scheduling does not act on a partial view.
func TestPartialListingChangesNothing(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	target := testSettings().Target()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	fake.put("app_blog", "20260301T000000Z", manifestFor("prj_blog", "Blog"))
	fake.listErr["app_shop"] = errors.New("the listing timed out")
	if e := w.reconcile(ctx, fake, testSettings()); e == nil {
		t.Fatal("an incomplete pass reported success")
	}
	state, _ := s.StorageTarget(ctx, target)
	if state.ReconciledAt != 0 || state.LastError == "" {
		t.Fatal("a partial pass was recorded as a complete view", state)
	}
	if backups, _ := s.PrefixBackups(ctx, target, "app_shop"); len(backups) != 0 {
		t.Fatal("an unreadable folder was recorded anyway", backups)
	}
	if len(fake.removed) != 0 {
		t.Fatal("retention ran on a partial view", fake.removed)
	}
	// Once it lists, the whole store is reconciled and the other database is intact.
	delete(fake.listErr, "app_shop")
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if state, _ = s.StorageTarget(ctx, target); state.ReconciledAt == 0 {
		t.Fatal("a complete pass was not recorded")
	}
	if backups, _, _ := s.BucketBackups(ctx, target, "app_blog", 0, 10); len(backups) != 1 {
		t.Fatal(backups)
	}
}

// A database folder that disappears keeps a reconciled, empty record: "nothing
// is there" has to stay distinguishable from "never looked".
func TestVanishedPrefixLeavesATombstone(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	target := testSettings().Target()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	fake.objects = map[string]int64{}
	fake.manifests = map[string]storage.Manifest{}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	prefixes, _ := s.PrefixStates(ctx, target)
	if len(prefixes) != 1 || prefixes[0].Complete != 0 || prefixes[0].ReconciledAt == 0 {
		t.Fatal("the vanished folder left no reconciled record", prefixes)
	}
	if backups, _ := s.PrefixBackups(ctx, target, "app_shop"); len(backups) != 0 {
		t.Fatal(backups)
	}
}

// An archive with no manifest is deleted only when a local job proves it is our
// own abandoned upload, in this same store.
func TestAbandonedUploadsNeedProvenance(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	fake := newFakeStore()
	fake.put("app_shop", "20260301T000000Z", manifestFor("prj_shop", "Shop"))
	fake.put("app_shop", "20260210T000000Z", nil) // an interrupted upload
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if !fake.has("pgfy/backups/app_shop/20260210T000000Z/archive.dump") {
		t.Fatal("an archive of unknown origin was deleted")
	}
	// Now record the job that wrote it, in a different store first.
	p, _, e := s.CreateProject(ctx, store.Project{ID: "prj_shop", Name: "Shop", DBName: "app_shop", RoleName: "app_shop"}, "key", "sealed", now.Add(-72*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.EnqueueBackupJob(ctx, "job_1", p.ID, "{}", true, now.Add(-72*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e := s.SetJobTarget(ctx, "job_1", "another-store", "pgfy/backups/app_shop/20260210T000000Z"); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteBackupJob(ctx, "job_1", p.ID, "interrupted", "upload_archive", "restarted", "{}", nil, true, now.Add(-48*time.Hour), 24*time.Hour); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if !fake.has("pgfy/backups/app_shop/20260210T000000Z/archive.dump") {
		t.Fatal("provenance from a different store authorised a deletion")
	}
	if e := s.SetJobTarget(ctx, "job_1", testSettings().Target(), "pgfy/backups/app_shop/20260210T000000Z"); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if fake.has("pgfy/backups/app_shop/20260210T000000Z/archive.dump") {
		t.Fatal("our own abandoned upload was not cleaned up")
	}
}

// An hourly schedule must not have today's backups thinned to one the moment
// the next lands; older days are still reduced to one backup each.
func TestRetentionKeepsTheLastDayWhole(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	fake := newFakeStore()
	for _, stamp := range []string{"20260301T110000Z", "20260301T100000Z", "20260301T090000Z", "20260227T110000Z", "20260227T100000Z"} {
		fake.put("app_shop", stamp, manifestFor("prj_shop", "Shop"))
	}
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 1, RetentionDaily: 3, RetentionWeekly: 0}, now); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	kept, _ := s.PrefixBackups(ctx, testSettings().Target(), "app_shop")
	if len(kept) != 4 {
		t.Fatal("expected today's three backups and the newest of the older day", stamps(kept))
	}
	if fake.has("pgfy/backups/app_shop/20260227T100000Z/archive.dump") {
		t.Fatal("an older day was not thinned to its newest backup")
	}
}

// During an update the bucket is read, never changed: a rollback restores
// metadata but cannot bring back an object the release being tried deleted.
func TestMaintenanceReadsTheBucketButDeletesNothing(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w, s := testWorker(t, now)
	ctx := context.Background()
	fake := newFakeStore()
	for _, day := range []string{"20260301T000000Z", "20260220T000000Z", "20260101T000000Z"} {
		fake.put("app_shop", day, manifestFor("prj_shop", "Shop"))
	}
	if e := s.SetBackupPolicy(ctx, store.BackupPolicy{TargetIntervalHours: 24, RetentionDaily: 1, RetentionWeekly: 0}, now); e != nil {
		t.Fatal(e)
	}
	if e := s.SetMaintenance(ctx, true); e != nil {
		t.Fatal(e)
	}
	if e := w.reconcile(ctx, fake, testSettings()); e != nil {
		t.Fatal(e)
	}
	if len(fake.removed) != 0 {
		t.Fatal("objects deleted during maintenance", fake.removed)
	}
	if seen, _ := s.PrefixBackups(ctx, testSettings().Target(), "app_shop"); len(seen) != 3 {
		t.Fatal("the listing was not recorded", stamps(seen))
	}
}
