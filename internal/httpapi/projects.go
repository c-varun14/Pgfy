package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/c-varun14/Pgfy/internal/hba"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

// databaseAccess describes how applications reach PostgreSQL right now.
type databaseAccess struct {
	Mode        string          `json:"mode"` // direct or tunnel
	Host        string          `json:"host"`
	Port        int             `json:"port"`
	Certificate json.RawMessage `json:"certificate"`
}

func (s *Server) databaseAccess() databaseAccess {
	access := databaseAccess{Mode: "tunnel", Host: "127.0.0.1", Port: 5432, Certificate: json.RawMessage(`{"state":"unknown"}`)}
	if s.Config.Mode == "https" {
		access.Mode = "direct"
		access.Host = s.Config.Hostname
	}
	if b, e := os.ReadFile(s.TLSStatePath); e == nil && json.Valid(b) {
		access.Certificate = json.RawMessage(b)
	}
	return access
}

func validProjectName(name string) bool {
	if name == "" || len(name) > 64 || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" -_.()", r)) {
			return false
		}
	}
	return true
}

func (s *Server) provisioningReady(w http.ResponseWriter) bool {
	if s.Mgmt == nil || s.Provisioner == nil {
		failure(w, 503, "postgres_unavailable", "Project provisioning is unavailable on this installation. Run host diagnostics.")
		return false
	}
	return true
}

type projectView struct {
	store.Project
	SizeBytes   *int64                `json:"size_bytes"`
	SizeError   string                `json:"size_error,omitempty"`
	Policy      *store.PolicyState    `json:"policy,omitempty"`
	Connections []postgres.Connection `json:"connections_now,omitempty"`
}

func (s *Server) projectSummary(r *http.Request, p store.Project) projectView {
	view := projectView{Project: p}
	if p.Stage == "ready" && s.Mgmt != nil {
		if size, e := s.Mgmt.DatabaseSize(r.Context(), p.DBName); e == nil {
			view.SizeBytes = &size
		} else {
			view.SizeError = "Size is unavailable while PostgreSQL cannot be reached."
		}
	}
	return view
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	projects, e := s.Store.Projects(r.Context())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Management storage is unavailable.")
		return
	}
	views := []projectView{}
	for _, p := range projects {
		views = append(views, s.projectSummary(r, p))
	}
	write(w, 200, map[string]any{"projects": views, "database_access": s.databaseAccess()})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.authorize(w, r)
	if !ok || !s.provisioningReady(w) {
		return
	}
	var in struct {
		Name           string `json:"name"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validProjectName(in.Name) {
		failure(w, 400, "invalid_name", "Use 1–64 letters, digits, spaces, dots, dashes, underscores, or parentheses.")
		return
	}
	key := in.IdempotencyKey
	if key == "" || len(key) > 128 {
		// Without a client key, the same name within one minute is one project.
		key = security.Hash(session.Email + "|" + in.Name + "|" + s.Now().Truncate(time.Minute).Format(time.RFC3339))
	}
	suffix := hex.EncodeToString([]byte(security.Token()))[:12]
	project := store.Project{ID: "prj_" + suffix, Name: in.Name, DBName: "app_" + suffix, RoleName: "app_" + suffix}
	password := security.Token()
	created, wasCreated, e := s.Store.CreateProject(r.Context(), project, key, s.Vault.Seal("project:"+project.ID, []byte(password)), s.Now())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Project could not be saved. Run host diagnostics.")
		return
	}
	s.Provisioner.Kick()
	status := 202
	if !wasCreated {
		status = 200
	}
	write(w, status, s.projectSummary(r, created))
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	p, e := s.Store.Project(r.Context(), r.PathValue("id"))
	if errors.Is(e, store.ErrProjectNotFound) {
		failure(w, 404, "not_found", "Project not found.")
		return p, false
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Management storage is unavailable.")
		return p, false
	}
	return p, true
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	view := s.projectSummary(r, p)
	if st, e := s.Store.PolicyState(r.Context(), p.ID); e == nil {
		view.Policy = &st
	}
	if p.Stage == "ready" && s.Mgmt != nil {
		if connections, e := s.Mgmt.Connections(r.Context(), p.DBName, p.RoleName); e == nil {
			view.Connections = connections
		}
	}
	write(w, 200, map[string]any{"project": view, "database_access": s.databaseAccess()})
}

func (s *Server) retryProject(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.provisioningReady(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if e := s.Store.RetryProject(r.Context(), p.ID); e != nil {
		failure(w, 409, "not_failed", "Only failed projects can be retried.")
		return
	}
	s.Provisioner.Kick()
	w.WriteHeader(204)
}

type connectionDetails struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"sslmode"`
	URL      string `json:"url"`
	Psql     string `json:"psql"`
}

func (s *Server) projectCredentials(r *http.Request, p store.Project) (connectionDetails, error) {
	sealed, e := s.Store.SealedPassword(r.Context(), p.ID)
	if e != nil {
		return connectionDetails{}, e
	}
	password, e := s.Vault.Open("project:"+p.ID, sealed)
	if e != nil {
		return connectionDetails{}, e
	}
	access := s.databaseAccess()
	c := connectionDetails{Host: access.Host, Port: access.Port, Database: p.DBName, User: p.RoleName, Password: string(password), SSLMode: "verify-full"}
	if access.Mode == "tunnel" {
		// The SSH tunnel already encrypts the hop; PostgreSQL TLS cannot be verified
		// against a loopback hostname and some drivers refuse the placeholder cert.
		c.SSLMode = "disable"
	}
	c.URL = fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=%s", url.PathEscape(c.User), url.PathEscape(c.Password), c.Host, c.Port, c.Database, c.SSLMode)
	c.Psql = fmt.Sprintf("psql %q", c.libpqURL())
	return c, nil
}

// libpqURL adds sslrootcert=system for libpq-based clients (psql, psycopg, Ruby),
// which otherwise look for ~/.postgresql/root.crt under verify-full. Drivers such
// as node-postgres, pgx and JDBC use the system store on their own and would
// misread the parameter as a file path, so the plain URL stays driver-neutral.
func (c connectionDetails) libpqURL() string {
	if c.SSLMode == "verify-full" {
		return c.URL + "&sslrootcert=system"
	}
	return c.URL
}

func (s *Server) getCredentials(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if p.Stage != "ready" {
		failure(w, 409, "not_ready", "Credentials become available once the database is ready.")
		return
	}
	c, e := s.projectCredentials(r, p)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Stored credentials could not be read.")
		return
	}
	write(w, 200, c)
}

func (s *Server) updateAccess(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok || !s.provisioningReady(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var in struct {
		Revision  int      `json:"revision"`
		Addresses []string `json:"addresses"`
	}
	if !decode(w, r, &in) {
		return
	}
	normalized, e := hba.Normalize(in.Addresses)
	if e != nil {
		failure(w, 400, "invalid_addresses", e.Error())
		return
	}
	encoded, _ := json.Marshal(normalized)
	if _, e := s.Store.RecordPolicyRevision(r.Context(), p.ID, in.Revision, string(encoded), s.Now()); e != nil {
		if errors.Is(e, store.ErrRevisionConflict) {
			failure(w, 409, "revision_conflict", "The access rules changed elsewhere. Reload and try again.")
			return
		}
		failure(w, 503, "metadata_unavailable", "Access rules could not be saved.")
		return
	}
	if p.Stage == "ready" {
		_ = s.Provisioner.SyncPolicy(r.Context())
	}
	st, e := s.Store.PolicyState(r.Context(), p.ID)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Access rules could not be read back.")
		return
	}
	write(w, 200, st)
}

func (s *Server) createConnectionCheck(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if p.Stage != "ready" {
		failure(w, 409, "not_ready", "The database is not ready yet.")
		return
	}
	c, e := s.projectCredentials(r, p)
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Stored credentials could not be read.")
		return
	}
	token := security.Token()[:24]
	id := "chk_" + security.Token()[:16]
	applicationName := "pgfy-check-" + token
	if e := s.Store.NewConnectionCheck(r.Context(), id, p.ID, security.Hash(applicationName), s.databaseAccess().Mode, s.Now()); e != nil {
		failure(w, 503, "metadata_unavailable", "Connection check could not be saved.")
		return
	}
	command := fmt.Sprintf("psql %q -c \"select pg_sleep(20)\"", c.libpqURL()+"&application_name="+applicationName)
	write(w, 201, map[string]any{"id": id, "expires_at": s.Now().Add(10 * time.Minute).Unix(), "application_name": applicationName, "command": command})
}

func (s *Server) getConnectionCheck(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	_ = s.Store.ExpireConnectionChecks(r.Context(), s.Now())
	checkID := r.PathValue("check")
	if hash, e := s.Store.PendingChallengeHash(r.Context(), p.ID, checkID); e == nil && s.Mgmt != nil {
		connections, e := s.Mgmt.Connections(r.Context(), p.DBName, p.RoleName)
		if e == nil {
			for _, c := range connections {
				if security.Hash(c.ApplicationName) == hash {
					evidence, _ := json.Marshal(map[string]any{"client_addr": c.ClientAddr, "tls": c.TLS, "observed_at": s.Now().Unix()})
					_ = s.Store.CompleteConnectionCheck(r.Context(), checkID, string(evidence))
					break
				}
			}
		}
	}
	check, e := s.Store.ConnectionCheck(r.Context(), p.ID, checkID)
	if errors.Is(e, store.ErrProjectNotFound) {
		failure(w, 404, "not_found", "Connection check not found.")
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Connection check could not be read.")
		return
	}
	out := map[string]any{"id": check.ID, "state": check.State, "expires_at": check.ExpiresAt, "created_at": check.CreatedAt}
	if check.Evidence != "" {
		out["evidence"] = json.RawMessage(check.Evidence)
	}
	write(w, 200, out)
}
