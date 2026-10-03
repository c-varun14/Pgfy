package security

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA-1, eight digits.
func TestTOTPMatchesTheRFCVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := map[int64]string{59: "94287082", 1111111109: "07081804", 1111111111: "14050471", 1234567890: "89005924", 2000000000: "69279037", 20000000000: "65353130"}
	for unix, want := range vectors {
		if got := HOTP(secret, uint64(unix/TOTPPeriod), 8); got != want {
			t.Errorf("%d: %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyAllowsOneStepOfSkewAndNoReplay(t *testing.T) {
	secret := NewTOTPSecret()
	now := time.Unix(1_800_000_015, 0)
	step := TOTPStep(now)
	for _, s := range []int64{step - 1, step, step + 1} {
		if got, ok, _ := VerifyTOTP(secret, TOTPCode(secret, s), now, 0); !ok || got != s {
			t.Fatal("skew", s)
		}
	}
	if _, ok, _ := VerifyTOTP(secret, TOTPCode(secret, step-2), now, 0); ok {
		t.Fatal("two steps old accepted")
	}
	if _, ok, reused := VerifyTOTP(secret, TOTPCode(secret, step), now, step); ok || !reused {
		t.Fatal("a code was accepted twice")
	}
	if _, ok, _ := VerifyTOTP(secret, "12345", now, 0); ok {
		t.Fatal("short code accepted")
	}
	key := TOTPKey(secret)
	if len(strings.ReplaceAll(key, " ", "")) != 32 || strings.Contains(key, "=") {
		t.Fatal(key)
	}
}
