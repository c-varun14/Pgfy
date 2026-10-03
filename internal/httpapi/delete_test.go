package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/store"
)

func TestDeleteProjectConfirmsNameAndBackup(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	f.withProvisioner(t)
	target := f.withStorage(t)
	shop := f.readyProject(t, "000000000011", "Shop", 48*time.Hour)
	path := "/api/v1/projects/" + shop.ID
	if r := f.request("DELETE", path, `{"confirm_name":"shop"}`, cookie, csrf, f.s.Config.Origin); r.Code != 400 {
		t.Fatal("a name that differs in case was accepted", r.Code)
	}
	if r := f.request("DELETE", path, `{"confirm_name":"Shop"}`, cookie, csrf, f.s.Config.Origin); r.Code != 409 || !json.Valid(r.Body.Bytes()) {
		t.Fatal("deleted without a recent backup or an acknowledgment", r.Code)
	}
	// A recent recoverable backup is enough on its own.
	f.recordBackup(t, target, shop, f.now.Add(-time.Hour))
	r := f.request("DELETE", path, `{"confirm_name":"Shop"}`, cookie, csrf, f.s.Config.Origin)
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	var body struct {
		Project struct {
			Stage    string `json:"stage"`
			Deletion struct {
				Active bool `json:"active"`
			} `json:"deletion"`
		} `json:"project"`
		Job struct {
			Kind  string `json:"kind"`
			State string `json:"state"`
		} `json:"job"`
	}
	json.Unmarshal(r.Body.Bytes(), &body)
	if body.Project.Stage != "deleting" || !body.Project.Deletion.Active || body.Job.Kind != "delete" || body.Job.State != "queued" {
		t.Fatal(r.Body.String())
	}
	// Asking again while it is queued returns the same job instead of a second one.
	again := f.request("DELETE", path, `{}`, cookie, csrf, f.s.Config.Origin)
	if again.Code != 202 {
		t.Fatal(again.Code)
	}
	active, _ := f.s.Store.ActiveJobs(context.Background())
	if len(active) != 1 {
		t.Fatal("a second delete job was queued", len(active))
	}
	// Nothing else may change a project being deleted.
	limits := `{"statement_timeout_ms":30000,"idle_in_transaction_ms":0,"temp_file_limit_kb":-1,"lock_timeout_ms":5000,"connection_limit":10,"revision":1}`
	if r := f.request("PUT", path+"/limits", limits, cookie, csrf, f.s.Config.Origin); r.Code != 409 {
		t.Fatal("limits changed during deletion", r.Code)
	}
	if r := f.request("GET", path+"/credentials", "", cookie, "", ""); r.Code != 409 {
		t.Fatal("credentials served during deletion", r.Code)
	}
	entries, _ := f.s.Store.AuditEntries(context.Background(), 10)
	if len(entries) == 0 || entries[0].Action != "project.delete" || entries[0].Target != shop.ID {
		t.Fatal("deletion not audited", entries)
	}
}

func TestDeleteProjectAcknowledgedAndRefusedWhileProvisioning(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	f.withProvisioner(t)
	f.withStorage(t)
	shop := f.readyProject(t, "000000000012", "Shop", time.Hour)
	if r := f.request("DELETE", "/api/v1/projects/"+shop.ID, `{"confirm_name":"Shop","acknowledge_no_recent_backup":true}`, cookie, csrf, f.s.Config.Origin); r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	entries, _ := f.s.Store.AuditEntries(context.Background(), 1)
	if entries[0].Detail != `{"acknowledged_without_backup":true,"name":"Shop"}` {
		t.Fatal(entries[0].Detail)
	}
	pending, _, _ := f.s.Store.CreateProject(context.Background(), store.Project{ID: "prj_000000000013", Name: "New", DBName: "app_000000000013", RoleName: "app_000000000013"}, "k13", "sealed", f.now)
	if r := f.request("DELETE", "/api/v1/projects/"+pending.ID, `{"confirm_name":"New","acknowledge_no_recent_backup":true}`, cookie, csrf, f.s.Config.Origin); r.Code != 409 {
		t.Fatal("a project being set up was marked for deletion", r.Code)
	}
}
