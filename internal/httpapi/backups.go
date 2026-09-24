package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/c-varun14/Pgfy/internal/jobs"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/storage"
	"github.com/c-varun14/Pgfy/internal/store"
)

func (s *Server) jobsReady(w http.ResponseWriter) bool {
	if s.Jobs == nil {
		failure(w, 503, "postgres_unavailable", "Backups are unavailable on this installation. Run host diagnostics.")
		return false
	}
	return true
}

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.jobsReady(w) {
		return
	}
	settings, e := s.Jobs.StorageSettings(r.Context())
	if errors.Is(e, jobs.ErrStorageNotConfigured) {
		write(w, 200, map[string]any{"configured": false, "settings": storage.Settings{Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1", Prefix: "pgfy"}})
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Storage settings could not be read.")
		return
	}
	write(w, 200, map[string]any{"configured": true, "settings": settings.Masked()})
}

func (s *Server) putStorage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.jobsReady(w) {
		return
	}
	var in storage.Settings
	if !decode(w, r, &in) {
		return
	}
	in.ProtectionState, in.ProtectionCheckedAt = "", 0
	// Blank credentials keep the stored ones so the form can be resubmitted with masked values.
	if existing, e := s.Jobs.StorageSettings(r.Context()); e == nil {
		if in.AccessKey == "" || in.AccessKey == existing.Masked().AccessKey {
			in.AccessKey = existing.AccessKey
		}
		if in.SecretKey == "" || in.SecretKey == existing.Masked().SecretKey {
			in.SecretKey = existing.SecretKey
		}
		if in.SessionToken == existing.Masked().SessionToken {
			in.SessionToken = existing.SessionToken
		}
	}
	if e := in.Validate(); e != nil {
		failure(w, 400, "invalid_storage", e.Error())
		return
	}
	// Backups are only as safe as the bucket holding them, so the bucket is
	// asked about its own protection before these settings are accepted.
	state, e := s.checkProtection(r, in)
	if e != nil {
		failure(w, 400, "invalid_storage", e.Error())
		return
	}
	in.ProtectionState, in.ProtectionCheckedAt = state, s.Now().Unix()
	if e := s.Jobs.SaveStorageSettings(r.Context(), in); e != nil {
		failure(w, 503, "metadata_unavailable", "Storage settings could not be saved.")
		return
	}
	s.Jobs.Kick()
	s.audit(w, r, "settings.storage", in.Bucket, map[string]string{"endpoint": in.Endpoint, "prefix": in.Prefix})
	write(w, 200, map[string]any{"configured": true, "settings": in.Masked()})
}

// checkProtection refuses a bucket whose provider says versioning is off,
// whichever protection the operator chose: an acknowledgment is for providers
// that cannot answer, not a way past an answer nobody likes.
func (s *Server) checkProtection(r *http.Request, in storage.Settings) (string, error) {
	client, e := storage.New(in)
	if e != nil {
		return "", e
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	state, e := client.Protection(ctx)
	if e != nil {
		return "", e
	}
	switch {
	case state == storage.VersioningDisabled:
		return "", errors.New("this bucket does not keep versions of deleted objects. Enable versioning on the bucket, then save again.")
	case state == storage.VersioningUnsupported && in.BucketProtection == storage.ProtectionVersioning:
		return "", errors.New("this provider does not report bucket versioning. Protect the bucket with the provider's own lock and acknowledge it instead.")
	}
	return state, nil
}

func (s *Server) checkStorage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.jobsReady(w) {
		return
	}
	client, e := s.Jobs.StorageClient(r.Context())
	if errors.Is(e, jobs.ErrStorageNotConfigured) {
		failure(w, 409, "storage_not_configured", "Save storage settings first.")
		return
	}
	if e != nil {
		failure(w, 400, "invalid_storage", e.Error())
		return
	}
	// The check talks to an external endpoint; give it its own bound beyond the request default.
	ctx, cancel := contextWithTimeout(r, 40*time.Second)
	defer cancel()
	steps := client.Check(ctx)
	ok := len(steps) > 0
	for _, step := range steps {
		ok = ok && step.OK
	}
	write(w, 200, map[string]any{"ok": ok, "steps": steps})
}

type jobView struct {
	store.Job
	ResultJSON json.RawMessage `json:"result"`
	Elapsed    int64           `json:"elapsed_seconds"`
}

func (s *Server) jobView(j store.Job) jobView {
	view := jobView{Job: j, ResultJSON: json.RawMessage("{}")}
	if json.Valid([]byte(j.Result)) {
		view.ResultJSON = json.RawMessage(j.Result)
	}
	end := j.FinishedAt
	if end == 0 {
		end = s.Now().Unix()
	}
	if j.StartedAt > 0 {
		view.Elapsed = end - j.StartedAt
	}
	return view
}

func (s *Server) startBackup(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.jobsReady(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if p.Stage != "ready" || p.Failed {
		failure(w, 409, "not_ready", "The database is not ready yet.")
		return
	}
	if _, e := s.Jobs.StorageSettings(r.Context()); e != nil {
		failure(w, 409, "storage_not_configured", "Configure backup storage in Settings before backing up.")
		return
	}
	job, e := s.Store.EnqueueBackupJob(r.Context(), "job_"+security.Token()[:16], p.ID, `{"scheduled":false}`, false, s.Now())
	if errors.Is(e, store.ErrJobBusy) {
		failure(w, 409, "job_busy", "Another backup or restore is still running. Try again when it finishes.")
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "The backup could not be queued.")
		return
	}
	s.Jobs.Kick()
	write(w, 202, s.jobView(job))
}

type backupView struct {
	store.Backup
	ManifestJSON json.RawMessage `json:"manifest"`
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	backups, e := s.Store.ProjectBackups(r.Context(), p.ID, 50)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backup history could not be read.")
		return
	}
	history, e := s.Store.ProjectJobs(r.Context(), p.ID, 20)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backup history could not be read.")
		return
	}
	views := []backupView{}
	for _, b := range backups {
		views = append(views, backupView{Backup: b, ManifestJSON: json.RawMessage(b.Manifest)})
	}
	jobViews := []jobView{}
	for _, j := range history {
		jobViews = append(jobViews, s.jobView(j))
	}
	configured, target := s.storageTarget(r)
	policy, _ := s.Store.BackupPolicy(r.Context())
	schedule, _ := s.Store.BackupSchedule(r.Context(), p.ID)
	// The age shown is the bucket's answer, not our history: a backup removed
	// outside the application must not keep counting as recoverable.
	var newest int64
	if configured {
		if recoverable, e := s.Store.NewestRecoverable(r.Context(), target, s.Config.ID); e == nil {
			newest = recoverable[p.ID]
		}
	}
	var next int64
	if configured && newest > 0 {
		next = newest + int64(policy.Interval()/time.Second)
	}
	if schedule.NextAttemptAt > next {
		next = schedule.NextAttemptAt
	}
	write(w, 200, map[string]any{"backups": views, "jobs": jobViews, "storage_configured": configured,
		"next_scheduled_at": next, "newest_backup_at": newest, "target_interval_hours": policy.TargetIntervalHours,
		"failures": schedule.Failures, "last_attempt_at": schedule.LastAttemptAt})
}

// storageTarget identifies the store in use, so cached knowledge of one bucket
// is never shown for another.
func (s *Server) storageTarget(r *http.Request) (bool, string) {
	if s.Jobs == nil {
		return false, ""
	}
	settings, e := s.Jobs.StorageSettings(r.Context())
	if e != nil {
		return false, ""
	}
	return true, settings.Target()
}

func (s *Server) getBackupPolicy(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	policy, e := s.Store.BackupPolicy(r.Context())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backup settings could not be read.")
		return
	}
	write(w, 200, policy)
}

func (s *Server) putBackupPolicy(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	var in store.BackupPolicy
	if !decode(w, r, &in) {
		return
	}
	if e := s.Store.SetBackupPolicy(r.Context(), in, s.Now()); e != nil {
		failure(w, 400, "invalid_policy", e.Error())
		return
	}
	s.audit(w, r, "settings.backups", "", in)
	if s.Jobs != nil {
		s.Jobs.Kick()
	}
	policy, e := s.Store.BackupPolicy(r.Context())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backup settings could not be read back.")
		return
	}
	write(w, 200, policy)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	job, e := s.Store.Job(r.Context(), r.PathValue("id"))
	if errors.Is(e, store.ErrProjectNotFound) {
		failure(w, 404, "not_found", "Job not found.")
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Job could not be read.")
		return
	}
	write(w, 200, s.jobView(job))
}

type databaseGroup struct {
	DBName         string               `json:"db_name"`
	ProjectID      string               `json:"project_id,omitempty"`
	ProjectName    string               `json:"project_name,omitempty"`
	InstallationID string               `json:"installation_id,omitempty"`
	Mixed          bool                 `json:"mixed"`
	Foreign        bool                 `json:"foreign"`
	NewestAt       int64                `json:"newest_at"`
	Count          int                  `json:"count"`
	TotalBytes     int64                `json:"total_bytes"`
	HasMore        bool                 `json:"has_more"`
	ManifestOnly   int                  `json:"manifest_only"`
	Damaged        int                  `json:"damaged"`
	ReconciledAt   int64                `json:"reconciled_at"`
	Backups        []store.BucketBackup `json:"backups"`
}

const discoveryPage = 20

// listRecoveryBackups serves the reconciled view of the bucket. Each database
// is paged on its own, so no number of backups can hide one of them, and no
// request pays for a listing of the whole store.
func (s *Server) listRecoveryBackups(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.jobsReady(w) {
		return
	}
	settings, e := s.Jobs.StorageSettings(r.Context())
	if errors.Is(e, jobs.ErrStorageNotConfigured) {
		write(w, 200, map[string]any{"state": "storage_not_configured", "databases": []databaseGroup{}})
		return
	}
	if e != nil {
		failure(w, 400, "invalid_storage", e.Error())
		return
	}
	target := settings.Target()
	state, e := s.Store.StorageTarget(r.Context(), target)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backups could not be read.")
		return
	}
	if name := r.URL.Query().Get("db"); name != "" {
		s.pageDatabase(w, r, target, name)
		return
	}
	prefixes, e := s.Store.PrefixStates(r.Context(), target)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backups could not be read.")
		return
	}
	groups := []databaseGroup{}
	for _, prefix := range prefixes {
		group, e := s.group(r, target, prefix)
		if e != nil {
			failure(w, 503, "metadata_unavailable", "Backups could not be read.")
			return
		}
		if group.Count > 0 || group.ManifestOnly > 0 || group.Damaged > 0 {
			groups = append(groups, group)
		}
	}
	active, _ := s.Store.ActiveJobs(r.Context())
	recent, _ := s.Store.RecentJobs(r.Context(), 20)
	restores := []jobView{}
	for _, j := range recent {
		if j.Kind == "restore" {
			restores = append(restores, s.jobView(j))
		}
	}
	status := "ok"
	if state.ReconciledAt == 0 {
		status = "checking"
	}
	write(w, 200, map[string]any{"state": status, "databases": groups, "installation_id": s.Config.ID,
		"reconciled_at": state.ReconciledAt, "storage_error": state.LastError, "busy": len(active) > 0, "restores": restores})
}

func (s *Server) group(r *http.Request, target string, prefix store.PrefixState) (databaseGroup, error) {
	group := databaseGroup{DBName: prefix.DBName, Mixed: prefix.Mixed, Count: prefix.Complete,
		ManifestOnly: prefix.ManifestOnly, Damaged: prefix.Damaged, ReconciledAt: prefix.ReconciledAt, Backups: []store.BucketBackup{}}
	backups, more, e := s.Store.BucketBackups(r.Context(), target, prefix.DBName, 0, discoveryPage)
	if e != nil {
		return group, e
	}
	group.Backups, group.HasMore = backups, more
	for _, b := range backups {
		group.TotalBytes += b.SizeBytes
	}
	if len(backups) > 0 {
		newest := backups[0]
		group.NewestAt = newest.TakenAt
		// Identity comes from the manifests themselves and only when they agree;
		// a folder holding two installations' work claims neither.
		if !prefix.Mixed {
			group.ProjectID, group.ProjectName, group.InstallationID = newest.ProjectID, newest.ProjectName, newest.InstallationID
			group.Foreign = newest.InstallationID != s.Config.ID
		}
	}
	return group, nil
}

func (s *Server) pageDatabase(w http.ResponseWriter, r *http.Request, target, name string) {
	if !storage.ValidDatabaseSegment(name) {
		failure(w, 400, "invalid_request", "That is not a database folder written by this application.")
		return
	}
	before := int64(0)
	if value := r.URL.Query().Get("before"); value != "" {
		at, e := time.Parse(storage.TimeLayout, value)
		if e != nil {
			failure(w, 400, "invalid_request", "Provide a backup timestamp to page from.")
			return
		}
		before = at.Unix()
	}
	limit := discoveryPage
	if value := r.URL.Query().Get("limit"); value != "" {
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || n > 100 {
			failure(w, 400, "invalid_request", "Ask for between 1 and 100 backups.")
			return
		}
		limit = n
	}
	backups, more, e := s.Store.BucketBackups(r.Context(), target, name, before, limit)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Backups could not be read.")
		return
	}
	write(w, 200, map[string]any{"db_name": name, "backups": backups, "has_more": more})
}

func (s *Server) startRestore(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.authorize(w, r)
	if !ok || !s.jobsReady(w) || !s.provisioningReady(w) {
		return
	}
	var in struct {
		ManifestKey string `json:"manifest_key"`
		Name        string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validProjectName(in.Name) {
		failure(w, 400, "invalid_name", "Use 1–64 letters, digits, spaces, dots, dashes, underscores, or parentheses.")
		return
	}
	client, e := s.Jobs.StorageClient(r.Context())
	if e != nil {
		failure(w, 409, "storage_not_configured", "Configure backup storage in Settings first.")
		return
	}
	if _, e := client.Manifest(r.Context(), in.ManifestKey); e != nil {
		failure(w, 400, "invalid_backup", e.Error())
		return
	}
	// A restore always targets a brand-new project; existing databases are never touched.
	suffix := hex.EncodeToString([]byte(security.Token()))[:12]
	project := store.Project{ID: "prj_" + suffix, Name: in.Name, DBName: "app_" + suffix, RoleName: "app_" + suffix}
	password := security.Token()
	key := security.Hash(session.Email + "|restore|" + in.ManifestKey + "|" + s.Now().Truncate(time.Minute).Format(time.RFC3339))
	created, _, e := s.Store.CreateProject(r.Context(), project, key, s.Vault.Seal("project:"+project.ID, []byte(password)), s.Now())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "The target project could not be saved.")
		return
	}
	input, _ := json.Marshal(map[string]string{"manifest_key": in.ManifestKey, "name": in.Name, "project_id": created.ID})
	job, e := s.Store.EnqueueJob(r.Context(), "job_"+security.Token()[:16], "restore", created.ID, string(input), s.Now())
	if errors.Is(e, store.ErrJobBusy) {
		failure(w, 409, "job_busy", "Another backup or restore is still running. Try again when it finishes.")
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "The restore could not be queued.")
		return
	}
	s.Jobs.Kick()
	write(w, 202, map[string]any{"project": created, "job": s.jobView(job)})
}
