package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/c-varun14/Pgfy/internal/security"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS
var ErrSetup = errors.New("setup unavailable or token invalid")
var ErrTokenActive = errors.New("setup token has not expired; wait for expiry before replacing it")

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL"} {
		if _, e = db.ExecContext(ctx, q); e != nil {
			db.Close()
			return nil, e
		}
	}
	if e = s.migrate(ctx); e != nil {
		db.Close()
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) migrate(ctx context.Context) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL)"); e != nil {
		return e
	}
	files, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	known := map[string]bool{}
	for _, file := range files {
		known[file.Name()] = true
	}
	rows, e := tx.QueryContext(ctx, "SELECT name FROM schema_migrations")
	if e != nil {
		return e
	}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			return e
		}
		if !known[name] {
			rows.Close()
			return errors.New("database contains an unsupported migration")
		}
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); e != nil {
		return e
	}
	if count > len(files) {
		return errors.New("database schema is newer than application")
	}
	for _, f := range files {
		b, e := migrations.ReadFile("migrations/" + f.Name())
		if e != nil {
			return e
		}
		checksum := security.Hash(string(b))
		var existing string
		e = tx.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE name=?", f.Name()).Scan(&existing)
		if e == nil {
			if existing != checksum {
				return errors.New("migration checksum mismatch")
			}
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = tx.ExecContext(ctx, string(b)); e != nil {
			return fmt.Errorf("migration %s failed: %w", f.Name(), e)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO schema_migrations(name,checksum) VALUES (?,?)", f.Name(), checksum); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) Check(ctx context.Context) error {
	// A bounded write detects read-only/full storage as well as connection failure.
	_, e := s.DB.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES ('health','ok') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	return e
}
func (s *Store) Bind(ctx context.Context, id string) error {
	_, e := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO metadata(key,value) VALUES ('installation_id',?)", id)
	if e != nil {
		return e
	}
	var got string
	e = s.DB.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='installation_id'").Scan(&got)
	if e != nil {
		return e
	}
	if got != id {
		return errors.New("installation identity mismatch")
	}
	return nil
}
func (s *Store) SetupAvailable(ctx context.Context) (bool, error) {
	var n int
	e := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&n)
	return n == 0, e
}
func (s *Store) NewSetupToken(ctx context.Context, now time.Time) (string, error) {
	token := security.Token()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	// Take the write lock before inspecting administrator/token state.
	if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO metadata(key,value) VALUES ('setup_lock','1')"); e != nil {
		return "", e
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&n); e != nil {
		return "", e
	}
	if n != 0 {
		return "", ErrSetup
	}
	var expires int64
	e = tx.QueryRowContext(ctx, "SELECT expires_at FROM setup_token WHERE id=1").Scan(&expires)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	if e == nil && expires > now.Unix() {
		return "", ErrTokenActive
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO setup_token(id,token_hash,expires_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at", security.Hash(token), now.Add(30*time.Minute).Unix())
	if e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return token, nil
}
func (s *Store) Setup(ctx context.Context, token, email, passwordHash, sessionHash, scope string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, "DELETE FROM setup_token WHERE id=1 AND token_hash=? AND expires_at>? AND NOT EXISTS(SELECT 1 FROM administrator)", security.Hash(token), now.Unix())
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrSetup
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO administrator(id,email,password_hash,created_at) VALUES (1,?,?,?)", email, passwordHash, now.Unix()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,admin_id,scope,expires_at) VALUES (?,1,?,?)", sessionHash, scope, now.Add(12*time.Hour).Unix()); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Password(ctx context.Context, email string) (string, error) {
	var hash string
	e := s.DB.QueryRowContext(ctx, "SELECT password_hash FROM administrator WHERE email=?", email).Scan(&hash)
	return hash, e
}
func (s *Store) AddSession(ctx context.Context, token, scope string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=? OR scope<>?", now.Unix(), scope); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,admin_id,scope,expires_at) VALUES (?,1,?,?)", security.Hash(token), scope, now.Add(12*time.Hour).Unix())
	if e != nil {
		return e
	}
	return tx.Commit()
}

type Session struct {
	Email     string `json:"email"`
	ExpiresAt int64  `json:"expires_at"`
	CSRF      string `json:"csrf_token"`
	ClientIP  string `json:"client_ip,omitempty"`
}

func (s *Store) Session(ctx context.Context, token, scope string, now time.Time) (Session, error) {
	var out Session
	e := s.DB.QueryRowContext(ctx, "SELECT a.email,s.expires_at FROM sessions s JOIN administrator a ON a.id=s.admin_id WHERE s.token_hash=? AND s.scope=? AND s.expires_at>?", security.Hash(token), scope, now.Unix()).Scan(&out.Email, &out.ExpiresAt)
	return out, e
}
func (s *Store) Logout(ctx context.Context, token string) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", security.Hash(token))
	return e
}
