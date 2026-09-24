package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/c-varun14/Pgfy/internal/config"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

type fixture struct {
	s     *Server
	h     http.Handler
	token string
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "metadata.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	v, _ := security.NewVault(bytes.Repeat([]byte{1}, 32))
	f := &fixture{now: time.Now()}
	f.token, e = s.NewSetupToken(context.Background(), f.now)
	if e != nil {
		t.Fatal(e)
	}
	f.s = &Server{Config: config.Installation{ID: "installation", Mode: "tunnel", Origin: "http://127.0.0.1:8080", Generation: "1"}, Store: s, Vault: v, Now: func() time.Time { return f.now }, PG: func(context.Context) (string, error) { return "18.6", nil }, Assets: fs.FS(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>dashboard</html>")}})}
	f.h = f.s.Handler()
	return f
}
func (f *fixture) request(method, path, body string, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	f.h.ServeHTTP(out, req)
	return out
}
func (f *fixture) setup(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": "Admin@example.com", "password": "a sufficiently long passphrase", "token": f.token})
	res := f.request("POST", "/api/v1/setup", string(body), nil, "", f.s.Config.Origin)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	var data map[string]string
	json.Unmarshal(res.Body.Bytes(), &data)
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("cookie missing")
	}
	return cookies[0], data["csrf_token"]
}
func TestAuthenticationFlowAndDegradedHealth(t *testing.T) {
	f := newFixture(t)
	if r := f.request("GET", "/api/v1/settings", "", nil, "", ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	cookie, csrf := f.setup(t)
	if cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("bad tunnel cookie", cookie)
	}
	if r := f.request("GET", "/api/v1/auth/session", "", cookie, "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), "admin@example.com") {
		t.Fatal(r.Code)
	}
	f.s.PG = func(context.Context) (string, error) { return "", errors.New("secret internal database error") }
	if r := f.request("GET", "/health/ready", "", nil, "", ""); r.Code != 503 || strings.Contains(r.Body.String(), "secret") {
		t.Fatal("readiness not redacted")
	}
	if r := f.request("GET", "/health/live", "", nil, "", ""); r.Code != 200 {
		t.Fatal("liveness coupled to postgres")
	}
	if r := f.request("GET", "/", "", nil, "", ""); r.Code != 200 {
		t.Fatal("dashboard inaccessible")
	}
	if r := f.request("GET", "/api/v1/system/status", "", cookie, "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), "unavailable") {
		t.Fatal("degradation not visible")
	}
	if r := f.request("POST", "/api/v1/auth/logout", "{}", cookie, "", f.s.Config.Origin); r.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if r := f.request("POST", "/api/v1/auth/logout", "{}", cookie, csrf, "https://evil.example"); r.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	if r := f.request("POST", "/api/v1/auth/logout", "{}", cookie, csrf, f.s.Config.Origin); r.Code != 204 {
		t.Fatal(r.Code)
	}
	if r := f.request("GET", "/api/v1/auth/session", "", cookie, "", ""); r.Code != 401 {
		t.Fatal("logout not revoked")
	}
	login := `{"email":"ADMIN@example.com","password":"a sufficiently long passphrase"}`
	r := f.request("POST", "/api/v1/auth/login", login, nil, "", f.s.Config.Origin)
	if r.Code != 200 {
		t.Fatal("login depends on postgres", r.Code)
	}
	cookie = r.Result().Cookies()[0]
	f.now = f.now.Add(12 * time.Hour)
	if r = f.request("GET", "/api/v1/auth/session", "", cookie, "", ""); r.Code != 401 {
		t.Fatal("expired session accepted")
	}
}
func TestFailureBoundariesAndScope(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.setup(t)
	f.s.Config.Generation = "new"
	if r := f.request("GET", "/api/v1/auth/session", "", cookie, "", ""); r.Code != 401 {
		t.Fatal("session survived origin generation change")
	}
	f.s.Config.Generation = "1"
	f.s.Store.DB.Close()
	if r := f.request("GET", "/health/ready", "", nil, "", ""); r.Code != 503 {
		t.Fatal("SQLite failure reported ready")
	}
	if r := f.request("GET", "/api/v1/auth/session", "", cookie, "", ""); r.Code != 503 {
		t.Fatal("SQLite failure not surfaced")
	}
	f.s.Store = nil
	if r := f.request("GET", "/api/v1/setup", "", nil, "", ""); r.Code != 503 {
		t.Fatal("migration failure permitted setup")
	}
	if r := f.request("GET", "/settings", "", nil, "", ""); r.Code != 200 {
		t.Fatal("shell unavailable")
	}
}
func TestOriginRateLimitsAndProxySpoofing(t *testing.T) {
	f := newFixture(t)
	if r := f.request("POST", "/api/v1/auth/login", "{}", nil, "", ""); r.Code != 403 {
		t.Fatal("missing origin accepted")
	}
	for i := 0; i < 10; i++ {
		r := f.request("POST", "/api/v1/auth/login", "bad JSON", nil, "", f.s.Config.Origin)
		if r.Code != 400 {
			t.Fatal(r.Code)
		}
	}
	if r := f.request("POST", "/api/v1/auth/login", "{}", nil, "", f.s.Config.Origin); r.Code != 429 {
		t.Fatal("rate limit not enforced")
	}
	f.s.TrustedProxy = netip.MustParsePrefix("172.20.1.0/24")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:2000"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	if f.s.client(req) != "192.0.2.1" {
		t.Fatal("spoofed forwarding header trusted")
	}
	req.RemoteAddr = "172.20.1.2:2000"
	if f.s.client(req) != "203.0.113.1" {
		t.Fatal("trusted proxy not used")
	}
}
func TestHTTPSCookieAndNoTokenLeak(t *testing.T) {
	f := newFixture(t)
	f.s.Config.Mode = "https"
	f.s.Config.Hostname = "db.example.com"
	f.s.Config.Origin = "https://db.example.com"
	w := httptest.NewRecorder()
	f.s.setCookie(w, "random")
	c := w.Result().Cookies()[0]
	if c.Name != "__Host-pgfy_session" || !c.Secure || c.Domain != "" {
		t.Fatal("bad HTTPS cookie")
	}
	f.s.Config.Mode = "tunnel"
	f.s.Config.Origin = "http://127.0.0.1:8080"
	f.setup(t)
	for _, table := range []string{"setup_token", "sessions"} {
		var n int
		query := "SELECT count(*) FROM " + table + " WHERE token_hash=?"
		if e := f.s.Store.DB.QueryRow(query, f.token).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != 0 {
			t.Fatal("plaintext token persisted")
		}
	}
}

func TestMaintenanceRefusesWritesButNotSignIn(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.setup(t)
	if e := f.s.Store.SetMaintenance(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	origin := f.s.Config.Origin
	for _, path := range []string{"/api/v1/projects", "/api/v1/recovery/restores", "/api/v1/settings/backups", "/api/v1/auth/login/../../projects"} {
		if r := f.request("POST", path, `{}`, cookie, csrf, origin); r.Code != 503 || !strings.Contains(r.Body.String(), "maintenance") {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	if r := f.request("GET", "/api/v1/system/status", "", cookie, "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"maintenance":true`) {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := f.request("POST", "/api/v1/auth/logout", `{}`, cookie, csrf, origin); r.Code == 503 {
		t.Fatal("sign-out refused during maintenance")
	}
	body := `{"email":"admin@example.com","password":"a sufficiently long passphrase"}`
	if r := f.request("POST", "/api/v1/auth/login", body, nil, "", origin); r.Code != 200 {
		t.Fatal("sign-in refused during maintenance", r.Code, r.Body.String())
	}
}
