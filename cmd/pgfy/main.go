package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/c-varun14/Pgfy/internal/config"
	"github.com/c-varun14/Pgfy/internal/httpapi"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
	"github.com/c-varun14/Pgfy/web"
)

var version = "dev"
var commit = "unknown"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if e := run(); e != nil {
		slog.Error("command failed", "reason", e.Error())
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "version" {
		fmt.Printf("pgfy %s (%s)\n", version, commit)
		return nil
	}
	if command == "health" {
		route := "live"
		if len(os.Args) > 2 && os.Args[2] == "ready" {
			route = "ready"
		}
		client := http.Client{Timeout: 5 * time.Second}
		r, e := client.Get("http://127.0.0.1:3000/health/" + route)
		if e != nil {
			return errors.New("application health check failed")
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return errors.New("application is not ready")
		}
		return nil
	}
	cfg, e := config.Load(config.Env("PGFY_CONFIG", "/etc/pgfy/install.json"))
	if e != nil {
		return errors.New("invalid or missing installation configuration")
	}
	dbPath := config.Env("PGFY_DB", "/data/pgfy.db")
	var s *store.Store
	var storeErr error
	if command == "initialize-store" {
		// Only this explicit host-side first-install command may create metadata.
		file, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("metadata already exists or cannot be initialized; inspect it on the host")
		}
		file.Close()
	} else {
		stat, err := os.Stat(dbPath)
		if err != nil || stat.Size() == 0 {
			storeErr = errors.New("existing management storage is missing or empty")
		}
	}
	if storeErr == nil {
		s, storeErr = store.Open(dbPath)
	}
	if storeErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		storeErr = s.Bind(ctx, cfg.ID)
		cancel()
		if storeErr != nil {
			s.DB.Close()
			s = nil
		}
	}
	if s != nil {
		defer s.DB.Close()
	}
	if command == "initialize-store" {
		if storeErr != nil {
			return errors.New("metadata initialization failed; inspect state before retrying")
		}
		return nil
	}
	if command == "setup-token" {
		if storeErr != nil {
			return errors.New("management storage unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		token, e := s.NewSetupToken(ctx, time.Now())
		if e != nil {
			return e
		}
		fmt.Println(token)
		return nil
	}
	if command != "serve" {
		return errors.New("usage: pgfy [serve|version|health [ready]|setup-token|initialize-store]")
	}
	if storeErr != nil {
		slog.Error("management storage initialization failed; readiness disabled")
	}
	vault, e := security.LoadVault(config.Env("PGFY_KEY_FILE", "/run/secrets/encryption_key"))
	if e != nil {
		return errors.New("installation encryption key is missing or insecure")
	}
	pg, pgErr := postgres.Open(context.Background(), config.Env("PGFY_HEALTH_PASSWORD_FILE", "/run/secrets/health_password"), cfg.ID)
	if pgErr != nil {
		slog.Error("postgres health configuration unavailable")
	} else {
		defer pg.Pool.Close()
	}
	versions := map[string]string{"application": version, "commit": commit, "go": runtime.Version()}
	for _, tool := range []string{"psql", "pg_dump", "pg_restore"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, e := exec.CommandContext(ctx, tool, "--version").Output()
		cancel()
		if e != nil {
			versions[tool] = "unavailable"
		} else {
			versions[tool] = strings.TrimSpace(string(out))
		}
	}
	proxy := netip.Prefix{}
	if value := os.Getenv("PGFY_TRUSTED_PROXY"); value != "" {
		proxy, e = netip.ParsePrefix(value)
		if e != nil {
			return errors.New("invalid trusted proxy CIDR")
		}
	}
	api := httpapi.Server{Config: cfg, Store: s, Vault: vault, PG: pg.Check, Assets: web.Assets(), Versions: versions, TrustedProxy: proxy}
	srv := http.Server{Addr: config.Env("PGFY_LISTEN", ":3000"), Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	b, _ := json.Marshal(map[string]string{"version": version, "mode": cfg.Mode})
	slog.Info("application starting", "build", string(b))
	if e = srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		return errors.New("HTTP server failed")
	}
	return nil
}
