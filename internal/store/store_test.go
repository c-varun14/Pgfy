package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metadata.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s, path
}
func TestSetupAtomicAcrossConnections(t *testing.T) {
	s, path := openTest(t)
	s2, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s2.DB.Close()
	ctx := context.Background()
	now := time.Now()
	token, e := s.NewSetupToken(ctx, now)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, connection := range []*Store{s, s2} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			results <- s.Setup(ctx, token, "admin@example.com", "hash", string(rune('a'+i)), "scope", now)
		}(i, connection)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrSetup) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("got %d successful admins", success)
	}
	if _, e = s.NewSetupToken(ctx, now.Add(time.Hour)); !errors.Is(e, ErrSetup) {
		t.Fatal("setup reopened")
	}
	var count int
	s.DB.QueryRow("SELECT count(*) FROM administrator").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	s.DB.QueryRow("SELECT count(*) FROM auth_tokens WHERE purpose='setup' AND consumed_at IS NULL").Scan(&count)
	if count != 0 {
		t.Fatal("token not consumed")
	}
}
func TestTokensExpiryAndPreservation(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	now := time.Now()
	token, e := s.NewSetupToken(ctx, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.NewSetupToken(ctx, now.Add(time.Minute)); !errors.Is(e, ErrTokenActive) {
		t.Fatal("active token replaced")
	}
	for _, tc := range []struct {
		token string
		now   time.Time
	}{{"invalid", now}, {token, now.Add(30 * time.Minute)}} {
		if e = s.Setup(ctx, tc.token, "admin@example.com", "hash", "session", "scope", tc.now); !errors.Is(e, ErrSetup) {
			t.Fatal("invalid token accepted", e)
		}
	}
	s.DB.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	replacement, e := s.NewSetupToken(ctx, now.Add(31*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if replacement == token {
		t.Fatal("token reused")
	}
	if e = s.Setup(ctx, replacement, "admin@example.com", "hash", "session", "scope", now.Add(31*time.Minute)); e != nil {
		t.Fatal(e)
	}
}
func TestMigrationsSessionsAndReadiness(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	now := time.Now()
	token, _ := s.NewSetupToken(ctx, now)
	if e := s.Setup(ctx, token, "admin@example.com", "hash", "initial", "scope", now); e != nil {
		t.Fatal(e)
	}
	if e := s.AddSession(ctx, "session", "scope", now); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Session(ctx, "session", "scope", now); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Session(ctx, "session", "other", now); e == nil {
		t.Fatal("cross-scope session")
	}
	if _, e := s.Session(ctx, "session", "scope", now.Add(12*time.Hour)); e == nil {
		t.Fatal("expired session")
	}
	s.Logout(ctx, "session")
	if _, e := s.Session(ctx, "session", "scope", now); e == nil {
		t.Fatal("logout not invalidated")
	}
	if e := s.Bind(ctx, "id1"); e != nil {
		t.Fatal(e)
	}
	if e := s.Bind(ctx, "id2"); e == nil {
		t.Fatal("foreign installation accepted")
	}
	var wal string
	var fk, busy int
	s.DB.QueryRow("PRAGMA journal_mode").Scan(&wal)
	s.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	s.DB.QueryRow("PRAGMA busy_timeout").Scan(&busy)
	if wal != "wal" || fk != 1 || busy != 5000 {
		t.Fatal(wal, fk, busy)
	}
	s.DB.Exec("PRAGMA query_only=ON")
	if e := s.Check(ctx); e == nil {
		t.Fatal("read-only storage considered ready")
	}
	s.DB.Exec("PRAGMA query_only=OFF")
	s.DB.Exec("UPDATE schema_migrations SET checksum='tampered'")
	s.DB.Close()
	if reopened, e := Open(path); e == nil {
		reopened.DB.Close()
		t.Fatal("modified migration accepted")
	}
}
