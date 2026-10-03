// Package hoststatus reads what the host records about itself — disk, clock and
// the database certificate — for the dashboard and for alerts. The host writes
// these files; the application only reads them, from its read-only config mount.
package hoststatus

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"time"
)

const (
	// StaleAfter: the host timer writes every five minutes.
	StaleAfter = 15 * time.Minute
	// LowDiskPercent is the free share under which a filesystem is reported low.
	LowDiskPercent = 15.0
	// ExpiringWithin is when a certificate starts to count as expiring.
	ExpiringWithin = 14 * 24 * time.Hour
)

type Paths struct {
	Status      string // host-status.json, written by the host timer
	CertSync    string // cert-sync.json, written by each certificate delivery
	TLSState    string // postgres-tls/state.json
	Certificate string // postgres-tls/server.crt, what PostgreSQL serves
}

type Disk struct {
	Name        string  `json:"name"`
	Device      uint64  `json:"device,omitempty"`
	TotalBytes  int64   `json:"total_bytes"`
	FreeBytes   int64   `json:"free_bytes"`
	FreePercent float64 `json:"free_percent"`
	Low         bool    `json:"low"`
	Error       string  `json:"error,omitempty"`
}

type CertSync struct {
	At      string `json:"at"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Certificate struct {
	// State is trusted, placeholder, unknown, or not_used in tunnel mode.
	State     string    `json:"state"`
	Issuer    string    `json:"issuer,omitempty"`
	ExpiresAt *int64    `json:"expires_at"`
	Expiring  bool      `json:"expiring"`
	Expired   bool      `json:"expired"`
	LastSync  *CertSync `json:"last_sync,omitempty"`
}

type Status struct {
	// State is ok, stale (the host timer stopped writing) or unknown (no readable file).
	State           string      `json:"state"`
	WrittenAt       int64       `json:"written_at"`
	Disks           []Disk      `json:"disks"`
	NTPSynchronized *bool       `json:"ntp_synchronized"`
	Certificate     Certificate `json:"certificate"`
}

func Read(p Paths, mode string, now time.Time) Status {
	status := Status{State: "unknown", Disks: []Disk{}}
	var file struct {
		WrittenAt string `json:"written_at"`
		Disks     []Disk `json:"disks"`
		NTP       struct {
			Synchronized *bool `json:"synchronized"`
		} `json:"ntp"`
	}
	if b, e := os.ReadFile(p.Status); e == nil && json.Unmarshal(b, &file) == nil {
		if at, e := time.Parse(time.RFC3339, file.WrittenAt); e == nil {
			status.WrittenAt = at.Unix()
			status.State = "ok"
			if now.Sub(at) > StaleAfter {
				status.State = "stale"
			}
			status.NTPSynchronized = file.NTP.Synchronized
			for _, d := range file.Disks {
				if d.Error == "" && d.TotalBytes > 0 {
					d.FreePercent = float64(d.FreeBytes) * 100 / float64(d.TotalBytes)
					d.Low = d.FreePercent < LowDiskPercent
				}
				status.Disks = append(status.Disks, d)
			}
		}
	}
	status.Certificate = certificate(p, mode, now)
	return status
}

func certificate(p Paths, mode string, now time.Time) Certificate {
	if mode != "https" {
		return Certificate{State: "not_used"}
	}
	c := Certificate{State: "unknown"}
	var state struct {
		State  string `json:"state"`
		Issuer string `json:"issuer"`
	}
	if b, e := os.ReadFile(p.TLSState); e == nil && json.Unmarshal(b, &state) == nil && state.State != "" {
		c.State, c.Issuer = state.State, state.Issuer
	}
	// The served certificate is the truth about expiry, whatever the state file
	// says; the self-signed placeholder's ten years are not an expiry to report.
	if b, e := os.ReadFile(p.Certificate); e == nil && c.State == "trusted" {
		if block, _ := pem.Decode(b); block != nil {
			if cert, e := x509.ParseCertificate(block.Bytes); e == nil {
				at := cert.NotAfter.Unix()
				c.ExpiresAt = &at
				c.Expiring = cert.NotAfter.Sub(now) < ExpiringWithin
				c.Expired = !now.Before(cert.NotAfter)
			}
		}
	}
	var sync CertSync
	if b, e := os.ReadFile(p.CertSync); e == nil && json.Unmarshal(b, &sync) == nil && sync.At != "" {
		c.LastSync = &sync
	}
	return c
}
