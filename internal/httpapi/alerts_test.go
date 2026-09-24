package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/c-varun14/Pgfy/internal/alerts"
)

// The webhook URL is often the credential itself; it must never come back out.
func TestWebhookURLNeverLeavesTheServer(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer receiver.Close()
	f.s.Alerts = &alerts.Engine{Store: f.s.Store, Vault: f.s.Vault, InstallationID: "installation", Mode: "tunnel", Version: "test"}
	origin := f.s.Config.Origin
	if r := f.request("PUT", "/api/v1/settings/alerts", `{"url":"http://hooks.example.com/services/T0KEN","secret":"","private_endpoint":false}`, cookie, csrf, origin); r.Code != 400 {
		t.Fatal("plain http to a public receiver accepted", r.Code)
	}
	secretURL := receiver.URL + "/services/T0KEN"
	r := f.request("PUT", "/api/v1/settings/alerts", `{"url":"`+secretURL+`","secret":"s3cret","private_endpoint":true}`, cookie, csrf, origin)
	if r.Code != 200 || strings.Contains(r.Body.String(), "T0KEN") || strings.Contains(r.Body.String(), "s3cret") {
		t.Fatal(r.Code, r.Body.String())
	}
	got := f.request("GET", "/api/v1/settings/alerts", "", cookie, "", "")
	if strings.Contains(got.Body.String(), "T0KEN") || !strings.Contains(got.Body.String(), `"has_secret":true`) {
		t.Fatal(got.Body.String())
	}
	// Saving the masked form back keeps the stored URL and secret.
	masked := f.s.Alerts
	hook, _, _ := masked.Webhook(context.Background())
	f.request("PUT", "/api/v1/settings/alerts", `{"url":"`+hook.Masked()+`","secret":"","private_endpoint":true}`, cookie, csrf, origin)
	if again, _, _ := masked.Webhook(context.Background()); again.URL != secretURL || again.Secret != "s3cret" {
		t.Fatal("the stored webhook was lost", again.Masked())
	}
	test := f.request("POST", "/api/v1/settings/alerts/test", `{}`, cookie, csrf, origin)
	if test.Code != 200 || !strings.Contains(test.Body.String(), `"ok":true`) || received.Load() != 1 {
		t.Fatal(test.Code, test.Body.String(), received.Load())
	}
	receiver.Close()
	failed := f.request("POST", "/api/v1/settings/alerts/test", `{}`, cookie, csrf, origin)
	if !strings.Contains(failed.Body.String(), `"ok":false`) || strings.Contains(failed.Body.String(), "T0KEN") {
		t.Fatal("a failed delivery leaked the URL", failed.Body.String())
	}
	entries, _ := f.s.Store.AuditEntries(context.Background(), 10)
	for _, e := range entries {
		if strings.Contains(e.Target+e.Detail, "T0KEN") || strings.Contains(e.Detail, "s3cret") {
			t.Fatal("the audit trail holds the webhook secret", e)
		}
	}
	if list := f.request("GET", "/api/v1/alerts", "", cookie, "", ""); list.Code != 200 || strings.Contains(list.Body.String(), "T0KEN") {
		t.Fatal(list.Code, list.Body.String())
	}
}
