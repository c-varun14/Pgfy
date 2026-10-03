package hoststatus

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func paths(t *testing.T) Paths {
	dir := t.TempDir()
	return Paths{Status: filepath.Join(dir, "host-status.json"), CertSync: filepath.Join(dir, "cert-sync.json"), TLSState: filepath.Join(dir, "state.json"), Certificate: filepath.Join(dir, "server.crt")}
}

func writeCert(t *testing.T, path string, notAfter time.Time) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "db.example.com"}, NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644)
}

func TestFreshStaleAndMissing(t *testing.T) {
	p := paths(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if s := Read(p, "https", now); s.State != "unknown" || s.NTPSynchronized != nil || len(s.Disks) != 0 {
		t.Fatal("a missing file must read as unknown", s)
	}
	os.WriteFile(p.Status, []byte(`{"version":1,"written_at":"2026-09-24T11:57:00Z","disks":[{"name":"root","device":7,"total_bytes":1000,"free_bytes":100},{"name":"workspace","device":7,"total_bytes":1000,"free_bytes":500},{"name":"postgres","error":"could not be measured"}],"ntp":{"synchronized":false}}`), 0644)
	s := Read(p, "https", now)
	if s.State != "ok" || s.NTPSynchronized == nil || *s.NTPSynchronized {
		t.Fatal(s)
	}
	if !s.Disks[0].Low || s.Disks[1].Low || s.Disks[2].Low || s.Disks[2].FreePercent != 0 || s.Disks[2].Error == "" {
		t.Fatal("disk thresholds", s.Disks)
	}
	if Read(p, "https", now.Add(20*time.Minute)).State != "stale" {
		t.Fatal("an old file must read as stale")
	}
	os.WriteFile(p.Status, []byte(`{not json`), 0644)
	if Read(p, "https", now).State != "unknown" {
		t.Fatal("invalid JSON must read as unknown")
	}
}

func TestCertificateExpiryComesFromTheServedCertificate(t *testing.T) {
	p := paths(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	os.WriteFile(p.TLSState, []byte(`{"state":"trusted","issuer":"CN=R11","not_after":"stale text"}`), 0644)
	writeCert(t, p.Certificate, now.Add(10*24*time.Hour))
	os.WriteFile(p.CertSync, []byte(`{"at":"2026-09-24T03:00:00Z","ok":false,"message":"Caddy has not obtained a certificate"}`), 0644)
	c := Read(p, "https", now).Certificate
	if c.State != "trusted" || c.ExpiresAt == nil || *c.ExpiresAt != now.Add(10*24*time.Hour).Unix() || !c.Expiring {
		t.Fatal(c)
	}
	if c.LastSync == nil || c.LastSync.OK || c.LastSync.Message == "" {
		t.Fatal("the last sync result is missing", c.LastSync)
	}
	writeCert(t, p.Certificate, now.Add(60*24*time.Hour))
	if Read(p, "https", now).Certificate.Expiring {
		t.Fatal("a certificate with 60 days left is not expiring")
	}
	writeCert(t, p.Certificate, now.Add(-time.Hour))
	if c := Read(p, "https", now).Certificate; !c.Expired || !c.Expiring {
		t.Fatal("an expired certificate", c)
	}
	os.WriteFile(p.TLSState, []byte(`{"state":"placeholder"}`), 0644)
	writeCert(t, p.Certificate, now.Add(3650*24*time.Hour))
	if c := Read(p, "https", now).Certificate; c.ExpiresAt != nil {
		t.Fatal("the placeholder's expiry is not a real one", c)
	}
	os.WriteFile(p.TLSState, []byte(`{"state":"trusted"}`), 0644)
	os.WriteFile(p.Certificate, []byte("garbage"), 0644)
	if c := Read(p, "https", now).Certificate; c.ExpiresAt != nil || c.Expiring {
		t.Fatal("an unreadable certificate has no expiry", c)
	}
	if c := Read(p, "tunnel", now).Certificate; c.State != "not_used" || c.LastSync != nil {
		t.Fatal("tunnel mode has no database certificate", c)
	}
}
