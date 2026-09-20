package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testProject(id string) Project {
	return Project{ID: "prj_" + id, Name: "Demo " + id, DBName: "app_" + id, RoleName: "app_" + id}
}

func TestCreateProjectIdempotent(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	p, created, e := s.CreateProject(ctx, testProject("aaa000000001"), "key-1", "sealed", now)
	if e != nil || !created {
		t.Fatal(p, created, e)
	}
	if p.Stage != "identity_persisted" {
		t.Fatal(p.Stage)
	}
	again, created, e := s.CreateProject(ctx, testProject("bbb000000002"), "key-1", "other", now)
	if e != nil || created {
		t.Fatal(again, created, e)
	}
	if again.ID != p.ID || again.DBName != p.DBName {
		t.Fatal("repeated key returned a different project", again)
	}
	if _, e = s.SealedPassword(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
}

func TestCreateProjectConcurrent(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := testProject("aaa000000001")
			_, created, e := s.CreateProject(ctx, p, "key-1", "sealed", now)
			if e != nil {
				t.Error(e)
			}
			results <- created
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	for c := range results {
		if c {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("got %d creators", created)
	}
	list, e := s.Projects(ctx)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
}

func TestProjectStagesAndRetry(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	p, _, _ := s.CreateProject(ctx, testProject("aaa000000001"), "key-1", "sealed", now)
	incomplete, e := s.IncompleteProjects(ctx)
	if e != nil || len(incomplete) != 1 {
		t.Fatal(incomplete, e)
	}
	if e = s.SetProjectStage(ctx, p.ID, "role_created", now); e != nil {
		t.Fatal(e)
	}
	if e = s.FailProject(ctx, p.ID, "role app_x exists with different attributes"); e != nil {
		t.Fatal(e)
	}
	incomplete, _ = s.IncompleteProjects(ctx)
	if len(incomplete) != 0 {
		t.Fatal("failed project still queued")
	}
	got, _ := s.Project(ctx, p.ID)
	if !got.Failed || got.StageError == "" || got.Stage != "role_created" {
		t.Fatal(got)
	}
	if e = s.RetryProject(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.RetryProject(ctx, p.ID); !errors.Is(e, ErrProjectNotFound) {
		t.Fatal("retry of non-failed project", e)
	}
	if e = s.SetProjectStage(ctx, p.ID, "ready", now); e != nil {
		t.Fatal(e)
	}
	got, _ = s.Project(ctx, p.ID)
	if got.ReadyAt == 0 {
		t.Fatal("ready_at not recorded")
	}
	incomplete, _ = s.IncompleteProjects(ctx)
	if len(incomplete) != 0 {
		t.Fatal("ready project still queued")
	}
	if e = s.SetProjectFrozen(ctx, p.ID, now); e != nil {
		t.Fatal(e)
	}
	if got, _ = s.Project(ctx, p.ID); got.FrozenAt != now.Unix() {
		t.Fatal("frozen_at not recorded", got)
	}
	if e = s.SetProjectFrozen(ctx, p.ID, time.Time{}); e != nil {
		t.Fatal(e)
	}
	if got, _ = s.Project(ctx, p.ID); got.FrozenAt != 0 {
		t.Fatal("frozen_at not cleared", got)
	}
	if e = s.SetProjectFrozen(ctx, "prj_missing", now); !errors.Is(e, ErrProjectNotFound) {
		t.Fatal("missing project", e)
	}
}

func TestPolicyRevisionOptimisticCheck(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	p, _, _ := s.CreateProject(ctx, testProject("aaa000000001"), "key-1", "sealed", now)
	rev, e := s.RecordPolicyRevision(ctx, p.ID, 0, `["203.0.113.10/32"]`, now)
	if e != nil || rev != 1 {
		t.Fatal(rev, e)
	}
	if _, e = s.RecordPolicyRevision(ctx, p.ID, 0, `[]`, now); !errors.Is(e, ErrRevisionConflict) {
		t.Fatal("stale revision accepted", e)
	}
	st, e := s.PolicyState(ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if st.State != "pending" || st.CurrentRevision != 1 || string(st.Addresses) != `["203.0.113.10/32"]` {
		t.Fatal(st)
	}
	if e = s.ApplyPolicyResult(ctx, p.ID, 1, true, ""); e != nil {
		t.Fatal(e)
	}
	st, _ = s.PolicyState(ctx, p.ID)
	if st.State != "applied" || st.AppliedRevision != 1 {
		t.Fatal(st)
	}
	if e = s.ApplyPolicyResult(ctx, p.ID, 1, false, "helper rejected"); e != nil {
		t.Fatal(e)
	}
	st, _ = s.PolicyState(ctx, p.ID)
	if st.State != "failed" || st.LastError != "helper rejected" {
		t.Fatal(st)
	}
	if _, e = s.PolicyState(ctx, "prj_missing"); !errors.Is(e, ErrProjectNotFound) {
		t.Fatal(e)
	}
}

func TestConnectionCheckLifecycle(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now()
	p, _, _ := s.CreateProject(ctx, testProject("aaa000000001"), "key-1", "sealed", now)
	if e := s.NewConnectionCheck(ctx, "chk_1", p.ID, "hash1", "ssh-tunnel||0", now); e != nil {
		t.Fatal(e)
	}
	hash, e := s.PendingChallengeHash(ctx, p.ID, "chk_1")
	if e != nil || hash != "hash1" {
		t.Fatal(hash, e)
	}
	if e = s.CompleteConnectionCheck(ctx, "chk_1", `{"transport":"direct"}`); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteConnectionCheck(ctx, "chk_1", `{"transport":"direct"}`); !errors.Is(e, ErrProjectNotFound) {
		t.Fatal("double completion", e)
	}
	got, e := s.ConnectionCheck(ctx, p.ID, "chk_1")
	if e != nil || got.State != "successful" || got.Evidence == "" {
		t.Fatal(got, e)
	}
	if e = s.NewConnectionCheck(ctx, "chk_2", p.ID, "hash2", "ssh-tunnel||0", now); e != nil {
		t.Fatal(e)
	}
	if e = s.ExpireConnectionChecks(ctx, now.Add(11*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PendingChallengeHash(ctx, p.ID, "chk_2"); !errors.Is(e, ErrProjectNotFound) {
		t.Fatal("expired check still pending", e)
	}
	got, _ = s.ConnectionCheck(ctx, p.ID, "chk_2")
	if got.State != "expired" {
		t.Fatal(got.State)
	}
}
