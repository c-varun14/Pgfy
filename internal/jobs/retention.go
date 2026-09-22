// Reconciliation keeps the application's idea of what is recoverable equal to
// what the bucket actually holds, and retention removes what the operator's
// policy no longer keeps. Every deletion is deliberate: publication writes the
// manifest last, so deletion removes it first, and nothing is deleted without
// positive proof that it belongs to this installation.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/c-varun14/Pgfy/internal/storage"
	"github.com/c-varun14/Pgfy/internal/store"
)

// objectStore is the part of the S3 client reconciliation and retention use, so
// their ordering and their behaviour on failure can be exercised in tests.
type objectStore interface {
	ListDatabases(ctx context.Context) ([]string, error)
	ListBackups(ctx context.Context, dbName string) ([]storage.Entry, error)
	Manifest(ctx context.Context, key string) (storage.Manifest, error)
	Remove(ctx context.Context, key string) error
	Protection(ctx context.Context) (string, error)
}

// Reconcile reads the whole store and updates the recoverable view. It runs at
// startup before the first scheduling decision, after every successful backup,
// and on its own timer.
func (w *Worker) Reconcile(ctx context.Context) {
	settings, e := w.StorageSettings(ctx)
	if e != nil {
		return
	}
	client, e := w.StorageClient(ctx)
	if e != nil {
		slog.Error("backup storage is unusable", "reason", e.Error())
		return
	}
	if e := w.reconcile(ctx, client, settings); e != nil {
		slog.Warn("backup reconciliation incomplete", "reason", e.Error())
	}
}

func (w *Worker) reconcile(ctx context.Context, client objectStore, settings storage.Settings) error {
	target := settings.Target()
	if e := w.Store.ActivateStorageTarget(ctx, store.StorageTarget{Target: target, Endpoint: settings.Endpoint, Bucket: settings.Bucket, Prefix: settings.Prefix}); e != nil {
		return e
	}
	databases, e := client.ListDatabases(ctx)
	if e != nil {
		_ = w.Store.SetTargetReconciled(ctx, target, w.now(), e.Error())
		return e
	}
	present := map[string]bool{}
	for _, name := range databases {
		present[name] = true
	}
	if e := w.Store.TombstonePrefixes(ctx, target, present, w.now()); e != nil {
		return e
	}
	complete := true
	var failure error
	for _, name := range databases {
		if e := w.reconcilePrefix(ctx, client, target, name); e != nil {
			// One unreadable prefix must not make the rest look authoritative.
			complete, failure = false, e
			slog.Warn("backup folder could not be listed", "database", name, "reason", e.Error())
		}
	}
	if !complete {
		_ = w.Store.SetTargetReconciled(ctx, target, w.now(), failure.Error())
		return failure
	}
	if e := w.Store.SetTargetReconciled(ctx, target, w.now(), ""); e != nil {
		return e
	}
	// Only a complete view may retire history rows: "the object is gone" must
	// never be confused with "the listing did not get that far".
	if n, e := w.Store.ForgetLostLocalBackups(ctx, target); e != nil {
		slog.Warn("backup history could not be reconciled", "reason", e.Error())
	} else if n > 0 {
		slog.Warn("backups are no longer in the bucket", "count", n)
	}
	w.retain(ctx, client, settings, databases)
	w.pruneHistory(ctx)
	return nil
}

// reconcilePrefix records one database folder exactly as a complete listing
// found it. A partial or failed listing changes nothing at all.
func (w *Worker) reconcilePrefix(ctx context.Context, client objectStore, target, dbName string) error {
	entries, e := client.ListBackups(ctx, dbName)
	if e != nil {
		return e
	}
	known := map[string]store.BucketBackup{}
	recorded, e := w.Store.PrefixBackups(ctx, target, dbName)
	if e != nil {
		return e
	}
	for _, b := range recorded {
		known[b.ManifestKey] = b
	}
	state := store.PrefixState{DBName: dbName}
	backups := []store.BucketBackup{}
	owners := map[string]bool{}
	for _, entry := range entries {
		b := store.BucketBackup{ManifestKey: entry.ManifestKey, ArchiveKey: entry.ArchiveKey, DBName: dbName,
			TakenAt: entry.TakenAt.Unix(), State: entry.State, SizeBytes: entry.SizeBytes}
		if entry.State == storage.StateComplete || entry.State == storage.StateManifestOnly {
			previous, seen := known[entry.ManifestKey]
			if seen && previous.InstallationID != "" {
				b.InstallationID, b.ProjectID, b.ProjectName = previous.InstallationID, previous.ProjectID, previous.ProjectName
				b.PostgresVersion, b.TableCount = previous.PostgresVersion, previous.TableCount
			} else if m, e := client.Manifest(ctx, entry.ManifestKey); e == nil {
				if e := storage.CheckManifest(m, entry); e != nil {
					b.State = storage.StateDamaged
				} else {
					b.InstallationID, b.ProjectID, b.ProjectName = m.InstallationID, m.ProjectID, m.ProjectName
					b.PostgresVersion, b.TableCount = m.PostgresVersion, len(m.Tables)
				}
			} else {
				// Unreadable manifests are hidden, counted, and never deleted.
				b.State = storage.StateDamaged
			}
		}
		switch b.State {
		case storage.StateComplete:
			state.Complete++
			owners[b.InstallationID+"|"+b.ProjectID] = true
		case storage.StateManifestOnly:
			state.ManifestOnly++
		case storage.StateArchiveOnly:
			state.ArchiveOnly++
		default:
			state.Damaged++
		}
		backups = append(backups, b)
	}
	state.Mixed = len(owners) > 1
	if state.ManifestOnly > 0 {
		slog.Warn("incomplete backup found in the bucket", "database", dbName, "count", state.ManifestOnly)
	}
	return w.Store.ReplacePrefix(ctx, target, state, backups, w.now())
}

// retain applies the operator's policy: keep the newest backup always, then a
// daily and a weekly series. Nothing is deleted unless the bucket is protected
// and the backup provably belongs to this installation.
func (w *Worker) retain(ctx context.Context, client objectStore, settings storage.Settings, databases []string) {
	protection, e := client.Protection(ctx)
	if e != nil {
		slog.Warn("retention skipped: bucket protection could not be confirmed", "reason", e.Error())
		return
	}
	if protection == storage.VersioningDisabled {
		slog.Warn("retention skipped: the bucket does not keep versions of deleted objects")
		return
	}
	if protection == storage.VersioningUnsupported && settings.BucketProtection != storage.ProtectionAcknowledged {
		slog.Warn("retention skipped: this provider cannot confirm bucket protection and it has not been acknowledged")
		return
	}
	policy := w.Policy(ctx)
	target := settings.Target()
	for _, dbName := range databases {
		if e := w.retainDatabase(ctx, client, target, dbName, policy); e != nil {
			slog.Warn("retention incomplete", "database", dbName, "reason", e.Error())
		}
	}
}

func (w *Worker) retainDatabase(ctx context.Context, client objectStore, target, dbName string, policy store.BackupPolicy) error {
	backups, e := w.Store.PrefixBackups(ctx, target, dbName)
	if e != nil {
		return e
	}
	// Our own half-deletes are finished first: they have no manifest left, so
	// they are exempt from the ownership rules the rest of this depends on.
	for _, b := range backups {
		if b.State == storage.StateArchiveOnly && b.DeleteStartedAt > 0 {
			if e := w.finishDelete(ctx, client, target, b); e != nil {
				return e
			}
		}
	}
	if blocked := blockedByForeignContent(backups, w.InstallationID); blocked != "" {
		slog.Warn("retention skipped for this folder", "database", dbName, "reason", blocked)
		return nil
	}
	for _, b := range selectExpired(backups, policy, w.now()) {
		if e := w.deleteBackup(ctx, client, target, b); e != nil {
			return e
		}
	}
	return w.cleanAbandonedUploads(ctx, client, target, backups)
}

// blockedByForeignContent fails closed: a folder holding another installation's
// backups, or anything damaged, is left entirely alone.
func blockedByForeignContent(backups []store.BucketBackup, installationID string) string {
	for _, b := range backups {
		if b.State == storage.StateDamaged {
			return "it contains a backup that cannot be read"
		}
		if b.State == storage.StateComplete && b.InstallationID != installationID {
			return "it contains backups from another installation"
		}
	}
	return ""
}

// selectExpired keeps the newest backup, then the newest of each of the most
// recent days and weeks; everything else in this installation's own set expires.
func selectExpired(backups []store.BucketBackup, policy store.BackupPolicy, now time.Time) []store.BucketBackup {
	var complete []store.BucketBackup
	for _, b := range backups {
		if b.State == storage.StateComplete {
			complete = append(complete, b)
		}
	}
	if len(complete) == 0 {
		return nil
	}
	keep := map[string]bool{complete[0].ManifestKey: true} // rows arrive newest first
	days, weeks := map[string]bool{}, map[string]bool{}
	for _, b := range complete {
		at := time.Unix(b.TakenAt, 0).UTC()
		day := at.Format("2006-01-02")
		year, week := at.ISOWeek()
		weekKey := fmt.Sprintf("%d-%02d", year, week)
		if len(days) < policy.RetentionDaily && !days[day] {
			days[day] = true
			keep[b.ManifestKey] = true
			continue
		}
		if days[day] {
			continue
		}
		if len(weeks) < policy.RetentionWeekly && !weeks[weekKey] {
			weeks[weekKey] = true
			keep[b.ManifestKey] = true
		}
	}
	var expired []store.BucketBackup
	for _, b := range complete {
		if !keep[b.ManifestKey] {
			expired = append(expired, b)
		}
	}
	return expired
}

// deleteBackup mirrors publication. The intent is recorded first so a failure
// part-way can be finished later, the manifest goes before the archive so a
// half-deleted backup is never offered, and the local row goes last.
func (w *Worker) deleteBackup(ctx context.Context, client objectStore, target string, b store.BucketBackup) error {
	if e := w.Store.MarkBackupDeleting(ctx, target, b.ManifestKey, w.now()); e != nil {
		return e
	}
	if e := client.Remove(ctx, b.ManifestKey); e != nil {
		return e
	}
	if e := w.Store.MarkBackupArchiveOnly(ctx, target, b.ManifestKey); e != nil {
		return e
	}
	return w.finishDelete(ctx, client, target, b)
}

func (w *Worker) finishDelete(ctx context.Context, client objectStore, target string, b store.BucketBackup) error {
	if e := client.Remove(ctx, b.ArchiveKey); e != nil {
		return e
	}
	if e := w.Store.ForgetBucketBackup(ctx, target, b.ManifestKey); e != nil {
		return e
	}
	return w.Store.DeleteLocalBackupRow(ctx, b.ManifestKey)
}

// cleanAbandonedUploads removes the archive of an interrupted backup of ours,
// and only ours: without a local job claiming that exact folder in this exact
// store, the archive may be another installation's work in progress.
func (w *Worker) cleanAbandonedUploads(ctx context.Context, client objectStore, target string, backups []store.BucketBackup) error {
	cutoff := w.now().Add(-24 * time.Hour)
	for _, b := range backups {
		if b.State != storage.StateArchiveOnly || b.DeleteStartedAt > 0 || time.Unix(b.TakenAt, 0).After(cutoff) {
			continue
		}
		directory := b.ManifestKey[:len(b.ManifestKey)-len("/"+storage.ManifestFile)]
		ours, e := w.Store.AbandonedUploadKey(ctx, target, directory, cutoff)
		if e != nil {
			return e
		}
		if !ours {
			continue
		}
		if e := client.Remove(ctx, b.ArchiveKey); e != nil {
			return e
		}
		if e := w.Store.ForgetBucketBackup(ctx, target, b.ManifestKey); e != nil {
			return e
		}
	}
	return nil
}

func (w *Worker) pruneHistory(ctx context.Context) {
	if n, e := w.Store.PruneJobs(ctx, w.now().Add(-90*24*time.Hour)); e != nil {
		slog.Warn("job history could not be pruned", "reason", e.Error())
	} else if n > 0 {
		slog.Info("job history pruned", "rows", n)
	}
}
