package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/provision"
	"github.com/c-varun14/Pgfy/internal/store"
)

// auditEntry describes an action by the single administrator. Detail must
// never contain a secret.
func (s *Server) auditEntry(w http.ResponseWriter, action, target string, detail any) store.AuditEntry {
	encoded := ""
	if detail != nil {
		b, _ := json.Marshal(detail)
		encoded = string(b)
	}
	return store.AuditEntry{At: s.Now().Unix(), AdminID: 1, Action: action, Target: target, RequestID: w.Header().Get("X-Request-ID"), Detail: encoded}
}

// audit records an action that already committed. A failure to record is
// logged, never reported as a failure of the action itself.
func (s *Server) audit(w http.ResponseWriter, r *http.Request, action, target string, detail any) {
	if e := s.Store.Audit(r.Context(), s.auditEntry(w, action, target, detail)); e != nil {
		slog.Error("audit record not written", "action", action, "request_id", w.Header().Get("X-Request-ID"))
	}
}

func openToInternet(addresses []string) bool {
	for _, a := range addresses {
		if a == "0.0.0.0/0" || a == "::/0" {
			return true
		}
	}
	return false
}

func (s *Server) putLimits(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	if s.Provisioner == nil {
		failure(w, 503, "postgres_unavailable", "Project provisioning is unavailable on this installation. Run host diagnostics.")
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var in struct {
		store.Limits
		Revision int64 `json:"revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if e := in.Limits.Validate(); e != nil {
		failure(w, 400, "invalid_limits", e.Error())
		return
	}
	limits, e := s.Store.SetProjectLimits(r.Context(), p.ID, in.Limits, in.Revision, s.Now())
	if errors.Is(e, store.ErrRevisionConflict) {
		failure(w, 409, "revision_conflict", "The limits changed elsewhere. Reload and try again.")
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Limits could not be saved.")
		return
	}
	s.audit(w, r, "limits.update", p.ID, in.Limits)
	if p.Stage == "ready" {
		s.Provisioner.SyncLimits(r.Context())
		if current, e := s.Store.ProjectLimits(r.Context(), p.ID); e == nil {
			limits = current
		}
	}
	write(w, 200, limits)
}

func (s *Server) rotateCredentials(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.provisioningReady(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if p.Stage != "ready" || p.Failed {
		failure(w, 409, "not_ready", "Passwords can be changed once the database is ready.")
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	password, e := s.Provisioner.Rotate(ctx, p, s.auditEntry(w, "credentials.rotate", p.ID, nil))
	if e != nil {
		if errors.Is(e, provision.ErrRotationIncomplete) {
			failure(w, 502, "rotation_incomplete", "PostgreSQL did not confirm the new password yet. It will be finished automatically; the new password is shown once it is active.")
			return
		}
		failure(w, 503, "metadata_unavailable", "The password change could not be recorded; nothing changed.")
		return
	}
	// Built from the password this request set, never read back: a later
	// rotation may already be replacing it.
	write(w, 200, map[string]any{"credentials": s.connectionDetails(p, password), "rotated_at": s.Now().Unix()})
}

func (s *Server) connectionBudget(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	if s.Mgmt == nil {
		failure(w, 503, "postgres_unavailable", "PostgreSQL management is unavailable. Run host diagnostics.")
		return
	}
	b, e := s.Mgmt.ConnectionBudget(r.Context())
	if e != nil {
		failure(w, 503, "postgres_unavailable", "Connection use could not be read from PostgreSQL.")
		return
	}
	global, _ := b.Warnings()
	names := map[string]string{}
	if projects, e := s.Store.Projects(r.Context()); e == nil {
		for _, p := range projects {
			names[p.RoleName] = p.Name
		}
	}
	type roleView struct {
		postgres.RoleUse
		Project string `json:"project,omitempty"`
		Warning bool   `json:"warning"`
	}
	view := func(list []postgres.RoleUse) []roleView {
		out := []roleView{}
		for _, u := range list {
			// Warn at 80% of a role's own limit; -1 is unlimited.
			out = append(out, roleView{RoleUse: u, Project: names[u.Role], Warning: postgres.NearLimit(u.Connections, u.Limit)})
		}
		return out
	}
	write(w, 200, map[string]any{
		"max_connections": b.MaxConnections, "superuser_reserved": b.SuperuserReserved, "reserved": b.Reserved,
		"available": b.Available, "projects_used": b.ProjectsUsed, "projects_limit": b.ProjectsLimit, "other_used": b.OtherUsed,
		// Project roles compete for the ordinary slots; the system roles can use the reserved ones.
		"warning":       global,
		"overcommitted": b.ProjectsLimit > b.Available,
		"roles":         view(b.Roles),
		"system":        view(b.System),
	})
}
