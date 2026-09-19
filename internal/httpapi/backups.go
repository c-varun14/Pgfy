package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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
	if e := s.Jobs.SaveStorageSettings(r.Context(), in); e != nil {
		failure(w, 503, "metadata_unavailable", "Storage settings could not be saved.")
		return
	}
	write(w, 200, map[string]any{"configured": true, "settings": in.Masked()})
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
	job, e := s.Store.EnqueueJob(r.Context(), "job_"+security.Token()[:16], "backup", p.ID, `{"scheduled":false}`, s.Now())
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
	configured := false
	if s.Jobs != nil {
		_, e := s.Jobs.StorageSettings(r.Context())
		configured = e == nil
	}
	var next int64
	if configured && len(backups) > 0 {
		next = backups[0].CreatedAt + int64(jobs.ScheduleEvery/time.Second)
	}
	write(w, 200, map[string]any{"backups": views, "jobs": jobViews, "storage_configured": configured, "next_scheduled_at": next})
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

func (s *Server) listRecoveryBackups(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.jobsReady(w) {
		return
	}
	client, e := s.Jobs.StorageClient(r.Context())
	if errors.Is(e, jobs.ErrStorageNotConfigured) {
		write(w, 200, map[string]any{"state": "storage_not_configured", "backups": []storage.Manifest{}})
		return
	}
	if e != nil {
		failure(w, 400, "invalid_storage", e.Error())
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	manifests, e := client.ListManifests(ctx, 200)
	if e != nil {
		write(w, 200, map[string]any{"state": "storage_error", "error": e.Error(), "backups": []storage.Manifest{}})
		return
	}
	active, _ := s.Store.ActiveJobs(r.Context())
	recent, _ := s.Store.RecentJobs(r.Context(), 20)
	restores := []jobView{}
	for _, j := range recent {
		if j.Kind == "restore" {
			restores = append(restores, s.jobView(j))
		}
	}
	write(w, 200, map[string]any{"state": "ok", "backups": manifests, "installation_id": s.Config.ID, "busy": len(active) > 0, "restores": restores})
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
		failure(w, 400, "invalid_name", "Use 1–64 letters, digits, spaces, dots, dashes, or underscores.")
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
