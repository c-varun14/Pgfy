// Package provision turns persisted project identities into real PostgreSQL
// roles and databases, resuming from the recorded stage after interruption.
package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/c-varun14/Pgfy/internal/hba"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

// DefaultAddresses is the open-by-default policy: TLS plus the project password
// from anywhere until the user restricts it.
var DefaultAddresses = []string{"0.0.0.0/0", "::/0"}

type Provisioner struct {
	Store *store.Store
	Vault *security.Vault
	PG    *postgres.Management
	HBA   *hba.Manager
	kick  chan struct{}
}

func New(s *store.Store, v *security.Vault, pg *postgres.Management, h *hba.Manager) *Provisioner {
	return &Provisioner{Store: s, Vault: v, PG: pg, HBA: h, kick: make(chan struct{}, 1)}
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
	stages := []struct {
		name string
		run  func(context.Context) error
	}{
		{"role_created", func(ctx context.Context) error { return p.PG.EnsureRole(ctx, project.RoleName, string(password)) }},
		{"database_created", func(ctx context.Context) error { return p.PG.EnsureDatabase(ctx, project.DBName, project.RoleName) }},
		{"ready", func(ctx context.Context) error {
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
