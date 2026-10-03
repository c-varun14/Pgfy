package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

// deleteInput copies the project's identity into the job, because the job row
// outlives the project row it deletes.
type deleteInput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	DBName    string `json:"db_name"`
	RoleName  string `json:"role_name"`
}

type DeleteResult struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	DBName    string `json:"db_name"`
}

// NewDeleteJob names a delete job for a project and records its identity.
func NewDeleteJob(p store.Project) (string, string) {
	input, _ := json.Marshal(deleteInput{ProjectID: p.ID, Name: p.Name, DBName: p.DBName, RoleName: p.RoleName})
	return "job_" + security.Token()[:16], string(input)
}

// runDelete removes a project marked for deletion. Every step is idempotent, so
// an interrupted or failed deletion is simply run again. The project's lock is
// held throughout, so neither provisioning nor a password change can act on
// the role while it is being removed.
func (w *Worker) runDelete(ctx context.Context, job store.Job) (DeleteResult, error) {
	var in deleteInput
	if e := json.Unmarshal([]byte(job.Input), &in); e != nil {
		return DeleteResult{}, e
	}
	result := DeleteResult{ProjectID: in.ProjectID, Name: in.Name, DBName: in.DBName}
	if w.Provisioner == nil {
		return result, errors.New("PostgreSQL management is unavailable")
	}
	lock := w.Provisioner.RotationLock(in.ProjectID)
	lock.Lock()
	defer lock.Unlock()
	w.stage(ctx, job, "preparing")
	project, e := w.Store.Project(ctx, in.ProjectID)
	if e != nil {
		return result, errors.New("the project could not be read")
	}
	if project.Stage != "deleting" {
		return result, errors.New("the project is not marked for deletion")
	}
	w.stage(ctx, job, "drop_database")
	if e := w.Provisioner.PG.DropProject(ctx, project.DBName, project.RoleName); e != nil {
		return result, e
	}
	w.stage(ctx, job, "access_policy")
	if e := w.Provisioner.SyncPolicy(ctx); e != nil {
		return result, errors.New("the access policy could not be applied: " + e.Error())
	}
	w.stage(ctx, job, "remove_records")
	encoded, _ := json.Marshal(result)
	if e := w.Store.CompleteProjectDeletion(ctx, job.ID, project, string(encoded), w.now()); e != nil {
		return result, e
	}
	return result, nil
}

// resumeDeletions queues deletions that are marked but have no job: after a
// restart, or for another attempt after a failure.
func (w *Worker) resumeDeletions(ctx context.Context) {
	if n, e := w.Store.ResumeDeletions(ctx, NewDeleteJob, w.now()); e != nil {
		slog.Warn("deletions could not be resumed", "reason", e.Error())
	} else if n > 0 {
		slog.Info("deletions resumed", "count", n)
	}
}
