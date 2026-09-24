// Package provision turns persisted project identities into real PostgreSQL
// roles and databases, resuming from the recorded stage after interruption.
package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/c-varun14/Pgfy/internal/hba"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

// DefaultAddresses is the open-by-default policy: TLS plus the project password
// from anywhere until the user restricts it.
var DefaultAddresses = []string{"0.0.0.0/0", "::/0"}

// Roles is the part of PostgreSQL management provisioning needs; tests
// substitute it to exercise crash and race paths without a server.
type Roles interface {
	EnsureRole(ctx context.Context, name, password string, connectionLimit int64) error
	EnsureDatabase(ctx context.Context, name, owner string) error
	ApplyLimits(ctx context.Context, role string, settings map[string]string, connectionLimit int64) error
	RoleStates(ctx context.Context) (map[string]postgres.RoleState, error)
	ResetDatabaseLimits(ctx context.Context, role, database string, keys []string) error
	SetPassword(ctx context.Context, role, password string) error
	TerminateSessions(ctx context.Context, database, role string) error
}

type Provisioner struct {
	Store *store.Store
	Vault *security.Vault
	PG    Roles
	HBA   *hba.Manager
	kick  chan struct{}

	locks  sync.Map   // project ID → *sync.Mutex guarding every rotation step
	limits sync.Mutex // one limits sync at a time, so an older revision never lands after a newer one
}

func New(s *store.Store, v *security.Vault, pg Roles, h *hba.Manager) *Provisioner {
	return &Provisioner{Store: s, Vault: v, PG: pg, HBA: h, kick: make(chan struct{}, 1)}
}

// RotationLock serialises everything that changes a project's password: the
// dashboard's rotate request and the loop that finishes interrupted ones.
func (p *Provisioner) RotationLock(projectID string) *sync.Mutex {
	lock, _ := p.locks.LoadOrStore(projectID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// Kick wakes the loop without blocking the caller.
func (p *Provisioner) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run reconciles incomplete projects until ctx ends. It also reapplies the
// access policy once at startup so a fresh container serves the stored rules.
func (p *Provisioner) Run(ctx context.Context) {
	if e := p.SyncPolicy(ctx); e != nil {
		slog.Warn("access policy not applied at startup", "reason", e.Error())
	}
	for {
		p.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		case <-time.After(30 * time.Second):
		}
	}
}

func (p *Provisioner) pass(ctx context.Context) {
	if on, e := p.Store.Maintenance(ctx); e != nil || on {
		// A rollback restores metadata, not PostgreSQL: create nothing it would orphan.
		return
	}
	p.FinishRotations(ctx)
	p.SyncLimits(ctx)
	projects, e := p.Store.IncompleteProjects(ctx)
	if e != nil {
		slog.Error("provisioning queue unavailable", "reason", e.Error())
		return
	}
	changed := false
	for _, project := range projects {
		if ctx.Err() != nil {
			return
		}
		if e := p.provision(ctx, project); e != nil {
			slog.Warn("project provisioning failed", "project", project.ID, "stage", project.Stage, "reason", e.Error())
			_ = p.Store.FailProject(ctx, project.ID, e.Error())
			continue
		}
		changed = true
	}
	if changed {
		if e := p.SyncPolicy(ctx); e != nil {
			slog.Warn("access policy not applied", "reason", e.Error())
		}
	}
}

// provision advances one project to ready; every stage is idempotent and
// transient PostgreSQL errors are retried before the project is marked failed.
func (p *Provisioner) provision(ctx context.Context, project store.Project) error {
	sealed, e := p.Store.SealedPassword(ctx, project.ID)
	if e != nil {
		return e
	}
	password, e := p.Vault.Open("project:"+project.ID, sealed)
	if e != nil {
		return errors.New("stored credentials cannot be decrypted with this installation key")
	}
	limits, e := p.Store.ProjectLimits(ctx, project.ID)
	if e != nil {
		return e
	}
	stages := []struct {
		name string
		run  func(context.Context) error
	}{
		{"role_created", func(ctx context.Context) error { return p.PG.EnsureRole(ctx, project.RoleName, string(password), limits.ConnectionLimit) }},
		{"database_created", func(ctx context.Context) error { return p.PG.EnsureDatabase(ctx, project.DBName, project.RoleName) }},
		{"ready", func(ctx context.Context) error {
			// A project never serves without its guardrails.
			if e := p.applyLimits(ctx, project.ID, project.RoleName, limits); e != nil {
				return e
			}
			st, e := p.Store.PolicyState(ctx, project.ID)
			if e != nil {
				return e
			}
			if st.CurrentRevision == 0 {
				addresses, _ := json.Marshal(DefaultAddresses)
				if _, e := p.Store.RecordPolicyRevision(ctx, project.ID, 0, string(addresses), time.Now()); e != nil && !errors.Is(e, store.ErrRevisionConflict) {
					return e
				}
			}
			return nil
		}},
	}
	started := project.Stage != "identity_persisted"
	for _, stage := range stages {
		if started {
			// Resume after the persisted stage; the earlier stages already completed.
			if project.Stage == stage.name {
				started = false
			}
			continue
		}
		if e := retry(ctx, stage.run); e != nil {
			return fmt.Errorf("%s: %w", stage.name, e)
		}
		if e := p.Store.SetProjectStage(ctx, project.ID, stage.name, time.Now()); e != nil {
			return e
		}
	}
	return nil
}

func retry(ctx context.Context, fn func(context.Context) error) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		last = fn(ctx)
		if last == nil || errors.Is(last, postgres.ErrRoleMismatch) || errors.Is(last, postgres.ErrOwnerMismatch) {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(2 * time.Second):
		}
	}
	return last
}

// ProvisionNow provisions one project synchronously (used by restores) and
// applies the access policy; it returns the reason if the project failed.
func (p *Provisioner) ProvisionNow(ctx context.Context, projectID string) error {
	project, e := p.Store.Project(ctx, projectID)
	if e != nil {
		return e
	}
	if project.Failed {
		return errors.New(project.StageError)
	}
	if project.Stage != "ready" {
		if e := p.provision(ctx, project); e != nil {
			_ = p.Store.FailProject(ctx, project.ID, e.Error())
			return e
		}
	}
	return p.SyncPolicy(ctx)
}

// SyncPolicy renders every ready project's current revision into the managed
// rule file and records per-project outcomes truthfully.
func (p *Provisioner) SyncPolicy(ctx context.Context) error {
	projects, e := p.Store.Projects(ctx)
	if e != nil {
		return e
	}
	var rules []hba.Rule
	revisions := map[string]store.PolicyState{}
	for _, project := range projects {
		if project.Stage != "ready" {
			continue
		}
		st, e := p.Store.PolicyState(ctx, project.ID)
		if e != nil {
			return e
		}
		var addresses []string
		if e := json.Unmarshal(st.Addresses, &addresses); e != nil {
			return e
		}
		normalized, e := hba.Normalize(addresses)
		if e != nil {
			_ = p.Store.ApplyPolicyResult(ctx, project.ID, st.CurrentRevision, false, e.Error())
			continue
		}
		rules = append(rules, hba.Rule{Database: project.DBName, Role: project.RoleName, Addresses: normalized})
		revisions[project.ID] = st
	}
	_, applyErr := p.HBA.Apply(ctx, rules)
	for id, st := range revisions {
		if applyErr == nil {
			_ = p.Store.ApplyPolicyResult(ctx, id, st.CurrentRevision, true, "")
		} else if st.AppliedRevision != st.CurrentRevision {
			// Projects whose revision was already live keep it: the previous file was restored.
			_ = p.Store.ApplyPolicyResult(ctx, id, st.CurrentRevision, false, applyErr.Error())
		}
	}
	return applyErr
}

func (p *Provisioner) applyLimits(ctx context.Context, projectID, role string, l store.ProjectLimits) error {
	settings := postgres.LimitSettings(l.StatementTimeoutMS, l.IdleInTransactionMS, l.TempFileLimitKB, l.LockTimeoutMS)
	if e := p.PG.ApplyLimits(ctx, role, settings, l.ConnectionLimit); e != nil {
		return e
	}
	return p.Store.MarkLimitsApplied(ctx, projectID, l.Revision)
}

// SyncLimits brings every ready role's guardrails to what the operator set.
// Besides new revisions it repairs drift: the three timeouts are ordinary role
// settings a project could reset on itself. Only the limit keys are compared,
// so the write freeze and anything else on the role are left alone.
func (p *Provisioner) SyncLimits(ctx context.Context) {
	p.limits.Lock()
	defer p.limits.Unlock()
	projects, e := p.Store.ReadyProjectLimits(ctx)
	if e != nil {
		slog.Warn("role limits could not be listed", "reason", e.Error())
		return
	}
	if len(projects) == 0 {
		return
	}
	states, e := p.PG.RoleStates(ctx)
	if e != nil {
		slog.Warn("role limits could not be read", "reason", e.Error())
		return
	}
	for _, project := range projects {
		state, found := states[project.Role]
		if !found {
			continue
		}
		want := postgres.LimitSettings(project.StatementTimeoutMS, project.IdleInTransactionMS, project.TempFileLimitKB, project.LockTimeoutMS)
		var overrides []string
		for key := range want {
			if _, set := state.DatabaseSettings[key]; set {
				overrides = append(overrides, key)
			}
		}
		if len(overrides) > 0 {
			if e := p.PG.ResetDatabaseLimits(ctx, project.Role, project.Database, overrides); e != nil {
				slog.Warn("per-database overrides not removed", "project", project.ProjectID, "reason", e.Error())
			}
		}
		drifted := state.ConnectionLimit != project.ConnectionLimit
		for key, value := range want {
			if state.Settings[key] != value {
				drifted = true
			}
		}
		if !drifted {
			if project.AppliedRevision < project.Revision {
				_ = p.Store.MarkLimitsApplied(ctx, project.ProjectID, project.Revision)
			}
			continue
		}
		if e := p.applyLimits(ctx, project.ProjectID, project.Role, project.ProjectLimits); e != nil {
			slog.Warn("role limits not applied", "project", project.ProjectID, "reason", e.Error())
			_ = p.Store.LimitsFailed(ctx, project.ProjectID, "PostgreSQL did not accept the limits. Run pgfyctl diagnostics; they are retried automatically.")
		}
	}
}

// ErrRotationIncomplete means the new password is recorded but PostgreSQL has
// not confirmed it yet; the provisioner finishes the rotation on its own.
var ErrRotationIncomplete = errors.New("the password change will be finished automatically")

// Rotate issues a new password for a ready project: recorded first, then set
// on the role, then every session of the role is ended, and only then does it
// become the active credential, with its audit row in the same transaction.
func (p *Provisioner) Rotate(ctx context.Context, project store.Project, entry store.AuditEntry) (string, error) {
	lock := p.RotationLock(project.ID)
	lock.Lock()
	defer lock.Unlock()
	password := security.Token()
	sealed := p.Vault.Seal("project:"+project.ID, []byte(password))
	if e := p.Store.BeginRotation(ctx, project.ID, sealed, entry.RequestID); e != nil {
		return "", e
	}
	if e := p.finishRotation(ctx, project, password, sealed, entry); e != nil {
		slog.Warn("password rotation incomplete", "project", project.ID, "reason", e.Error())
		return "", ErrRotationIncomplete
	}
	return password, nil
}

func (p *Provisioner) finishRotation(ctx context.Context, project store.Project, password, sealed string, entry store.AuditEntry) error {
	if e := p.PG.SetPassword(ctx, project.RoleName, password); e != nil {
		return e
	}
	if e := p.PG.TerminateSessions(ctx, project.DBName, project.RoleName); e != nil {
		return e
	}
	_, e := p.Store.CompleteRotation(ctx, project.ID, sealed, entry)
	return e
}

// FinishRotations rolls interrupted rotations forward. A project whose
// rotation lock is held is being rotated right now and is left to that request.
func (p *Provisioner) FinishRotations(ctx context.Context) {
	pending, e := p.Store.PendingRotations(ctx)
	if e != nil {
		return
	}
	for _, r := range pending {
		lock := p.RotationLock(r.ProjectID)
		if !lock.TryLock() {
			continue
		}
		func() {
			defer lock.Unlock()
			// Re-read under the lock: the request that held it may have finished.
			sealed, e := p.Store.PendingPassword(ctx, r.ProjectID)
			if e != nil || sealed == "" {
				return
			}
			project, e := p.Store.Project(ctx, r.ProjectID)
			if e != nil {
				return
			}
			password, e := p.Vault.Open("project:"+r.ProjectID, sealed)
			if e != nil {
				return
			}
			entry := store.AuditEntry{At: time.Now().Unix(), Action: "credentials.rotate", Target: r.ProjectID, RequestID: r.RequestID, Detail: `{"finished":"automatically"}`}
			if e := p.finishRotation(ctx, project, string(password), sealed, entry); e != nil {
				slog.Warn("password rotation still incomplete", "project", r.ProjectID, "reason", e.Error())
			}
		}()
	}
}
