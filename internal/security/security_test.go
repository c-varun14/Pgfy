package security

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPassword(t *testing.T) {
	h, e := Password("a sufficiently long passphrase")
	if e != nil {
		t.Fatal(e)
	}
	if !VerifyPassword(h, "a sufficiently long passphrase") || VerifyPassword(h, "a different passphrase") {
		t.Fatal("password verification mismatch")
	}
	if VerifyPassword("$argon2id$v=19$m=999999999,t=3,p=1$x$y", "password") {
		t.Fatal("unbounded parameters accepted")
	}
	if _, e = Password("short"); e == nil {
		t.Fatal("short password accepted")
	}
}
func TestVault(t *testing.T) {
	v, e := NewVault(bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	a, b := v.Seal("project:1", []byte("secret")), v.Seal("project:1", []byte("secret"))
	if a == b {
		t.Fatal("nonce reused")
	}
	plain, e := v.Open("project:1", a)
	if e != nil || string(plain) != "secret" {
		t.Fatal("round trip failed")
	}
	if _, e = v.Open("project:2", a); e == nil {
		t.Fatal("wrong context accepted")
	}
	if _, e = v.Open("project:1", a[:len(a)-3]+"AAA"); e == nil {
		t.Fatal("tampering accepted")
	}
	other, _ := NewVault(bytes.Repeat([]byte{2}, 32))
	if _, e = other.Open("project:1", a); e == nil {
		t.Fatal("wrong key accepted")
	}
	if v.CSRF("session", "https") == v.CSRF("session", "tunnel") {
		t.Fatal("CSRF scopes collide")
	}
	path := filepath.Join(t.TempDir(), "key")
	if e = os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadVault(path); e == nil {
		t.Fatal("insecure key permissions accepted")
	}
}
