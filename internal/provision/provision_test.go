package provision

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

// fakeRoles models the role settings and passwords PostgreSQL would hold.
type fakeRoles struct {
	mu          sync.Mutex
	states      map[string]postgres.RoleState
	passwords   map[string]string
	terminated  []string
	failSetPass error
	beforeSet   func() // runs inside SetPassword before it takes effect
	applies     int
}

func newFakeRoles() *fakeRoles {
	return &fakeRoles{states: map[string]postgres.RoleState{}, passwords: map[string]string{}}
}
func (f *fakeRoles) EnsureRole(ctx context.Context, name, password string, limit int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passwords[name] = password
	if _, ok := f.states[name]; !ok {
		f.states[name] = postgres.RoleState{Settings: map[string]string{}, ConnectionLimit: limit, DatabaseSettings: map[string]string{}}
	}
	return nil
}
func (f *fakeRoles) EnsureDatabase(ctx context.Context, name, owner string) error { return nil }
func (f *fakeRoles) ResetDatabaseLimits(ctx context.Context, role, database string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range keys {
		delete(f.states[role].DatabaseSettings, key)
	}
	return nil
}
func (f *fakeRoles) ApplyLimits(ctx context.Context, role string, settings map[string]string, limit int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies++
	state := f.states[role]
	for k, v := range settings {
		state.Settings[k] = v
	}
	state.ConnectionLimit = limit
	f.states[role] = state
	return nil
}
func (f *fakeRoles) RoleStates(ctx context.Context) (map[string]postgres.RoleState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]postgres.RoleState{}
	for k, v := range f.states {
		copied := map[string]string{}
		for a, b := range v.Settings {
			copied[a] = b
		}
		database := map[string]string{}
		for a, b := range v.DatabaseSettings {
			database[a] = b
		}
		out[k] = postgres.RoleState{Settings: copied, ConnectionLimit: v.ConnectionLimit, DatabaseSettings: database}
	}
	return out, nil
}
func (f *fakeRoles) SetPassword(ctx context.Context, role, password string) error {
	if f.beforeSet != nil {
		f.beforeSet()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSetPass != nil {
		return f.failSetPass
	}
	f.passwords[role] = password
	return nil
}
func (f *fakeRoles) TerminateSessions(ctx context.Context, database, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, role)
	return nil
}

func fixture(t *testing.T) (*Provisioner, *fakeRoles, store.Project) {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "metadata.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	v, _ := security.NewVault(bytes.Repeat([]byte{7}, 32))
	roles := newFakeRoles()
	p := &Provisioner{Store: s, Vault: v, PG: roles, kick: make(chan struct{}, 1)}
	project := store.Project{ID: "prj_000000000001", Name: "Shop", DBName: "app_000000000001", RoleName: "app_000000000001"}
	if _, _, e := s.CreateProject(context.Background(), project, "key", v.Seal("project:"+project.ID, []byte(security.Token())), time.Now()); e != nil {
		t.Fatal(e)
	}
	for _, stage := range []string{"role_created", "database_created"} {
		if e := s.SetProjectStage(context.Background(), project.ID, stage, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	project.Stage = "database_created"
	if e := p.provision(context.Background(), store.Project{ID: project.ID, RoleName: project.RoleName, DBName: project.DBName, Stage: "identity_persisted"}); e != nil {
		t.Fatal(e)
	}
	project.Stage = "ready"
	return p, roles, project
}

func password(t *testing.T, p *Provisioner, id string) string {
	t.Helper()
	sealed, e := p.Store.SealedPassword(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := p.Vault.Open("project:"+id, sealed)
	if e != nil {
		t.Fatal(e)
	}
	return string(plain)
}

func TestReadyProjectsServeWithGuardrailsAndDriftIsRepaired(t *testing.T) {
	p, roles, project := fixture(t)
	ctx := context.Background()
	state := roles.states[project.RoleName]
	if state.Settings["statement_timeout"] != "60000ms" || state.Settings["temp_file_limit"] != "1048576kB" || state.ConnectionLimit != 25 {
		t.Fatal("a ready project lacks its guardrails", state)
	}
	// The project resets a timeout on itself; the write freeze shares the same settings.
	roles.states[project.RoleName].Settings["statement_timeout"] = "0"
	roles.states[project.RoleName].Settings["default_transaction_read_only"] = "on"
	p.SyncLimits(ctx)
	state = roles.states[project.RoleName]
	if state.Settings["statement_timeout"] != "60000ms" {
		t.Fatal("drift was not repaired")
	}
	if state.Settings["default_transaction_read_only"] != "on" {
		t.Fatal("repairing limits undid the write freeze")
	}
	// A per-database override would win over the role-wide guardrail.
	roles.states[project.RoleName].DatabaseSettings["lock_timeout"] = "0"
	roles.states[project.RoleName].DatabaseSettings["search_path"] = "app"
	p.SyncLimits(ctx)
	if _, set := roles.states[project.RoleName].DatabaseSettings["lock_timeout"]; set {
		t.Fatal("a per-database override of a limit survived")
	}
	if roles.states[project.RoleName].DatabaseSettings["search_path"] != "app" {
		t.Fatal("an unrelated per-database setting was removed")
	}
	applied := roles.applies
	p.SyncLimits(ctx)
	if roles.applies != applied {
		t.Fatal("limits re-applied without drift")
	}
	l, _ := p.Store.ProjectLimits(ctx, project.ID)
	changed := l.Limits
	changed.TempFileLimitKB, changed.ConnectionLimit = -1, 40
	if _, e := p.Store.SetProjectLimits(ctx, project.ID, changed, l.Revision, time.Now()); e != nil {
		t.Fatal(e)
	}
	p.SyncLimits(ctx)
	state = roles.states[project.RoleName]
	after, _ := p.Store.ProjectLimits(ctx, project.ID)
	if state.Settings["temp_file_limit"] != "-1" || state.ConnectionLimit != 40 || after.AppliedRevision != after.Revision {
		t.Fatal("a new revision was not applied", state, after)
	}
}

func TestRotationEndsSessionsBeforeTheNewPasswordIsActive(t *testing.T) {
	p, roles, project := fixture(t)
	ctx := context.Background()
	old := password(t, p, project.ID)
	entry := store.AuditEntry{At: 1, AdminID: 1, Action: "credentials.rotate", Target: project.ID, RequestID: "req-1"}
	fresh, e := p.Rotate(ctx, project, entry)
	if e != nil {
		t.Fatal(e)
	}
	if fresh == old || password(t, p, project.ID) != fresh || roles.passwords[project.RoleName] != fresh {
		t.Fatal("rotation did not leave one consistent password")
	}
	if len(roles.terminated) != 1 {
		t.Fatal("sessions were not ended")
	}
	entries, _ := p.Store.AuditEntries(ctx, 10)
	if len(entries) != 1 || entries[0].RequestID != "req-1" || strings.Contains(entries[0].Detail, fresh) {
		t.Fatal("audit row missing or holds the secret", entries)
	}
}

func TestInterruptedRotationIsFinishedNotLost(t *testing.T) {
	p, roles, project := fixture(t)
	ctx := context.Background()
	old := password(t, p, project.ID)
	// The server may have committed the change even though the call failed.
	roles.failSetPass = errors.New("timeout")
	if _, e := p.Rotate(ctx, project, store.AuditEntry{RequestID: "req-2"}); !errors.Is(e, ErrRotationIncomplete) {
		t.Fatal("expected an incomplete rotation", e)
	}
	if pending, _ := p.Store.PendingPassword(ctx, project.ID); pending == "" {
		t.Fatal("the new password was dropped")
	}
	if password(t, p, project.ID) != old {
		t.Fatal("an unconfirmed password became active")
	}
	roles.failSetPass = nil
	p.FinishRotations(ctx)
	now := password(t, p, project.ID)
	if now == old || roles.passwords[project.RoleName] != now || len(roles.terminated) != 1 {
		t.Fatal("the rotation was not rolled forward")
	}
	if pending, _ := p.Store.PendingPassword(ctx, project.ID); pending != "" {
		t.Fatal("pending password left behind")
	}
	entries, _ := p.Store.AuditEntries(ctx, 10)
	if len(entries) != 1 || entries[0].RequestID != "req-2" || !strings.Contains(entries[0].Detail, "automatically") {
		t.Fatal("finishing did not record the original request", entries)
	}
}

// The finisher and a new rotate request must never interleave: the role would
// end up enforcing one password while the dashboard holds another.
func TestFinisherAndRotateNeverInterleave(t *testing.T) {
	p, roles, project := fixture(t)
	ctx := context.Background()
	roles.failSetPass = errors.New("timeout")
	p.Rotate(ctx, project, store.AuditEntry{})
	roles.failSetPass = nil
	inRotate := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	roles.beforeSet = func() { once.Do(func() { close(inRotate); <-release }) }
	done := make(chan string)
	go func() {
		fresh, e := p.Rotate(ctx, project, store.AuditEntry{})
		if e != nil {
			t.Error(e)
		}
		done <- fresh
	}()
	<-inRotate
	p.FinishRotations(ctx) // the lock is held: must skip, not apply the older pending password
	close(release)
	fresh := <-done
	if password(t, p, project.ID) != fresh || roles.passwords[project.RoleName] != fresh {
		t.Fatal("dashboard and PostgreSQL disagree about the password")
	}
}

func TestMaintenanceSkipsRotationsAndLimits(t *testing.T) {
	p, roles, project := fixture(t)
	ctx := context.Background()
	roles.failSetPass = errors.New("timeout")
	p.Rotate(ctx, project, store.AuditEntry{})
	roles.failSetPass = nil
	roles.states[project.RoleName].Settings["statement_timeout"] = "0"
	p.Store.SetMaintenance(ctx, true)
	p.pass(ctx)
	if pending, _ := p.Store.PendingPassword(ctx, project.ID); pending == "" {
		t.Fatal("a rotation was finished during maintenance")
	}
	if roles.states[project.RoleName].Settings["statement_timeout"] != "0" {
		t.Fatal("limits were applied during maintenance")
	}
}
