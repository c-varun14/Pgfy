package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func Password(password string) (string, error) {
	if utf8.RuneCountInString(password) < 15 || len(password) > 256 {
		return "", errors.New("password must contain at least 15 characters and at most 256 UTF-8 bytes")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	sum := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

func VerifyPassword(encoded, password string) bool {
	// Only accept our bounded parameters: a damaged hash must not allocate arbitrary memory.
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=1" || len(password) > 256 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return subtle.ConstantTimeCompare(want, got) == 1
}

type Vault struct {
	aead cipher.AEAD
	key  []byte
}

func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("installation key must contain 32 bytes")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &Vault{aead: aead, key: append([]byte(nil), key...)}, nil
}
func LoadVault(path string) (*Vault, error) {
	stat, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return nil, errors.New("installation key permissions must be 0600 or stricter")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	return NewVault(b)
}
func (v *Vault) Seal(context string, plain []byte) string {
	nonce := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		panic(e)
	}
	return "v1:" + base64.RawURLEncoding.EncodeToString(v.aead.Seal(nonce, nonce, plain, []byte("pgfy:v1:"+context)))
}
func (v *Vault) Open(context, encoded string) ([]byte, error) {
	if !strings.HasPrefix(encoded, "v1:") {
		return nil, errors.New("unsupported secret version")
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if e != nil || len(b) < v.aead.NonceSize() {
		return nil, errors.New("invalid encrypted secret")
	}
	n := v.aead.NonceSize()
	return v.aead.Open(nil, b[:n], b[n:], []byte("pgfy:v1:"+context))
}
func (v *Vault) CSRF(session, scope string) string {
	h := hmac.New(sha256.New, v.key)
	h.Write([]byte("csrf:" + scope + ":" + session))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
