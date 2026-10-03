package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

// Sealing contexts: an enrolment's payload names its own token, and the stored
// factor names the administrator, so one sealed value can never stand in for another.
const adminFactorContext = "admin:totp:1"

func enrolContext(tokenHash string) string { return "auth_token:enrol:" + tokenHash }

// cookie names follow the session cookie's rule: __Host- and Secure in HTTPS
// mode, host-only in tunnel mode; always HttpOnly and SameSite=Strict.
func (s *Server) cookie(kind string) string {
	if s.Config.Mode == "https" {
		return "__Host-pgfy_" + kind
	}
	return "pgfy_tunnel_" + kind
}

func (s *Server) setStepCookie(w http.ResponseWriter, kind, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: s.cookie(kind), Value: value, Path: "/", HttpOnly: true, Secure: s.Config.Mode == "https", SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds()), Expires: s.Now().Add(ttl)})
}

func (s *Server) clearCookie(w http.ResponseWriter, kind string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookie(kind), Value: "", Path: "/", HttpOnly: true, Secure: s.Config.Mode == "https", SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *Server) stepCookie(r *http.Request, kind string) (string, bool) {
	c, e := r.Cookie(s.cookie(kind))
	if e != nil || len(c.Value) != 43 {
		return "", false
	}
	return c.Value, true
}

// enrolView is what an authenticator app needs typed in; no QR code.
func (s *Server) enrolView(secret []byte, account string) map[string]any {
	return map[string]any{"next": "enrol", "key": security.TOTPKey(secret), "account": account, "issuer": "Pgfy",
		"algorithm": "SHA1", "digits": security.TOTPDigits, "period": security.TOTPPeriod, "server_time": s.Now().Unix()}
}

// startEnrolment seals a fresh secret into an enrolment and sets its cookie.
func (s *Server) startEnrolment(w http.ResponseWriter, r *http.Request, en store.Enrolment, begin func(hash, sealed string) error) bool {
	en.Secret = security.NewTOTPSecret()
	en.Scope = s.scope()
	enrol := security.Token()
	hash := security.Hash(enrol)
	payload, _ := json.Marshal(en)
	if e := begin(hash, s.Vault.Seal(enrolContext(hash), payload)); e != nil {
		switch {
		case errors.Is(e, store.ErrSetup):
			failure(w, 409, "setup_rejected", "Setup token is invalid, expired, or already used.")
		case errors.Is(e, store.ErrToken):
			failure(w, 409, "reset_rejected", "Reset token is invalid, expired, or already used. Issue a new one with pgfyctl reset-admin.")
		default:
			failure(w, 503, "metadata_unavailable", "The sign-in step could not be saved. Run host diagnostics.")
		}
		return false
	}
	s.setStepCookie(w, "enrol", enrol, 10*time.Minute)
	account := en.Email
	if account == "" {
		account, _ = s.Store.AdminEmail(r.Context())
	}
	write(w, 200, s.enrolView(en.Secret, account))
	return true
}

func (s *Server) signedIn(w http.ResponseWriter, email, session string, status int) {
	s.setCookie(w, session)
	write(w, status, map[string]string{"email": email, "csrf_token": s.Vault.CSRF(session, s.scope())})
}

func (s *Server) codeAllowed(w http.ResponseWriter, r *http.Request) bool {
	// Codes have their own bucket and never take an Argon2 slot.
	if !s.codeLimiter.allow(s.client(r), s.Now()) {
		w.Header().Set("Retry-After", "900")
		failure(w, 429, "rate_limited", "Too many attempts. Try again later.")
		return false
	}
	return true
}

func codeFailure(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, store.ErrCode):
		failure(w, 401, "invalid_code", "That code is not right. Check the time on your device and try the current code.")
	case errors.Is(e, store.ErrCodeReused):
		failure(w, 401, "code_reused", "That code was already used. Wait for the next one.")
	case errors.Is(e, store.ErrCodeLocked):
		seconds := int64(60)
		var locked *store.LockedError
		if errors.As(e, &locked) {
			seconds = locked.Seconds
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		failure(w, 429, "code_locked", "Too many wrong codes. Wait a few minutes and try again.")
	case errors.Is(e, store.ErrMaintenance):
		failure(w, 503, "maintenance", "An update is in progress. Finish the reset when it completes.")
	default:
		failure(w, 503, "metadata_unavailable", "Management storage is unavailable.")
	}
}

type codeInput struct {
	Code string `json:"code"`
}

// confirmCode completes the password step with a code from the enrolled factor.
func (s *Server) confirmCode(w http.ResponseWriter, r *http.Request) {
	// Without a pending step there is nothing to guess against; such requests
	// must not use up the budget the administrator needs.
	pending, ok := s.stepCookie(r, "pending")
	if !ok {
		failure(w, 401, "code_expired", "Sign in again.")
		return
	}
	if !s.codeAllowed(w, r) {
		return
	}
	var in codeInput
	if !decode(w, r, &in) {
		return
	}
	now := s.Now()
	session := security.Token()
	email, e := s.Store.ConfirmPending(r.Context(), security.Hash(pending), security.Hash(session), s.scope(), now, func(sealed string, last int64) (int64, error) {
		secret, e := s.Vault.Open(adminFactorContext, sealed)
		if e != nil {
			return 0, store.ErrToken
		}
		step, valid, reused := security.VerifyTOTP(secret, in.Code, now, last)
		if reused {
			return 0, store.ErrCodeReused
		}
		if !valid {
			return 0, store.ErrCode
		}
		return step, nil
	})
	if errors.Is(e, store.ErrToken) {
		s.clearCookie(w, "pending")
		failure(w, 401, "code_expired", "This sign-in expired. Sign in again.")
		return
	}
	if e != nil {
		codeFailure(w, e)
		return
	}
	s.clearCookie(w, "pending")
	s.signedIn(w, email, session, 200)
}

// confirmEnrolment turns an enrolment into a signed-in administrator.
func (s *Server) confirmEnrolment(w http.ResponseWriter, r *http.Request) {
	enrol, ok := s.stepCookie(r, "enrol")
	if !ok {
		failure(w, 401, "enrol_expired", "This setup step expired. Start again.")
		return
	}
	if !s.codeAllowed(w, r) {
		return
	}
	var in codeInput
	if !decode(w, r, &in) {
		return
	}
	now := s.Now()
	hash := security.Hash(enrol)
	session := security.Token()
	email, e := s.Store.ConfirmEnrolment(r.Context(), hash, security.Hash(session), s.scope(), w.Header().Get("X-Request-ID"), now, func(sealed string) (store.Confirmed, error) {
		var c store.Confirmed
		plain, e := s.Vault.Open(enrolContext(hash), sealed)
		if e != nil || json.Unmarshal(plain, &c.Enrolment) != nil {
			return c, store.ErrToken
		}
		step, valid, _ := security.VerifyTOTP(c.Secret, in.Code, now, 0)
		if !valid {
			return c, store.ErrCode
		}
		c.SecretSealed, c.Step = s.Vault.Seal(adminFactorContext, c.Secret), step
		return c, nil
	})
	if errors.Is(e, store.ErrToken) {
		s.clearCookie(w, "enrol")
		failure(w, 401, "enrol_expired", "This setup step expired or was replaced. Start again.")
		return
	}
	if e != nil {
		codeFailure(w, e)
		return
	}
	s.clearCookie(w, "enrol")
	s.signedIn(w, email, session, 200)
}

// currentEnrolment shows the key again after a reload, while the step lives.
func (s *Server) currentEnrolment(w http.ResponseWriter, r *http.Request) {
	enrol, ok := s.stepCookie(r, "enrol")
	if !ok {
		failure(w, 401, "enrol_expired", "No setup step is in progress.")
		return
	}
	hash := security.Hash(enrol)
	sealed, e := s.Store.EnrolmentPayload(r.Context(), hash, s.Now())
	var en store.Enrolment
	if e == nil {
		plain, oe := s.Vault.Open(enrolContext(hash), sealed)
		if oe != nil || json.Unmarshal(plain, &en) != nil {
			e = store.ErrToken
		}
	}
	if e == nil && en.Kind == "upgrade" {
		if enrolled, _, fe := s.Store.Factor(r.Context()); fe != nil || enrolled {
			e = store.ErrToken // a factor was set meanwhile; this key is stale
		}
	}
	if e == nil && !s.Store.EnrolmentUsable(r.Context(), hash, en.Kind) {
		e = store.ErrToken
	}
	if e != nil {
		s.clearCookie(w, "enrol")
		failure(w, 401, "enrol_expired", "This setup step expired. Start again.")
		return
	}
	account := en.Email
	if account == "" {
		account, _ = s.Store.AdminEmail(r.Context())
	}
	write(w, 200, s.enrolView(en.Secret, account))
}

// startReset takes the SSH-issued token and a new password. Nothing changes
// until a code from the new factor confirms it.
func (s *Server) startReset(w http.ResponseWriter, r *http.Request) {
	if !s.beginAuth(w, r) {
		return
	}
	defer func() { <-s.hashSlots }()
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Token) != 43 {
		failure(w, 409, "reset_rejected", "Reset token is invalid, expired, or already used. Issue a new one with pgfyctl reset-admin.")
		return
	}
	hash, e := security.Password(in.Password)
	if e != nil {
		failure(w, 400, "invalid_password", e.Error())
		return
	}
	s.startEnrolment(w, r, store.Enrolment{Kind: "reset", PasswordHash: hash}, func(enrolHash, sealed string) error {
		return s.Store.BeginReset(r.Context(), in.Token, enrolHash, sealed, s.Now())
	})
}

func (s *Server) abandonSteps(r *http.Request) {
	for _, kind := range []string{"pending", "enrol"} {
		if value, ok := s.stepCookie(r, kind); ok {
			_ = s.Store.Abandon(r.Context(), security.Hash(value), s.Now())
		}
	}
}

// abandon ends an unfinished sign-in step ("Back to sign in").
func (s *Server) abandon(w http.ResponseWriter, r *http.Request) {
	s.abandonSteps(r)
	s.clearCookie(w, "pending")
	s.clearCookie(w, "enrol")
	w.WriteHeader(204)
}
