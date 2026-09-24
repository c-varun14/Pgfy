package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/c-varun14/Pgfy/internal/security"
)

var (
	// ErrToken covers every unusable token alike: unknown, expired, consumed,
	// out of attempts, or of the wrong kind. Callers cannot tell them apart.
	ErrToken      = errors.New("the sign-in step expired or is not valid")
	ErrCode       = errors.New("the code is not valid")
	ErrCodeReused = errors.New("that code was already used; wait for the next one")
	ErrCodeLocked = errors.New("too many wrong codes; wait before trying again")
)

const (
	setupTTL   = 30 * time.Minute
	resetTTL   = 30 * time.Minute
	enrolTTL   = 10 * time.Minute
	pendingTTL = 5 * time.Minute
	codeTries  = 5
	sessionTTL = 12 * time.Hour
)

type token struct {
	ID       int64
	Parent   sql.NullInt64
	Payload  string
	Attempts int
	Max      int
}

func issue(ctx context.Context, tx *sql.Tx, purpose, hash string, ttl time.Duration, max int, parent *int64, payload string, now time.Time) (int64, error) {
	// Old spent tokens are pruned as new ones are issued.
	if _, e := tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE expires_at < ?", now.Add(-24*time.Hour).Unix()); e != nil {
		return 0, e
	}
	var p any
	if parent != nil {
		p = *parent
	}
	r, e := tx.ExecContext(ctx, "INSERT INTO auth_tokens(purpose,token_hash,expires_at,max_attempts,parent_id,sealed_payload,created_at) VALUES (?,?,?,?,?,?,?)",
		purpose, hash, now.Add(ttl).Unix(), max, p, payload, now.Unix())
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}

// use spends one attempt on a live token of the given purpose. The increment
// is guarded by the maximum inside the statement, so concurrent requests can
// never use more attempts than allowed.
func use(ctx context.Context, tx *sql.Tx, purpose, hash string, now time.Time) (token, error) {
	var t token
	r, e := tx.ExecContext(ctx, "UPDATE auth_tokens SET attempts=attempts+1 WHERE token_hash=? AND purpose=? AND consumed_at IS NULL AND expires_at>? AND attempts<max_attempts", hash, purpose, now.Unix())
	if e != nil {
		return t, e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return t, ErrToken
	}
	e = tx.QueryRowContext(ctx, "SELECT id,parent_id,sealed_payload,attempts,max_attempts FROM auth_tokens WHERE token_hash=?", hash).Scan(&t.ID, &t.Parent, &t.Payload, &t.Attempts, &t.Max)
	return t, e
}

func consume(ctx context.Context, tx *sql.Tx, id int64, now time.Time) error {
	_, e := tx.ExecContext(ctx, "UPDATE auth_tokens SET consumed_at=? WHERE id=?", now.Unix(), id)
	return e
}

// spendFailure consumes a token whose last attempt just failed.
func spendFailure(ctx context.Context, tx *sql.Tx, t token, now time.Time) error {
	if t.Attempts >= t.Max {
		return consume(ctx, tx, t.ID, now)
	}
	return nil
}

func addSession(ctx context.Context, tx *sql.Tx, sessionHash, scope string, now time.Time) error {
	if _, e := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=? OR scope<>?", now.Unix(), scope); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,admin_id,scope,expires_at) VALUES (?,1,?,?)", sessionHash, scope, now.Add(sessionTTL).Unix())
	return e
}

func adminExists(ctx context.Context, tx *sql.Tx) (bool, error) {
	var n int
	e := tx.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&n)
	return n > 0, e
}

// NewSetupToken issues the one-use setup token shown in the installer terminal.
// Only a live, unused token blocks a replacement.
func (s *Store) NewSetupToken(ctx context.Context, now time.Time) (string, error) {
	plain := security.Token()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	// Take the write lock before inspecting administrator/token state.
	if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO metadata(key,value) VALUES ('setup_lock','1')"); e != nil {
		return "", e
	}
	if exists, e := adminExists(ctx, tx); e != nil {
		return "", e
	} else if exists {
		return "", ErrSetup
	}
	var live int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM auth_tokens WHERE purpose='setup' AND consumed_at IS NULL AND expires_at>?", now.Unix()).Scan(&live); e != nil {
		return "", e
	}
	if live > 0 {
		return "", ErrTokenActive
	}
	if _, e = issue(ctx, tx, "setup", security.Hash(plain), setupTTL, 1, nil, "", now); e != nil {
		return "", e
	}
	return plain, tx.Commit()
}

// Setup creates the administrator in one step: tunnel mode only, where the
// second factor is optional. HTTPS mode enrols a factor first.
func (s *Store) Setup(ctx context.Context, setupToken, email, passwordHash, sessionHash, scope string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	t, e := use(ctx, tx, "setup", security.Hash(setupToken), now)
	if e != nil {
		return ErrSetup
	}
	if exists, e := adminExists(ctx, tx); e != nil || exists {
		return ErrSetup
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO administrator(id,email,password_hash,created_at) VALUES (1,?,?,?)", email, passwordHash, now.Unix()); e != nil {
		return e
	}
	if e = consume(ctx, tx, t.ID, now); e != nil {
		return e
	}
	if e = addSession(ctx, tx, sessionHash, scope, now); e != nil {
		return e
	}
	return tx.Commit()
}

// Enrolment payloads, sealed by the caller under a context naming the token.
type Enrolment struct {
	Kind         string `json:"kind"` // setup, reset or upgrade
	Email        string `json:"email,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	// Binding is Hash(password_hash) at the password step, for upgrade.
	Binding string `json:"binding,omitempty"`
	// Scope is the session scope the enrolment began in.
	Scope  string `json:"scope,omitempty"`
	Secret []byte `json:"secret"`
}

// BeginEnrolment spends the setup token: from here no competing enrolment can
// start, and the administrator exists only once a code confirms.
func (s *Store) BeginEnrolment(ctx context.Context, setupToken, enrolHash, sealed string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	t, e := use(ctx, tx, "setup", security.Hash(setupToken), now)
	if e != nil {
		return ErrSetup
	}
	if exists, e := adminExists(ctx, tx); e != nil || exists {
		return ErrSetup
	}
	if e = consume(ctx, tx, t.ID, now); e != nil {
		return e
	}
	if _, e = issue(ctx, tx, "enrol", enrolHash, enrolTTL, codeTries, &t.ID, sealed, now); e != nil {
		return e
	}
	return tx.Commit()
}

// BeginUpgrade enrols a factor for an administrator who has none, straight
// after a successful password step (HTTPS mode after an update).
func (s *Store) BeginUpgrade(ctx context.Context, enrolHash, sealed string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = issue(ctx, tx, "enrol", enrolHash, enrolTTL, codeTries, nil, sealed, now); e != nil {
		return e
	}
	return tx.Commit()
}

// NewResetToken is issued over SSH only. It replaces any earlier unused one.
func (s *Store) NewResetToken(ctx context.Context, now time.Time) (string, error) {
	plain := security.Token()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if exists, e := adminExists(ctx, tx); e != nil {
		return "", e
	} else if !exists {
		return "", ErrSetup
	}
	if _, e = tx.ExecContext(ctx, "UPDATE auth_tokens SET consumed_at=? WHERE purpose='reset' AND consumed_at IS NULL", now.Unix()); e != nil {
		return "", e
	}
	if _, e = issue(ctx, tx, "reset", security.Hash(plain), resetTTL, 1, nil, "", now); e != nil {
		return "", e
	}
	return plain, tx.Commit()
}

// BeginReset spends the reset token and starts an enrolment. Nothing about the
// current password, factor or sessions changes until a code confirms.
func (s *Store) BeginReset(ctx context.Context, resetToken, enrolHash, sealed string, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	t, e := use(ctx, tx, "reset", security.Hash(resetToken), now)
	if e != nil {
		return ErrToken
	}
	if e = consume(ctx, tx, t.ID, now); e != nil {
		return e
	}
	if _, e = issue(ctx, tx, "enrol", enrolHash, enrolTTL, codeTries, &t.ID, sealed, now); e != nil {
		return e
	}
	return tx.Commit()
}

// EnrolmentPayload returns a live enrolment's sealed payload without spending
// an attempt, so a reloaded page can show the key again.
func (s *Store) EnrolmentPayload(ctx context.Context, enrolHash string, now time.Time) (string, error) {
	var payload string
	e := s.DB.QueryRowContext(ctx, "SELECT sealed_payload FROM auth_tokens WHERE token_hash=? AND purpose='enrol' AND consumed_at IS NULL AND expires_at>? AND attempts<max_attempts", enrolHash, now.Unix()).Scan(&payload)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrToken
	}
	return payload, e
}

// Confirmed is what a verified enrolment code yields.
type Confirmed struct {
	Enrolment
	SecretSealed string // under the administrator's own context
	Step         int64
}

// ConfirmEnrolment completes setup, a reset or an upgrade in one transaction.
// verify unseals the payload and checks the code; its error (a wrong or reused
// code) is returned only after the spent attempt has been committed.
func (s *Store) ConfirmEnrolment(ctx context.Context, enrolHash, sessionHash, scope, requestID string, now time.Time, verify func(sealed string) (Confirmed, error)) (string, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	t, e := use(ctx, tx, "enrol", enrolHash, now)
	if e != nil {
		return "", ErrToken
	}
	c, verr := verify(t.Payload)
	if verr != nil {
		if e = spendFailure(ctx, tx, t, now); e != nil {
			return "", e
		}
		if e = tx.Commit(); e != nil {
			return "", e
		}
		return "", verr
	}
	exists, e := adminExists(ctx, tx)
	if e != nil {
		return "", e
	}
	parentPurpose := ""
	if t.Parent.Valid {
		if e = tx.QueryRowContext(ctx, "SELECT purpose FROM auth_tokens WHERE id=? AND consumed_at IS NOT NULL", t.Parent.Int64).Scan(&parentPurpose); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return "", e
		}
	}
	// A verified code for an enrolment that can no longer apply spends it for good.
	stale := func() (string, error) {
		if e := consume(ctx, tx, t.ID, now); e != nil {
			return "", e
		}
		return "", commitThen(tx, ErrToken)
	}
	switch c.Kind {
	case "setup":
		if parentPurpose != "setup" || exists {
			return stale()
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO administrator(id,email,password_hash,created_at,totp_secret_sealed,totp_last_step) VALUES (1,?,?,?,?,?)",
			c.Email, c.PasswordHash, now.Unix(), c.SecretSealed, c.Step); e != nil {
			return "", e
		}
	case "upgrade":
		var current string
		if e = tx.QueryRowContext(ctx, "SELECT password_hash FROM administrator WHERE id=1").Scan(&current); e != nil {
			return stale()
		}
		if security.Hash(current) != c.Binding {
			return stale()
		}
		// Only the first enrolment can set a factor; a later one must go through a reset.
		r, e := tx.ExecContext(ctx, "UPDATE administrator SET totp_secret_sealed=?, totp_last_step=?, code_failures=0, code_locked_until=0 WHERE id=1 AND totp_secret_sealed IS NULL AND password_hash=?", c.SecretSealed, c.Step, current)
		if e != nil {
			return "", e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return stale()
		}
	case "reset":
		if parentPurpose != "reset" || !exists {
			return stale()
		}
		var newest int64
		if e = tx.QueryRowContext(ctx, "SELECT COALESCE(max(id),0) FROM auth_tokens WHERE purpose='reset'").Scan(&newest); e != nil {
			return "", e
		}
		if newest != t.Parent.Int64 {
			return stale() // a newer reset was issued
		}
		if on, e := maintenance(ctx, tx); e != nil || on {
			// A rollback would bring the old credentials back; resets wait for the update.
			return "", ErrMaintenance
		}
		if _, e = tx.ExecContext(ctx, "UPDATE administrator SET password_hash=?, totp_secret_sealed=?, totp_last_step=?, code_failures=0, code_locked_until=0 WHERE id=1", c.PasswordHash, c.SecretSealed, c.Step); e != nil {
			return "", e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM sessions"); e != nil {
			return "", e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM auth_tokens WHERE (purpose IN ('pending','enrol','reset') AND id<>? AND id<>?)", t.ID, t.Parent.Int64); e != nil {
			return "", e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE auth_tokens SET consumed_at=? WHERE purpose='reset' AND consumed_at IS NULL", now.Unix()); e != nil {
			return "", e
		}
	default:
		return stale()
	}
	if c.Scope != "" && c.Scope != scope {
		return stale() // the access mode or origin changed since the enrolment began
	}
	action, event, summary := "auth.second_factor", "second_factor_enrolled", "A second factor was enrolled for the administrator"
	if c.Kind == "reset" {
		action, event, summary = "auth.reset", "admin_reset", "Administrator access was reset over SSH"
	}
	if e = audit(ctx, tx, AuditEntry{At: now.Unix(), AdminID: 1, Action: action, RequestID: requestID, Detail: `{"kind":"` + c.Kind + `"}`}); e != nil {
		return "", e
	}
	if e = RecordAlertEvent(ctx, tx, event, event+":"+now.UTC().Format(time.RFC3339), summary, "If this was not you, reset access over SSH with pgfyctl reset-admin.", now); e != nil {
		return "", e
	}
	if e = addSession(ctx, tx, sessionHash, scope, now); e != nil {
		return "", e
	}
	if e = consume(ctx, tx, t.ID, now); e != nil {
		return "", e
	}
	var email string
	if e = tx.QueryRowContext(ctx, "SELECT email FROM administrator WHERE id=1").Scan(&email); e != nil {
		return "", e
	}
	return email, tx.Commit()
}

type pendingPayload struct {
	Binding string `json:"binding"`
	Scope   string `json:"scope"`
}

// Factor reports whether the administrator has a second factor, and the
// current password hash the password step is bound to.
func (s *Store) Factor(ctx context.Context) (enrolled bool, passwordHash string, e error) {
	var secret sql.NullString
	e = s.DB.QueryRowContext(ctx, "SELECT totp_secret_sealed, password_hash FROM administrator WHERE id=1").Scan(&secret, &passwordHash)
	return secret.Valid && secret.String != "", passwordHash, e
}

// BeginPending records a successful password step. It is bound to the
// password it proved and to the session scope; it is not a session.
func (s *Store) BeginPending(ctx context.Context, pendingHash, passwordHash, scope string, now time.Time) error {
	payload, _ := json.Marshal(pendingPayload{Binding: security.Hash(passwordHash), Scope: scope})
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = issue(ctx, tx, "pending", pendingHash, pendingTTL, codeTries, nil, string(payload), now); e != nil {
		return e
	}
	return tx.Commit()
}

// ConfirmPending checks a code against the stored factor. Wrong codes count
// against the administrator across pending tokens and restarts: every fifth in
// a row doubles a lockout from one minute up to an hour.
func (s *Store) ConfirmPending(ctx context.Context, pendingHash, sessionHash, scope string, now time.Time, verify func(secretSealed string, lastStep int64) (int64, error)) (string, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var email, passwordHash string
	var secret sql.NullString
	var lastStep, failures, lockedUntil int64
	if e = tx.QueryRowContext(ctx, "SELECT email, password_hash, totp_secret_sealed, totp_last_step, code_failures, code_locked_until FROM administrator WHERE id=1").
		Scan(&email, &passwordHash, &secret, &lastStep, &failures, &lockedUntil); e != nil {
		return "", ErrToken
	}
	if lockedUntil > now.Unix() {
		return "", &LockedError{Seconds: lockedUntil - now.Unix()} // decided before anything is spent or written
	}
	t, e := use(ctx, tx, "pending", pendingHash, now)
	if e != nil {
		return "", ErrToken
	}
	var p pendingPayload
	if json.Unmarshal([]byte(t.Payload), &p) != nil || p.Binding != security.Hash(passwordHash) || p.Scope != scope || !secret.Valid {
		// The password changed, or the scope did: this password step no longer counts.
		if e = consume(ctx, tx, t.ID, now); e != nil {
			return "", e
		}
		return "", commitThen(tx, ErrToken)
	}
	step, verr := verify(secret.String, lastStep)
	if verr != nil {
		if e = spendFailure(ctx, tx, t, now); e != nil {
			return "", e
		}
		if errors.Is(verr, ErrCode) {
			failures++
			locked := int64(0)
			if failures%5 == 0 {
				lock := time.Minute << (failures/5 - 1)
				if failures/5 > 7 || lock > time.Hour {
					lock = time.Hour
				}
				locked = now.Add(lock).Unix()
			}
			if _, e = tx.ExecContext(ctx, "UPDATE administrator SET code_failures=?, code_locked_until=CASE WHEN ?>0 THEN ? ELSE code_locked_until END WHERE id=1", failures, locked, locked); e != nil {
				return "", e
			}
			if failures%10 == 0 {
				if e = RecordAlertEvent(ctx, tx, "second_factor_failures", "second_factor_failures:"+now.UTC().Format(time.RFC3339), "Repeated wrong sign-in codes",
					"Someone who knows the administrator password entered many wrong codes. If it was not you, change the password with pgfyctl reset-admin.", now); e != nil {
					return "", e
				}
			}
		}
		return "", commitThen(tx, verr)
	}
	// A code is accepted once: a replay updates nothing.
	r, e := tx.ExecContext(ctx, "UPDATE administrator SET totp_last_step=?, code_failures=0, code_locked_until=0 WHERE id=1 AND totp_last_step<?", step, step)
	if e != nil {
		return "", e
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return "", commitThen(tx, ErrCodeReused)
	}
	if e = addSession(ctx, tx, sessionHash, scope, now); e != nil {
		return "", e
	}
	if e = consume(ctx, tx, t.ID, now); e != nil {
		return "", e
	}
	return email, tx.Commit()
}

// commitThen keeps what a failed attempt must leave behind (the spent attempt,
// the failure count) and then reports the failure.
func commitThen(tx *sql.Tx, failure error) error {
	if e := tx.Commit(); e != nil {
		return e
	}
	return failure
}

// LockedError says how long code entry stays locked.
type LockedError struct{ Seconds int64 }

func (e *LockedError) Error() string        { return ErrCodeLocked.Error() }
func (e *LockedError) Is(target error) bool { return target == ErrCodeLocked }

// Abandon ends a pending or enrolment step the user walked away from.
func (s *Store) Abandon(ctx context.Context, hash string, now time.Time) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE auth_tokens SET consumed_at=? WHERE token_hash=? AND purpose IN ('pending','enrol') AND consumed_at IS NULL", now.Unix(), hash)
	return e
}

// EnrolmentUsable is the read-only form of the checks a confirmation makes.
func (s *Store) EnrolmentUsable(ctx context.Context, enrolHash, kind string) bool {
	var parent sql.NullInt64
	if e := s.DB.QueryRowContext(ctx, "SELECT parent_id FROM auth_tokens WHERE token_hash=?", enrolHash).Scan(&parent); e != nil {
		return false
	}
	var admins int
	s.DB.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&admins)
	switch kind {
	case "setup":
		return admins == 0
	case "reset":
		var newest int64
		s.DB.QueryRowContext(ctx, "SELECT COALESCE(max(id),0) FROM auth_tokens WHERE purpose='reset'").Scan(&newest)
		return admins == 1 && parent.Valid && newest == parent.Int64
	}
	return true
}

func (s *Store) AdminEmail(ctx context.Context) (string, error) {
	var email string
	e := s.DB.QueryRowContext(ctx, "SELECT email FROM administrator WHERE id=1").Scan(&email)
	return email, e
}
