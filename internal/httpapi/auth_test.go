package httpapi

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/security"
)

func httpsFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.s.Config.Mode, f.s.Config.Hostname, f.s.Config.Origin = "https", "dash.example.com", "https://dash.example.com"
	return f
}

func (f *fixture) post(path, body string, cookies ...*http.Cookie) (int, map[string]any, []*http.Cookie) {
	req := strings.NewReader(body)
	r := f.requestWith("POST", path, req, cookies, "")
	var out map[string]any
	json.Unmarshal(r.Body.Bytes(), &out)
	return r.Code, out, r.Result().Cookies()
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name && c.MaxAge >= 0 && c.Value != "" {
			return c
		}
	}
	return nil
}

func code(t *testing.T, key string, now time.Time) string {
	secret, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(key, " ", ""))
	if e != nil {
		t.Fatal(e)
	}
	return security.TOTPCode(secret, security.TOTPStep(now))
}

// In HTTPS mode no administrator ever exists, and no session is ever issued, without a second factor.
func TestHTTPSSetupAndSignInRequireASecondFactor(t *testing.T) {
	f := httpsFixture(t)
	body := `{"email":"admin@example.com","password":"a sufficiently long passphrase","token":"` + f.token + `"}`
	status, view, cookies := f.post("/api/v1/setup", body)
	enrol := cookieNamed(cookies, "__Host-pgfy_enrol")
	if status != 200 || view["next"] != "enrol" || enrol == nil || !enrol.Secure || !enrol.HttpOnly || cookieNamed(cookies, "__Host-pgfy_session") != nil {
		t.Fatal("setup must enrol before any session", status, view)
	}
	if available, _ := f.s.Store.SetupAvailable(context.Background()); !available {
		t.Fatal("the administrator exists before the factor is confirmed")
	}
	key := view["key"].(string)
	if status, _, _ := f.post("/api/v1/auth/enrol/confirm", `{"code":"000000"}`, enrol); status != 401 {
		t.Fatal("a wrong code confirmed", status)
	}
	status, signed, cookies := f.post("/api/v1/auth/enrol/confirm", `{"code":"`+code(t, key, f.now)+`"}`, enrol)
	if status != 200 || signed["csrf_token"] == "" || cookieNamed(cookies, "__Host-pgfy_session") == nil {
		t.Fatal("confirmation did not sign in", status, signed)
	}
	// Sign in again: the password step alone yields no session.
	f.now = f.now.Add(time.Minute)
	status, step, cookies := f.post("/api/v1/auth/login", `{"email":"admin@example.com","password":"a sufficiently long passphrase"}`)
	pending := cookieNamed(cookies, "__Host-pgfy_pending")
	if status != 200 || step["next"] != "code" || pending == nil || cookieNamed(cookies, "__Host-pgfy_session") != nil {
		t.Fatal("password step issued a session", status, step)
	}
	valid := code(t, key, f.now)
	if status, _, cookies := f.post("/api/v1/auth/code", `{"code":"`+valid+`"}`, pending); status != 200 || cookieNamed(cookies, "__Host-pgfy_session") == nil {
		t.Fatal("a correct code did not sign in", status)
	}
	status, step, cookies = f.post("/api/v1/auth/login", `{"email":"admin@example.com","password":"a sufficiently long passphrase"}`)
	if status, reply, _ := f.post("/api/v1/auth/code", `{"code":"`+valid+`"}`, cookieNamed(cookies, "__Host-pgfy_pending")); status != 401 || reply["error"].(map[string]any)["code"] != "code_reused" {
		t.Fatal("a code was accepted twice", status, reply)
	}
}

// An administrator from before second factors enrols one at the next HTTPS sign-in.
func TestExistingAdministratorEnrolsAtNextHTTPSSignIn(t *testing.T) {
	f := newFixture(t)
	f.setup(t) // tunnel mode, password only
	f.s.Config.Mode, f.s.Config.Hostname, f.s.Config.Origin = "https", "dash.example.com", "https://dash.example.com"
	status, view, cookies := f.post("/api/v1/auth/login", `{"email":"admin@example.com","password":"a sufficiently long passphrase"}`)
	enrol := cookieNamed(cookies, "__Host-pgfy_enrol")
	if status != 200 || view["next"] != "enrol" || enrol == nil {
		t.Fatal("HTTPS sign-in without a factor did not enrol one", status, view)
	}
	// A reload shows the same key again.
	again := f.requestWith("GET", "/api/v1/auth/enrol", nil, []*http.Cookie{enrol}, "")
	var shown map[string]any
	json.Unmarshal(again.Body.Bytes(), &shown)
	if shown["key"] != view["key"] {
		t.Fatal("the key could not be shown again", again.Code)
	}
	if status, _, _ := f.post("/api/v1/auth/enrol/confirm", `{"code":"`+code(t, view["key"].(string), f.now)+`"}`, enrol); status != 200 {
		t.Fatal(status)
	}
	var events int
	f.s.Store.DB.QueryRow("SELECT count(*) FROM alert_events WHERE kind='second_factor_enrolled'").Scan(&events)
	if events != 1 {
		t.Fatal("first enrolment was not alerted")
	}
}

func TestTunnelModeKeepsPasswordSignInUntilAFactorExists(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	status, reply, cookies := f.post("/api/v1/auth/login", `{"email":"admin@example.com","password":"a sufficiently long passphrase"}`)
	if status != 200 || reply["csrf_token"] == nil || cookieNamed(cookies, "pgfy_tunnel_session") == nil {
		t.Fatal(status, reply)
	}
	// A reset over SSH enrols a factor; from then on a code is required in either mode.
	token, _ := f.s.Store.NewResetToken(context.Background(), f.now)
	status, view, cookies := f.post("/api/v1/auth/reset", `{"token":"`+token+`","password":"another long enough passphrase"}`)
	enrol := cookieNamed(cookies, "pgfy_tunnel_enrol")
	if status != 200 || enrol == nil || enrol.Secure {
		t.Fatal(status, view)
	}
	if status, _, _ := f.post("/api/v1/auth/enrol/confirm", `{"code":"`+code(t, view["key"].(string), f.now)+`"}`, enrol); status != 200 {
		t.Fatal(status)
	}
	f.now = f.now.Add(time.Minute)
	if status, _, _ := f.post("/api/v1/auth/login", `{"email":"admin@example.com","password":"a sufficiently long passphrase"}`); status != 401 {
		t.Fatal("the old password still works after the reset", status)
	}
	status, step, _ := f.post("/api/v1/auth/login", `{"email":"admin@example.com","password":"another long enough passphrase"}`)
	if status != 200 || step["next"] != "code" {
		t.Fatal("a factor exists but tunnel sign-in skipped it", status, step)
	}
}

// requestWith sends a request as the browser would to the configured origin, carrying several cookies.
func (f *fixture) requestWith(method, path string, body io.Reader, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, f.s.Config.Origin+path, body)
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		req.Header.Set("Origin", f.s.Config.Origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	out := httptest.NewRecorder()
	f.h.ServeHTTP(out, req)
	return out
}
