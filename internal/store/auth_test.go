package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/c-varun14/Pgfy/internal/security"
)

func confirmWith(c Confirmed, err error) func(string) (Confirmed, error) {
	return func(string) (Confirmed, error) { return c, err }
}

func enrolAdmin(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	token, e := s.NewSetupToken(ctx, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.BeginEnrolment(ctx, token, "enrol-1", "sealed", now); e != nil {
		t.Fatal(e)
	}
	c := Confirmed{Enrolment: Enrolment{Kind: "setup", Email: "admin@example.com", PasswordHash: "hash-1"}, SecretSealed: "factor", Step: 10}
	if _, e = s.ConfirmEnrolment(ctx, "enrol-1", "session-1", "scope", "req", now, confirmWith(c, nil)); e != nil {
		t.Fatal(e)
	}
}

func TestSetupTokenIsSpentAtEnrolmentAndCanBeReissuedAfterTheWindow(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	token, _ := s.NewSetupToken(ctx, now)
	if e := s.BeginEnrolment(ctx, token, "enrol-a", "p", now); e != nil {
		t.Fatal(e)
	}
	if e := s.BeginEnrolment(ctx, token, "enrol-b", "p", now); !errors.Is(e, ErrSetup) {
		t.Fatal("a second enrolment started from the same setup token", e)
	}
	// The enrolment lapses unconfirmed: the operator must be able to start over.
	if _, e := s.NewSetupToken(ctx, now.Add(11*time.Minute)); e != nil {
		t.Fatal("setup-token is a dead end after a lapsed enrolment:", e)
	}
	if _, e := s.ConfirmEnrolment(ctx, "enrol-a", "x", "scope", "", now.Add(11*time.Minute), confirmWith(Confirmed{Enrolment: Enrolment{Kind: "setup"}}, nil)); !errors.Is(e, ErrToken) {
		t.Fatal("an expired enrolment confirmed", e)
	}
}

func TestWrongEnrolmentCodesSpendTheTokenAndOnlyOneEnrolmentWins(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	token, _ := s.NewSetupToken(ctx, now)
	s.BeginEnrolment(ctx, token, "enrol", "p", now)
	for i := 0; i < 5; i++ {
		if _, e := s.ConfirmEnrolment(ctx, "enrol", "s", "scope", "", now, confirmWith(Confirmed{}, ErrCode)); !errors.Is(e, ErrCode) {
			t.Fatal(i, e)
		}
	}
	good := Confirmed{Enrolment: Enrolment{Kind: "setup", Email: "a@example.com", PasswordHash: "h"}, SecretSealed: "f", Step: 1}
	if _, e := s.ConfirmEnrolment(ctx, "enrol", "s", "scope", "", now, confirmWith(good, nil)); !errors.Is(e, ErrToken) {
		t.Fatal("a sixth attempt was allowed", e)
	}
	// Two upgrade enrolments outstanding: the first to confirm sets the factor, the other cannot replace it.
	s2, _ := openTest(t)
	token, _ = s2.NewSetupToken(ctx, now)
	s2.Setup(ctx, token, "a@example.com", "hash", "sess", "scope", now)
	s2.BeginUpgrade(ctx, "up-1", "p", now)
	s2.BeginUpgrade(ctx, "up-2", "p", now)
	upgrade := func(secret string) Confirmed {
		return Confirmed{Enrolment: Enrolment{Kind: "upgrade", Binding: security.Hash("hash")}, SecretSealed: secret, Step: 1}
	}
	if _, e := s2.ConfirmEnrolment(ctx, "up-1", "s1", "scope", "", now, confirmWith(upgrade("first"), nil)); e != nil {
		t.Fatal(e)
	}
	if _, e := s2.ConfirmEnrolment(ctx, "up-2", "s2", "scope", "", now, confirmWith(upgrade("second"), nil)); !errors.Is(e, ErrToken) {
		t.Fatal("a second upgrade replaced the factor", e)
	}
	var secret string
	s2.DB.QueryRow("SELECT totp_secret_sealed FROM administrator").Scan(&secret)
	if secret != "first" {
		t.Fatal(secret)
	}
}

func TestCodesAreSingleUseBoundToThePasswordAndBackedOff(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	enrolAdmin(t, s, now)
	accept := func(step int64) func(string, int64) (int64, error) {
		return func(_ string, last int64) (int64, error) { return step, nil }
	}
	s.BeginPending(ctx, "p1", "hash-1", "scope", now)
	if _, e := s.ConfirmPending(ctx, "p1", "s1", "scope", now, accept(11)); e != nil {
		t.Fatal(e)
	}
	s.BeginPending(ctx, "p2", "hash-1", "scope", now)
	if _, e := s.ConfirmPending(ctx, "p2", "s2", "scope", now, accept(11)); !errors.Is(e, ErrCodeReused) {
		t.Fatal("a replayed step was accepted", e)
	}
	// A password step no longer counts once the password changed.
	s.BeginPending(ctx, "p3", "old-hash", "scope", now)
	if _, e := s.ConfirmPending(ctx, "p3", "s3", "scope", now, accept(12)); !errors.Is(e, ErrToken) {
		t.Fatal(e)
	}
	// Wrong codes are counted across new pending tokens; the fifth locks.
	wrong := func(string, int64) (int64, error) { return 0, ErrCode }
	for i := 0; i < 5; i++ {
		hash := "w" + string(rune('a'+i))
		s.BeginPending(ctx, hash, "hash-1", "scope", now)
		if _, e := s.ConfirmPending(ctx, hash, "s", "scope", now, wrong); !errors.Is(e, ErrCode) {
			t.Fatal(i, e)
		}
	}
	s.BeginPending(ctx, "after", "hash-1", "scope", now)
	if _, e := s.ConfirmPending(ctx, "after", "s", "scope", now.Add(30*time.Second), accept(20)); !errors.Is(e, ErrCodeLocked) {
		t.Fatal("no lockout after five wrong codes", e)
	}
	if _, e := s.ConfirmPending(ctx, "after", "s", "scope", now.Add(2*time.Minute), accept(20)); e != nil {
		t.Fatal("the locked attempt spent the pending token or the lock did not lapse", e)
	}
}

func TestResetChangesNothingUntilConfirmedThenEverything(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	enrolAdmin(t, s, now)
	// Lock the administrator out with wrong codes; the SSH reset is the way back in.
	for i := 0; i < 5; i++ {
		hash := "w" + string(rune('a'+i))
		s.BeginPending(ctx, hash, "hash-1", "scope", now)
		s.ConfirmPending(ctx, hash, "s", "scope", now, func(string, int64) (int64, error) { return 0, ErrCode })
	}
	first, _ := s.NewResetToken(ctx, now)
	second, _ := s.NewResetToken(ctx, now)
	if e := s.BeginReset(ctx, first, "stale", "p", now); !errors.Is(e, ErrToken) {
		t.Fatal("a replaced reset token still works", e)
	}
	if e := s.BeginReset(ctx, second, "reset-enrol", "p", now); e != nil {
		t.Fatal(e)
	}
	if hash, _ := s.Password(ctx, "admin@example.com"); hash != "hash-1" {
		t.Fatal("starting a reset changed the password")
	}
	s.SetMaintenance(ctx, true)
	c := Confirmed{Enrolment: Enrolment{Kind: "reset", PasswordHash: "hash-2"}, SecretSealed: "factor-2", Step: 30}
	if _, e := s.ConfirmEnrolment(ctx, "reset-enrol", "new-session", "scope", "req-9", now, confirmWith(c, nil)); !errors.Is(e, ErrMaintenance) {
		t.Fatal("a reset completed during an update", e)
	}
	s.SetMaintenance(ctx, false)
	if _, e := s.ConfirmEnrolment(ctx, "reset-enrol", "new-session", "scope", "req-9", now, confirmWith(c, nil)); e != nil {
		t.Fatal(e)
	}
	var sessions, failures int
	var secret string
	s.DB.QueryRow("SELECT count(*) FROM sessions WHERE token_hash<>'new-session'").Scan(&sessions)
	s.DB.QueryRow("SELECT code_failures, totp_secret_sealed FROM administrator").Scan(&failures, &secret)
	if sessions != 0 || failures != 0 || secret != "factor-2" {
		t.Fatal("reset did not replace everything", sessions, failures, secret)
	}
	entries, _ := s.AuditEntries(ctx, 5)
	var events int
	s.DB.QueryRow("SELECT count(*) FROM alert_events WHERE kind='admin_reset'").Scan(&events)
	if entries[0].Action != "auth.reset" || entries[0].RequestID != "req-9" || events != 1 {
		t.Fatal("reset not audited or alerted", entries, events)
	}
	s.BeginPending(ctx, "fresh", "hash-2", "scope", now)
	if _, e := s.ConfirmPending(ctx, "fresh", "s", "scope", now, func(string, int64) (int64, error) { return 31, nil }); e != nil {
		t.Fatal("signing in right after a reset failed", e)
	}
}

func TestMigrationMovesAnUnusedSetupToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	previous := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrationSources[0], ".")
	for _, entry := range entries {
		if entry.Name() < "008" {
			body, _ := fs.ReadFile(migrationSources[0], entry.Name())
			previous[entry.Name()] = &fstest.MapFile{Data: body}
		}
	}
	saved := migrationSources
	migrationSources = []fs.FS{previous}
	old, e := Open(path)
	migrationSources = saved
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	token := security.Token()
	old.DB.Exec("INSERT INTO setup_token(id,token_hash,expires_at) VALUES (1,?,?)", security.Hash(token), now.Add(20*time.Minute).Unix())
	old.DB.Close()
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if _, e := s.DB.Exec("SELECT 1 FROM setup_token"); e == nil {
		t.Fatal("setup_token still exists")
	}
	if e := s.BeginEnrolment(context.Background(), token, "enrol", "p", now); e != nil {
		t.Fatal("the migrated setup token does not work", e)
	}
}
