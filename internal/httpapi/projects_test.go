package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectRoutesRequireAuthAndProvisioning(t *testing.T) {
	f := newFixture(t)
	if r := f.request("GET", "/api/v1/projects", "", nil, "", ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	cookie, csrf := f.setup(t)
	r := f.request("GET", "/api/v1/projects", "", cookie, "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"projects":[]`) || !strings.Contains(r.Body.String(), `"mode":"tunnel"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	// This fixture has no management connection: creation must refuse instead of queueing silently.
	r = f.request("POST", "/api/v1/projects", `{"name":"Shop"}`, cookie, csrf, f.s.Config.Origin)
	if r.Code != 503 || !strings.Contains(r.Body.String(), "postgres_unavailable") {
		t.Fatal(r.Code, r.Body.String())
	}
	if r = f.request("POST", "/api/v1/projects", `{"name":"Shop"}`, cookie, "", f.s.Config.Origin); r.Code != 403 {
		t.Fatal("missing CSRF accepted", r.Code)
	}
	if r = f.request("GET", "/api/v1/projects/prj_missing", "", cookie, "", ""); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = f.request("GET", "/api/v1/auth/session", "", cookie, "", "")
	var session map[string]any
	json.Unmarshal(r.Body.Bytes(), &session)
	if session["client_ip"] == "" {
		t.Fatal(r.Body.String())
	}
}

func TestProjectNameValidation(t *testing.T) {
	for _, ok := range []string{"Shop", "my-app_1.0", "Café orders", "Shop (recovered)"} {
		if !validProjectName(ok) {
			t.Fatal("rejected", ok)
		}
	}
	for _, bad := range []string{"", " padded", strings.Repeat("a", 65), "semi;colon", "new\nline", "slash/y"} {
		if validProjectName(bad) {
			t.Fatal("accepted", bad)
		}
	}
}
