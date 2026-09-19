package hba

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePG struct {
	problems []string
	reloads  int
	fail     bool
}

func (f *fakePG) HBAFileErrors(context.Context) ([]string, error) { return f.problems, nil }
func (f *fakePG) Reload(context.Context) (time.Time, error) {
	f.reloads++
	if f.fail {
		return time.Time{}, errors.New("reload failed")
	}
	return time.Now(), nil
}

func TestNormalize(t *testing.T) {
	got, e := Normalize([]string{"203.0.113.10", " 198.51.100.0/24 ", "2001:db8::1", "203.0.113.10/32", ""})
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"203.0.113.10/32", "198.51.100.0/24", "2001:db8::1/128"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatal(got)
	}
	for _, bad := range []string{"example.com", "10.0.0.0/33", "::ffff:1.2.3.4", "1.2.3"} {
		if _, e := Normalize([]string{bad}); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	many := make([]string, MaxAddresses+1)
	for i := range many {
		many[i] = "10.0.0.1"
	}
	if _, e := Normalize(many); e == nil {
		t.Fatal("accepted too many addresses")
	}
}

func TestRenderOnlyAdmitsProjectRolesWithTLSRemotely(t *testing.T) {
	out := Render("172.20.242.0/24", []Rule{{Database: "app_b", Role: "app_b", Addresses: []string{"0.0.0.0/0"}}, {Database: "app_a", Role: "app_a", Addresses: nil}})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[1] != "host app_a app_a 172.20.242.0/24 scram-sha-256" || lines[2] != "host app_b app_b 172.20.242.0/24 scram-sha-256" || lines[3] != "hostssl app_b app_b 0.0.0.0/0 scram-sha-256" {
		t.Fatal(out)
	}
	if strings.Contains(out, "trust") || strings.Contains(out, "pgfy_") {
		t.Fatal(out)
	}
}

func TestApplyRestoresPreviousFileWhenRejected(t *testing.T) {
	dir := t.TempDir()
	pg := &fakePG{}
	m := &Manager{Dir: dir, TunnelSource: "172.20.242.0/24", PG: pg}
	if _, e := m.Apply(context.Background(), []Rule{{Database: "app_a", Role: "app_a"}}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "projects.conf"))
	pg.problems = []string{"line 2: bad"}
	if _, e := m.Apply(context.Background(), []Rule{{Database: "app_b", Role: "app_b"}}); e == nil {
		t.Fatal("expected rejection")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "projects.conf"))
	if string(before) != string(after) || pg.reloads != 1 {
		t.Fatal(string(after), pg.reloads)
	}
	pg.problems = nil
	pg.fail = true
	if _, e := m.Apply(context.Background(), []Rule{{Database: "app_b", Role: "app_b"}}); e == nil {
		t.Fatal("expected reload failure")
	}
	after, _ = os.ReadFile(filepath.Join(dir, "projects.conf"))
	if string(before) != string(after) {
		t.Fatal(string(after))
	}
}
