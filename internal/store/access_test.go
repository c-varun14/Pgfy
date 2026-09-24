package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestAuditIsAppendOnly(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if e := s.Audit(ctx, AuditEntry{At: 1, AdminID: 1, Action: "limits.update", Target: "prj_x", RequestID: "r1"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec("UPDATE audit SET action='nothing'"); e == nil || !strings.Contains(e.Error(), "append-only") {
		t.Fatal("audit row updated", e)
	}
	if _, e := s.DB.Exec("DELETE FROM audit"); e == nil {
		t.Fatal("audit row deleted")
	}
	entries, _ := s.AuditEntries(ctx, 5)
	if len(entries) != 1 || entries[0].Action != "limits.update" || entries[0].RequestID != "r1" {
		t.Fatal(entries)
	}
}

// An installation updated from the previous release gets limits for every
// project it already has, matching the connection limit its roles were created with.
func TestAccessMigrationBackfillsExistingProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	previous := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrationSources[0], ".")
	for _, entry := range entries {
		if entry.Name() < "006" {
			body, _ := fs.ReadFile(migrationSources[0], entry.Name())
			previous[entry.Name()] = &fstest.MapFile{Data: body}
		}
	}
	saved := migrationSources
	migrationSources = []fs.FS{previous}
	old, e := Open(path)
	migrationSources = saved
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"prj_a", "prj_b"} {
		if _, e := old.DB.Exec("INSERT INTO projects(id,name,db_name,role_name,idempotency_key,created_at) VALUES (?,?,?,?,?,1)", id, id, "db_"+id, "role_"+id, "key_"+id); e != nil {
			t.Fatal(e)
		}
	}
	old.DB.Close()
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	for _, id := range []string{"prj_a", "prj_b"} {
		l, e := s.ProjectLimits(context.Background(), id)
		if e != nil || l.Limits != DefaultLimits || l.Revision != 1 || l.AppliedRevision != 0 {
			t.Fatal(id, l, e)
		}
	}
}

func TestLimitsValidationAndRevisions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p := ready(t, s, "prj_l", time.Unix(1_800_000_000, 0))
	for _, bad := range []Limits{
		{StatementTimeoutMS: 500, TempFileLimitKB: -1, ConnectionLimit: 5},
		{TempFileLimitKB: 0, ConnectionLimit: 5}, // would fail every spilling sort
		{TempFileLimitKB: -1, ConnectionLimit: 0},
		{TempFileLimitKB: -1, ConnectionLimit: 5, LockTimeoutMS: 90_000_000},
	} {
		if bad.Validate() == nil {
			t.Fatal("accepted", bad)
		}
	}
	current, _ := s.ProjectLimits(ctx, p.ID)
	next := Limits{TempFileLimitKB: -1, ConnectionLimit: 10}
	updated, e := s.SetProjectLimits(ctx, p.ID, next, current.Revision, time.Now())
	if e != nil || updated.Limits != next || updated.Revision != current.Revision+1 {
		t.Fatal(updated, e)
	}
	if _, e := s.SetProjectLimits(ctx, p.ID, next, current.Revision, time.Now()); !errors.Is(e, ErrRevisionConflict) {
		t.Fatal("stale revision accepted", e)
	}
}
