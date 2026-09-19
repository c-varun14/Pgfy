// Package hba renders the dashboard-managed PostgreSQL access rules. The host
// pins system roles and the final rejects outside this file, so the worst a
// bug here can do is admit or exclude project roles.
package hba

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxAddresses = 32

type Rule struct {
	Database  string
	Role      string
	Addresses []string
}

type Postgres interface {
	HBAFileErrors(ctx context.Context) ([]string, error)
	Reload(ctx context.Context) (time.Time, error)
}

type Manager struct {
	Dir          string // managed directory mounted into PostgreSQL
	TunnelSource string // CIDR the loopback-published port appears from
	PG           Postgres
	mu           sync.Mutex
}

// Normalize validates a user-supplied allowlist and returns canonical CIDRs.
func Normalize(addresses []string) ([]string, error) {
	if len(addresses) > MaxAddresses {
		return nil, fmt.Errorf("at most %d addresses are allowed", MaxAddresses)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range addresses {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		prefix, e := netip.ParsePrefix(value)
		if e != nil {
			addr, ae := netip.ParseAddr(value)
			if ae != nil {
				return nil, fmt.Errorf("%q is not an IP address or CIDR range", value)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("%q: write IPv4 addresses without an IPv6 prefix", value)
		}
		if !seen[prefix.String()] {
			seen[prefix.String()] = true
			out = append(out, prefix.String())
		}
	}
	return out, nil
}

func Render(tunnelSource string, rules []Rule) string {
	var b strings.Builder
	b.WriteString("# Written by the Pgfy dashboard. Project roles only; system roles are pinned by the host.\n")
	sorted := append([]Rule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Database < sorted[j].Database })
	for _, r := range sorted {
		if tunnelSource != "" {
			fmt.Fprintf(&b, "host %s %s %s scram-sha-256\n", r.Database, r.Role, tunnelSource)
		}
		for _, a := range r.Addresses {
			fmt.Fprintf(&b, "hostssl %s %s %s scram-sha-256\n", r.Database, r.Role, a)
		}
	}
	return b.String()
}

// Apply writes the rules, validates them through PostgreSQL's own parser,
// reloads, and restores the previous file if anything is rejected.
func (m *Manager) Apply(ctx context.Context, rules []Rule) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target := filepath.Join(m.Dir, "projects.conf")
	previous, e := os.ReadFile(target)
	hadPrevious := e == nil
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return time.Time{}, e
	}
	if e := write(target, Render(m.TunnelSource, rules)); e != nil {
		return time.Time{}, e
	}
	restore := func() {
		if hadPrevious {
			_ = write(target, string(previous))
		} else {
			_ = os.Remove(target)
		}
	}
	problems, e := m.PG.HBAFileErrors(ctx)
	if e != nil {
		restore()
		return time.Time{}, fmt.Errorf("policy could not be validated: %w", e)
	}
	if len(problems) > 0 {
		restore()
		return time.Time{}, errors.New("PostgreSQL rejected the policy: " + strings.Join(problems, "; "))
	}
	loaded, e := m.PG.Reload(ctx)
	if e != nil {
		restore()
		return time.Time{}, e
	}
	return loaded, nil
}

func write(target, content string) error {
	tmp := target + ".tmp"
	if e := os.WriteFile(tmp, []byte(content), 0o644); e != nil {
		return e
	}
	return os.Rename(tmp, target)
}
