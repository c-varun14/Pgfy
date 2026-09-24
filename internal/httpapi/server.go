package httpapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/c-varun14/Pgfy/internal/alerts"
	"github.com/c-varun14/Pgfy/internal/config"
	"github.com/c-varun14/Pgfy/internal/hoststatus"
	"github.com/c-varun14/Pgfy/internal/jobs"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/provision"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

type PGCheck func(context.Context) (string, error)
type Server struct {
	Config       config.Installation
	Store        *store.Store
	Vault        *security.Vault
	PG           PGCheck
	Assets       fs.FS
	Versions     map[string]string
	TrustedProxy netip.Prefix
	Mgmt         *postgres.Management
	Provisioner  *provision.Provisioner
	Jobs         *jobs.Worker
	TLSStatePath string
	HostPaths    hoststatus.Paths
	Alerts       *alerts.Engine
	Now          func() time.Time
	limiter      rateLimit
	hashSlots    chan struct{}
}
type bucket struct {
	count int
	until time.Time
}
type rateLimit struct {
	mu      sync.Mutex
	clients map[string]bucket
	global  bucket
}

func (l *rateLimit) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients == nil {
		l.clients = map[string]bucket{}
	}
	if !now.Before(l.global.until) {
		l.global = bucket{until: now.Add(time.Minute)}
	}
	b := l.clients[key]
	if !now.Before(b.until) {
		b = bucket{until: now.Add(15 * time.Minute)}
	}
	for k, v := range l.clients {
		if !now.Before(v.until) {
			delete(l.clients, k)
		}
	}
	if l.global.count >= 30 || b.count >= 10 || len(l.clients) >= 10000 {
		return false
	}
	l.global.count++
	b.count++
	l.clients[key] = b
	return true
}
func (s *Server) scope() string {
	return s.Config.ID + ":" + s.Config.Mode + ":" + s.Config.Generation + ":" + s.Config.Origin
}
func (s *Server) cookieName() string {
	if s.Config.Mode == "https" {
		return "__Host-pgfy_session"
	}
	return "pgfy_tunnel_session"
}
func (s *Server) Handler() http.Handler {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.hashSlots = make(chan struct{}, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "live"}) })
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /api/v1/setup", s.setupAvailable)
	mux.HandleFunc("POST /api/v1/setup", s.setup)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/session", s.session)
	mux.HandleFunc("GET /api/v1/system/status", s.status)
	mux.HandleFunc("GET /api/v1/settings", s.settings)
	mux.HandleFunc("GET /api/v1/projects", s.listProjects)
	mux.HandleFunc("POST /api/v1/projects", s.createProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", s.getProject)
	mux.HandleFunc("POST /api/v1/projects/{id}/retry", s.retryProject)
	mux.HandleFunc("GET /api/v1/projects/{id}/credentials", s.getCredentials)
	mux.HandleFunc("PUT /api/v1/projects/{id}/access", s.updateAccess)
	mux.HandleFunc("PUT /api/v1/projects/{id}/writes", s.updateWrites)
	mux.HandleFunc("PUT /api/v1/projects/{id}/limits", s.putLimits)
	mux.HandleFunc("POST /api/v1/projects/{id}/credentials/rotate", s.rotateCredentials)
	mux.HandleFunc("GET /api/v1/system/connections", s.connectionBudget)
	mux.HandleFunc("GET /api/v1/settings/alerts", s.getAlertSettings)
	mux.HandleFunc("PUT /api/v1/settings/alerts", s.putAlertSettings)
	mux.HandleFunc("POST /api/v1/settings/alerts/test", s.testAlerts)
	mux.HandleFunc("GET /api/v1/alerts", s.listAlerts)
	mux.HandleFunc("POST /api/v1/projects/{id}/connection-checks", s.createConnectionCheck)
	mux.HandleFunc("GET /api/v1/projects/{id}/connection-checks/{check}", s.getConnectionCheck)
	mux.HandleFunc("GET /api/v1/settings/storage", s.getStorage)
	mux.HandleFunc("PUT /api/v1/settings/storage", s.putStorage)
	mux.HandleFunc("POST /api/v1/settings/storage/check", s.checkStorage)
	mux.HandleFunc("GET /api/v1/settings/backups", s.getBackupPolicy)
	mux.HandleFunc("PUT /api/v1/settings/backups", s.putBackupPolicy)
	mux.HandleFunc("POST /api/v1/projects/{id}/backups", s.startBackup)
	mux.HandleFunc("GET /api/v1/projects/{id}/backups", s.listBackups)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/v1/recovery/backups", s.listRecoveryBackups)
	mux.HandleFunc("POST /api/v1/recovery/restores", s.startRestore)
	mux.HandleFunc("/", s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", security.Token()[:16])
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.Config.Mode == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), baseContextKey{}, r.Context()), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		defer func() {
			if recover() != nil {
				slog.Error("request failed", "request_id", w.Header().Get("X-Request-ID"))
				failure(w, 500, "internal_error", "Request could not be completed.")
			}
		}()
		if strings.HasPrefix(r.URL.Path, "/api/") {
			u, _ := url.Parse(s.Config.Origin)
			if r.Host != u.Host {
				failure(w, 403, "invalid_host", "Use the configured dashboard address.")
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if r.Header.Get("Origin") != s.Config.Origin || media != "application/json" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
					failure(w, 403, "origin_rejected", "Request origin or content type was rejected.")
					return
				}
			}
			if s.Store == nil {
				failure(w, 503, "metadata_unavailable", "Management storage is unavailable. Run host diagnostics.")
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" && !maintenanceExempt[path.Clean(r.URL.Path)] {
				// Nothing a rollback would have to undo may be written during an update.
				if on, e := s.Store.Maintenance(r.Context()); e != nil || on {
					failure(w, 503, "maintenance", "An update is in progress. Try again when it finishes.")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// Signing in and out stays possible while an update holds the installation quiet.
var maintenanceExempt = map[string]bool{"/api/v1/setup": true, "/api/v1/auth/login": true, "/api/v1/auth/logout": true}

type baseContextKey struct{}

// contextWithTimeout derives from the request's pre-timeout context so slow
// external calls (object storage) get their own explicit bound.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	base, _ := r.Context().Value(baseContextKey{}).(context.Context)
	if base == nil {
		base = r.Context()
	}
	return context.WithTimeout(base, d)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "request_id": w.Header().Get("X-Request-ID")}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		failure(w, 400, "invalid_request", "Provide a valid JSON request.")
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		failure(w, 400, "invalid_request", "Provide a single JSON object.")
		return false
	}
	return true
}
func (s *Server) checks(ctx context.Context) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	sqliteOK := s.Store != nil && s.Store.Check(ctx) == nil
	if s.PG == nil {
		return sqliteOK, "", errors.New("postgres unavailable")
	}
	version, e := s.PG(ctx)
	return sqliteOK, version, e
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	sqliteOK, _, pgErr := s.checks(r.Context())
	if !sqliteOK || pgErr != nil {
		write(w, 503, map[string]string{"status": "not_ready"})
		return
	}
	write(w, 200, map[string]string{"status": "ready"})
}
func (s *Server) setupAvailable(w http.ResponseWriter, r *http.Request) {
	available, e := s.Store.SetupAvailable(r.Context())
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Management storage is unavailable.")
		return
	}
	write(w, 200, map[string]bool{"available": available})
}
func (s *Server) client(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip, e := netip.ParseAddr(host)
	if e == nil && s.TrustedProxy.IsValid() && s.TrustedProxy.Contains(ip) {
		if forwarded, e := netip.ParseAddr(r.Header.Get("X-Forwarded-For")); e == nil {
			return forwarded.String()
		}
	}
	return host
}
func (s *Server) beginAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.limiter.allow(s.client(r), s.Now()) {
		w.Header().Set("Retry-After", "900")
		failure(w, 429, "rate_limited", "Too many attempts. Try again later.")
		return false
	}
	select {
	case s.hashSlots <- struct{}{}:
		return true
	default:
		failure(w, 429, "auth_busy", "Authentication is busy. Try again shortly.")
		return false
	}
}

type credentials struct {
	Token    string `json:"token"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func normalizeEmail(value string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	a, e := mail.ParseAddress(v)
	return v, e == nil && a.Address == v && len(v) <= 254
}
func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: s.Config.Mode == "https", SameSite: http.SameSiteStrictMode, MaxAge: 43200, Expires: s.Now().Add(12 * time.Hour)})
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !s.beginAuth(w, r) {
		return
	}
	defer func() { <-s.hashSlots }()
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	email, valid := normalizeEmail(in.Email)
	if !valid || len(in.Token) != 43 {
		failure(w, 400, "invalid_setup", "Provide a valid email, setup token, and password.")
		return
	}
	hash, e := security.Password(in.Password)
	if e != nil {
		failure(w, 400, "invalid_password", e.Error())
		return
	}
	token := security.Token()
	e = s.Store.Setup(r.Context(), in.Token, email, hash, security.Hash(token), s.scope(), s.Now())
	if errors.Is(e, store.ErrSetup) {
		failure(w, 409, "setup_rejected", "Setup token is invalid, expired, or already used.")
		return
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Setup could not be saved. Run host diagnostics.")
		return
	}
	s.setCookie(w, token)
	write(w, 201, map[string]string{"email": email, "csrf_token": s.Vault.CSRF(token, s.scope())})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.beginAuth(w, r) {
		return
	}
	defer func() { <-s.hashSlots }()
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	email, _ := normalizeEmail(in.Email)
	hash, e := s.Store.Password(r.Context(), email)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		failure(w, 503, "metadata_unavailable", "Management storage is unavailable.")
		return
	}
	// A fixed valid dummy hash keeps unknown emails on the same expensive verification path.
	if errors.Is(e, sql.ErrNoRows) {
		hash = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	valid := security.VerifyPassword(hash, in.Password)
	if e != nil || !valid {
		failure(w, 401, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	token := security.Token()
	if e = s.Store.AddSession(r.Context(), token, s.scope(), s.Now()); e != nil {
		failure(w, 503, "metadata_unavailable", "Session could not be saved.")
		return
	}
	s.setCookie(w, token)
	write(w, 200, map[string]string{"email": email, "csrf_token": s.Vault.CSRF(token, s.scope())})
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (store.Session, string, bool) {
	c, e := r.Cookie(s.cookieName())
	if e != nil || len(c.Value) != 43 {
		failure(w, 401, "unauthorized", "Sign in to continue.")
		return store.Session{}, "", false
	}
	session, e := s.Store.Session(r.Context(), c.Value, s.scope(), s.Now())
	if errors.Is(e, sql.ErrNoRows) {
		failure(w, 401, "unauthorized", "Your session expired. Sign in again.")
		return session, "", false
	}
	if e != nil {
		failure(w, 503, "metadata_unavailable", "Management storage is unavailable.")
		return session, "", false
	}
	session.CSRF = s.Vault.CSRF(c.Value, s.scope())
	if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
		failure(w, 403, "csrf_rejected", "Refresh the page and try again.")
		return session, "", false
	}
	return session, c.Value, true
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.authorize(w, r)
	if ok {
		// The caller's address helps "restrict to my IP"; it is the proxy-reported client.
		session.ClientIP = s.client(r)
		write(w, 200, session)
	}
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_, token, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if e := s.Store.Logout(r.Context(), token); e != nil {
		failure(w, 503, "metadata_unavailable", "Logout could not be saved. Try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.Config.Mode == "https", SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); !ok {
		return
	}
	sqliteOK, version, pgErr := s.checks(r.Context())
	pgStatus := "available"
	if pgErr != nil {
		pgStatus = "unavailable"
		version = ""
	}
	sqliteStatus := "available"
	if !sqliteOK {
		sqliteStatus = "unavailable"
	}
	var sqliteVersion string
	_ = s.Store.DB.QueryRowContext(r.Context(), "SELECT sqlite_version()").Scan(&sqliteVersion)
	maintenance, _ := s.Store.Maintenance(r.Context())
	write(w, 200, map[string]any{"ready": sqliteOK && pgErr == nil, "maintenance": maintenance, "host": hoststatus.Read(s.HostPaths, s.Config.Mode, s.Now()), "sqlite": map[string]string{"status": sqliteStatus, "version": sqliteVersion}, "postgres": map[string]string{"status": pgStatus, "version": version}, "versions": s.Versions, "backups": s.backupStatus(r), "database_access": s.databaseAccess()})
}

// backupStatus answers from the reconciled view in SQLite, so it stays cheap
// and keeps working while PostgreSQL or the worker is down.
func (s *Server) backupStatus(r *http.Request) string {
	configured, target := s.storageTarget(r)
	if !configured {
		return "not_configured"
	}
	state, e := s.Store.StorageTarget(r.Context(), target)
	if e != nil {
		return "unavailable"
	}
	if state.ReconciledAt == 0 {
		return "checking"
	}
	if failures, e := s.Store.BackupFailures(r.Context()); e == nil && len(failures) > 0 {
		return "failing"
	}
	policy, e := s.Store.BackupPolicy(r.Context())
	if e != nil {
		return "unavailable"
	}
	late, e := s.Store.LateProjects(r.Context(), target, s.Config.ID, policy.Interval(), s.Now())
	if e != nil {
		return "unavailable"
	}
	if len(late) > 0 {
		return "stale"
	}
	return "ok"
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorize(w, r); ok {
		write(w, 200, s.Config)
	}
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health/") {
		failure(w, 404, "not_found", "Endpoint not found.")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	if s.Assets == nil {
		http.Error(w, "Dashboard assets unavailable", 503)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, e := fs.Stat(s.Assets, path); e != nil {
		if strings.HasPrefix(path, "assets/") {
			http.NotFound(w, r)
			return
		}
		path = "index.html"
	}
	if path == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path = "/" + path
	r2.URL = &u
	// Serve index directly to avoid FileServer's /index.html redirect loop for SPA routes.
	if path == "index.html" {
		b, e := fs.ReadFile(s.Assets, path)
		if e != nil {
			http.Error(w, "Dashboard unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != "HEAD" {
			_, _ = w.Write(b)
		}
		return
	}
	http.FileServer(http.FS(s.Assets)).ServeHTTP(w, r2)
}
