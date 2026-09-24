package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/provision"
)

// noRoles stands in for PostgreSQL: it knows no roles, so limits are recorded
// but never reported as applied.
type noRoles struct{}

func (noRoles) EnsureRole(context.Context, string, string, int64) error { return nil }
func (noRoles) EnsureDatabase(context.Context, string, string) error    { return nil }
func (noRoles) ApplyLimits(context.Context, string, map[string]string, int64) error {
	return nil
}
func (noRoles) RoleStates(context.Context) (map[string]postgres.RoleState, error) {
	return map[string]postgres.RoleState{}, nil
}
func (noRoles) ResetDatabaseLimits(context.Context, string, string, []string) error { return nil }
func (noRoles) SetPassword(context.Context, string, string) error                   { return nil }
func (noRoles) TerminateSessions(context.Context, string, string) error             { return nil }

func (f *fixture) withProvisioner(t *testing.T) {
	f.s.Provisioner = provision.New(f.s.Store, f.s.Vault, noRoles{}, nil)
}

func TestProjectListShowsWhichDatabasesAreOpenToTheInternet(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.setup(t)
	ctx := context.Background()
	open := f.readyProject(t, "000000000001", "Open", 0)
	closed := f.readyProject(t, "000000000002", "Closed", 0)
	f.s.Store.RecordPolicyRevision(ctx, open.ID, 0, `["0.0.0.0/0","::/0"]`, f.now)
	f.s.Store.RecordPolicyRevision(ctx, closed.ID, 0, `["203.0.113.7/32"]`, f.now)
	r := f.request("GET", "/api/v1/projects", "", cookie, "", "")
	var body struct {
		Projects []struct {
			ID             string `json:"id"`
			OpenToInternet bool   `json:"open_to_internet"`
		} `json:"projects"`
	}
	json.Unmarshal(r.Body.Bytes(), &body)
	got := map[string]bool{}
	for _, p := range body.Projects {
		got[p.ID] = p.OpenToInternet
	}
	if !got[open.ID] || got[closed.ID] || len(got) != 2 {
		t.Fatal(r.Body.String())
	}
}

func TestLimitsNeedPostgreSQLManagement(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	p := f.readyProject(t, "000000000005", "Shop", 0)
	ok := `{"statement_timeout_ms":30000,"idle_in_transaction_ms":0,"temp_file_limit_kb":-1,"lock_timeout_ms":5000,"connection_limit":10,"revision":1}`
	if r := f.request("PUT", "/api/v1/projects/"+p.ID+"/limits", ok, cookie, csrf, f.s.Config.Origin); r.Code != 503 {
		t.Fatal("limits accepted with nothing to apply them", r.Code)
	}
}

func TestLimitsAreValidatedVersionedAndAudited(t *testing.T) {
	f := newFixture(t)
	f.withProvisioner(t)
	cookie, csrf := f.setup(t)
	origin := f.s.Config.Origin
	p := f.readyProject(t, "000000000003", "Shop", 0)
	path := "/api/v1/projects/" + p.ID + "/limits"
	if r := f.request("PUT", path, `{"statement_timeout_ms":60000,"idle_in_transaction_ms":0,"temp_file_limit_kb":0,"lock_timeout_ms":0,"connection_limit":10,"revision":1}`, cookie, csrf, origin); r.Code != 400 {
		t.Fatal("a zero temp_file_limit was accepted", r.Code)
	}
	ok := `{"statement_timeout_ms":30000,"idle_in_transaction_ms":0,"temp_file_limit_kb":-1,"lock_timeout_ms":5000,"connection_limit":10,"revision":1}`
	if r := f.request("PUT", path, ok, cookie, csrf, origin); r.Code != 200 || !strings.Contains(r.Body.String(), `"revision":2`) {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := f.request("PUT", path, ok, cookie, csrf, origin); r.Code != 409 {
		t.Fatal("stale revision accepted", r.Code)
	}
	entries, _ := f.s.Store.AuditEntries(context.Background(), 5)
	if len(entries) != 1 || entries[0].Action != "limits.update" || entries[0].Target != p.ID || entries[0].RequestID == "" {
		t.Fatal(entries)
	}
	if r := f.request("GET", "/api/v1/projects/"+p.ID, "", cookie, "", ""); !strings.Contains(r.Body.String(), `"connection_limit":10`) {
		t.Fatal(r.Body.String())
	}
}

func TestAccessChangesAreAuditedWithoutSecrets(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	f.s.Provisioner = nil
	p := f.readyProject(t, "000000000004", "Blog", 0)
	f.request("PUT", "/api/v1/settings/backups", `{"target_interval_hours":12,"retention_daily":14,"retention_weekly":8}`, cookie, csrf, f.s.Config.Origin)
	entries, _ := f.s.Store.AuditEntries(context.Background(), 5)
	if len(entries) != 1 || entries[0].Action != "settings.backups" {
		t.Fatal(entries)
	}
	if r := f.request("GET", "/api/v1/system/connections", "", cookie, "", ""); r.Code != 503 {
		t.Fatal("connection budget without PostgreSQL management", r.Code)
	}
	if r := f.request("POST", "/api/v1/projects/"+p.ID+"/credentials/rotate", `{}`, cookie, csrf, f.s.Config.Origin); r.Code != 503 {
		t.Fatal("rotation without PostgreSQL management", r.Code)
	}
}

func TestStatusReportsTheHost(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.setup(t)
	r := f.request("GET", "/api/v1/system/status", "", cookie, "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"host":{"state":"unknown"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}
