package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// TOTP follows RFC 6238 with the parameters every authenticator app supports:
// HMAC-SHA1, six digits, 30-second steps. The key is entered by hand; there is
// no QR code and no dependency.
const (
	TOTPPeriod = 30
	TOTPDigits = 6
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns 20 random bytes, the size RFC 4226 recommends.
func NewTOTPSecret() []byte {
	secret := make([]byte, 20)
	if _, e := rand.Read(secret); e != nil {
		panic(e)
	}
	return secret
}

// TOTPKey is the secret as an authenticator app expects it typed: base32,
// grouped in fours for reading.
func TOTPKey(secret []byte) string {
	encoded := totpEncoding.EncodeToString(secret)
	var groups []string
	for i := 0; i < len(encoded); i += 4 {
		groups = append(groups, encoded[i:min(i+4, len(encoded))])
	}
	return strings.Join(groups, " ")
}

// HOTP is RFC 4226 with dynamic truncation to the requested number of digits.
func HOTP(secret []byte, counter uint64, digits int) string {
	mac := hmac.New(sha1.New, secret)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1)
	for i := 0; i < digits; i++ {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}

func TOTPStep(now time.Time) int64 { return now.Unix() / TOTPPeriod }

// TOTPCode is the code for a given step.
func TOTPCode(secret []byte, step int64) string { return HOTP(secret, uint64(step), TOTPDigits) }

// VerifyTOTP accepts the current step and one either side for clock skew, and
// only steps newer than the last accepted one, so a code works once.
// reused reports a correct code whose step was already accepted.
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (step int64, ok, reused bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false, false
	}
	current := TOTPStep(now)
	for candidate := current - 1; candidate <= current+1; candidate++ {
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, candidate)), []byte(code)) == 1 {
			if candidate <= lastStep {
				return candidate, false, true
			}
			return candidate, true, false
		}
	}
	return 0, false, false
}
